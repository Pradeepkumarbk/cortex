package alertdist

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/weaveworks/cortex/pkg/alertmodel"
	"github.com/weaveworks/cortex/pkg/prom1/storage/metric"

	billing "github.com/weaveworks/billing-client"
	"github.com/weaveworks/common/instrument"
	"github.com/weaveworks/common/user"
	"github.com/weaveworks/cortex/pkg/alertingest/client"
	ingester_client "github.com/weaveworks/cortex/pkg/alertingest/client"
	ring "github.com/weaveworks/cortex/pkg/alertring"
	"github.com/weaveworks/cortex/pkg/util"
)

var errIngestionRateLimitExceeded = errors.New("ingestion rate limit exceeded")

var (
	numClientsDesc = prometheus.NewDesc(
		"cortex_distributor_ingester_clients",
		"The current number of ingester clients.",
		nil, nil,
	)
	labelNameBytes = []byte(model.MetricNameLabel)
)

// Distributor is a storage.SampleAppender and a client.Querier which
// forwards appends and queries to individual ingesters.
type Distributor struct {
	cfg        Config
	ring       ring.ReadRing
	clientsMtx sync.RWMutex
	clients    map[string]client.AlertIngesterClient
	quit       chan struct{}
	done       chan struct{}

	billingClient *billing.Client

	// Per-user rate limiters.
	ingestLimitersMtx sync.Mutex
	ingestLimiters    map[string]*rate.Limiter

	queryDuration          *prometheus.HistogramVec
	receivedSamples        prometheus.Counter
	sendDuration           *prometheus.HistogramVec
	ingesterAppends        *prometheus.CounterVec
	ingesterAppendFailures *prometheus.CounterVec
	ingesterQueries        *prometheus.CounterVec
	ingesterQueryFailures  *prometheus.CounterVec
}

// Config contains the configuration require to
// create a Distributor
type Config struct {
	EnableBilling bool
	BillingConfig billing.Config

	ReplicationFactor   int
	RemoteTimeout       time.Duration
	ClientCleanupPeriod time.Duration
	IngestionRateLimit  float64
	IngestionBurstSize  int
	CompressToIngester  bool

	// for testing
	ingesterClientFactory func(addr string, timeout time.Duration, withCompression bool) (client.AlertIngesterClient, error)
}

// RegisterFlags adds the flags required to config this to the given FlagSet
func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	flag.BoolVar(&cfg.EnableBilling, "distributor.enable-billing", false, "Report number of ingested samples to billing system.")
	cfg.BillingConfig.RegisterFlags(f)

	flag.IntVar(&cfg.ReplicationFactor, "distributor.replication-factor", 3, "The number of ingesters to write to and read from.")
	flag.DurationVar(&cfg.RemoteTimeout, "distributor.remote-timeout", 2*time.Second, "Timeout for downstream ingesters.")
	flag.DurationVar(&cfg.ClientCleanupPeriod, "distributor.client-cleanup-period", 15*time.Second, "How frequently to clean up clients for ingesters that have gone away.")
	flag.Float64Var(&cfg.IngestionRateLimit, "distributor.ingestion-rate-limit", 25000, "Per-user ingestion rate limit in samples per second.")
	flag.IntVar(&cfg.IngestionBurstSize, "distributor.ingestion-burst-size", 50000, "Per-user allowed ingestion burst size (in number of samples).")
	flag.BoolVar(&cfg.CompressToIngester, "distributor.compress-to-ingester", false, "Compress data in calls to ingesters.")
}

// New constructs a new Distributor
func New(cfg Config, ring ring.ReadRing) (*Distributor, error) {
	if 0 > cfg.ReplicationFactor {
		return nil, fmt.Errorf("ReplicationFactor must be greater than zero: %d", cfg.ReplicationFactor)
	}
	if cfg.ingesterClientFactory == nil {
		cfg.ingesterClientFactory = ingester_client.MakeIngesterClient
	}

	var billingClient *billing.Client
	if cfg.EnableBilling {
		var err error
		billingClient, err = billing.NewClient(cfg.BillingConfig)
		if err != nil {
			return nil, err
		}
	}

	d := &Distributor{
		cfg:            cfg,
		ring:           ring,
		clients:        map[string]client.AlertIngesterClient{},
		quit:           make(chan struct{}),
		done:           make(chan struct{}),
		billingClient:  billingClient,
		ingestLimiters: map[string]*rate.Limiter{},
		queryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "cortex",
			Name:      "distributor_query_duration_seconds",
			Help:      "Time spent executing expression queries.",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 20, 30},
		}, []string{"method", "status_code"}),
		receivedSamples: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "cortex",
			Name:      "distributor_received_samples_total",
			Help:      "The total number of received samples.",
		}),
		sendDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "cortex",
			Name:      "distributor_send_duration_seconds",
			Help:      "Time spent sending a sample batch to multiple replicated ingesters.",
			Buckets:   []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
		}, []string{"method", "status_code"}),
		ingesterAppends: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "cortex",
			Name:      "distributor_ingester_appends_total",
			Help:      "The total number of batch appends sent to ingesters.",
		}, []string{"ingester"}),
		ingesterAppendFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "cortex",
			Name:      "distributor_ingester_append_failures_total",
			Help:      "The total number of failed batch appends sent to ingesters.",
		}, []string{"ingester"}),
		ingesterQueries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "cortex",
			Name:      "distributor_ingester_queries_total",
			Help:      "The total number of queries sent to ingesters.",
		}, []string{"ingester"}),
		ingesterQueryFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "cortex",
			Name:      "distributor_ingester_query_failures_total",
			Help:      "The total number of failed queries sent to ingesters.",
		}, []string{"ingester"}),
	}
	go d.Run()
	return d, nil
}

// Run starts the distributor's maintenance loop.
func (d *Distributor) Run() {
	cleanupClients := time.NewTicker(d.cfg.ClientCleanupPeriod)
	for {
		select {
		case <-cleanupClients.C:
			d.removeStaleIngesterClients()
		case <-d.quit:
			close(d.done)
			return
		}
	}
}

// Stop stops the distributor's maintenance loop.
func (d *Distributor) Stop() {
	close(d.quit)
	<-d.done
}

func (d *Distributor) removeStaleIngesterClients() {
	d.clientsMtx.Lock()
	defer d.clientsMtx.Unlock()

	ingesters := map[string]struct{}{}
	for _, ing := range d.ring.GetAll() {
		ingesters[ing.Addr] = struct{}{}
	}

	for addr, client := range d.clients {
		if _, ok := ingesters[addr]; ok {
			continue
		}
		level.Info(util.Logger).Log("msg", "removing stale ingester client", "addr", addr)
		delete(d.clients, addr)

		// Do the gRPC closing in the background since it might take a while and
		// we're holding a mutex.
		go func(addr string, closer io.Closer) {
			if err := closer.Close(); err != nil {
				level.Error(util.Logger).Log("msg", "error closing connection to ingester", "ingester", addr, "err", err)
			}
		}(addr, client.(io.Closer))
	}
}

func (d *Distributor) getClientFor(ingester *ring.IngesterDesc) (client.AlertIngesterClient, error) {
	d.clientsMtx.RLock()
	client, ok := d.clients[ingester.Addr]
	d.clientsMtx.RUnlock()
	if ok {
		return client, nil
	}

	d.clientsMtx.Lock()
	defer d.clientsMtx.Unlock()
	client, ok = d.clients[ingester.Addr]
	if ok {
		return client, nil
	}

	client, err := d.cfg.ingesterClientFactory(ingester.Addr, d.cfg.RemoteTimeout, d.cfg.CompressToIngester)
	if err != nil {
		return nil, err
	}
	d.clients[ingester.Addr] = client
	return client, nil
}

func tokenForLabels(userID string, labels []client.LabelPair) (uint32, error) {
	for _, label := range labels {
		if label.Name.Equal(labelNameBytes) {
			return tokenFor(userID, label.Value), nil
		}
	}
	return 0, fmt.Errorf("No metric name label")
}

func tokenFor(userID string, name []byte) uint32 {
	h := fnv.New32()
	h.Write([]byte(userID))
	h.Write(name)
	return h.Sum32()
}

type messageTracker struct {
	labels      []byte
	message     template.Alert
	minSuccess  int
	maxFailures int
	succeeded   int32
	failed      int32
}

type pushTracker struct {
	samplesPending int32
	samplesFailed  int32
	done           chan struct{}
	err            chan error
}

// Push implements client.IngesterServer
func (d *Distributor) Push(ctx context.Context, req *template.Alert) (*client.WriteResponse, error) {
	userID, err := user.ExtractOrgID(ctx)
	if err != nil {
		return nil, err
	}

	// First we flatten out the request into a list of messages.
	// We use the heuristic of 1 message per TS to size the array.
	// We also work out the hash value at the same time.
	messages := make([]messageTracker, 0, 1)
	keys := make([]uint32, 0, 1)
	key := tokenFor(userID, []byte("alert"))
	keys = append(keys, key)
	messages = append(messages, messageTracker{
		labels:  []byte("alert"),
		message: *req,
	})
	d.receivedSamples.Add(float64(1))

	if len(messages) == 0 {
		return &client.WriteResponse{}, nil
	}

	limiter := d.getOrCreateIngestLimiter(userID)
	if !limiter.AllowN(time.Now(), len(messages)) {
		return nil, errIngestionRateLimitExceeded
	}

	var ingesters [][]*ring.IngesterDesc
	if err := instrument.TimeRequestHistogram(ctx, "Distributor.Push[ring-lookup]", d.sendDuration, func(context.Context) error {
		var err error
		ingesters, err = d.ring.BatchGet(keys, d.cfg.ReplicationFactor, ring.Write)
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	messagesByIngester := map[*ring.IngesterDesc][]*messageTracker{}
	for i := range messages {
		// We need a response from a quorum of ingesters, which is n/2 + 1.
		minSuccess := (len(ingesters[i]) / 2) + 1
		messages[i].minSuccess = minSuccess
		messages[i].maxFailures = len(ingesters[i]) - minSuccess

		// Skip those that have not heartbeated in a while. NB these are still
		// included in the calculation of minSuccess, so if too many failed ingesters
		// will cause the whole write to fail.
		liveIngesters := make([]*ring.IngesterDesc, 0, len(ingesters[i]))
		for _, ingester := range ingesters[i] {
			if d.ring.IsHealthy(ingester) {
				liveIngesters = append(liveIngesters, ingester)
			}
		}

		// This is just a shortcut - if there are not minSuccess available ingesters,
		// after filtering out dead ones, don't even bother trying.
		if len(liveIngesters) < minSuccess {
			return nil, fmt.Errorf("wanted at least %d live ingesters to process write, had %d",
				minSuccess, len(liveIngesters))
		}

		for _, liveIngester := range liveIngesters {
			messageForIngester := messagesByIngester[liveIngester]
			messagesByIngester[liveIngester] = append(messageForIngester, &messages[i])
		}
	}

	pushTracker := pushTracker{
		samplesPending: int32(len(messages)),
		done:           make(chan struct{}),
		err:            make(chan error),
	}
	for ingester, messages := range messagesByIngester {
		go func(ingester *ring.IngesterDesc, messages []*messageTracker) {
			d.sendMessages(ctx, ingester, messages, &pushTracker)
		}(ingester, messages)
	}
	select {
	case err := <-pushTracker.err:
		return nil, err
	case <-pushTracker.done:
		return &client.WriteResponse{}, nil
	}
}

func (d *Distributor) getOrCreateIngestLimiter(userID string) *rate.Limiter {
	d.ingestLimitersMtx.Lock()
	defer d.ingestLimitersMtx.Unlock()

	if limiter, ok := d.ingestLimiters[userID]; ok {
		return limiter
	}

	limiter := rate.NewLimiter(rate.Limit(d.cfg.IngestionRateLimit), d.cfg.IngestionBurstSize)
	d.ingestLimiters[userID] = limiter
	return limiter
}

func (d *Distributor) sendMessages(ctx context.Context, ingester *ring.IngesterDesc, messageTrackers []*messageTracker, pushTracker *pushTracker) {
	err := d.sendMessagesErr(ctx, ingester, messageTrackers)

	// If we succeed, decrement each sample's pending count by one.  If we reach
	// the required number of successful puts on this sample, then decrement the
	// number of pending samples by one.  If we successfully push all samples to
	// min success ingesters, wake up the waiting rpc so it can return early.
	// Similarly, track the number of errors, and if it exceeds maxFailures
	// shortcut the waiting rpc.
	//
	// The use of atomic increments here guarantees only a single sendSamples
	// goroutine will write to either channel.
	for i := range messageTrackers {
		if err != nil {
			if atomic.AddInt32(&messageTrackers[i].failed, 1) <= int32(messageTrackers[i].maxFailures) {
				continue
			}
			if atomic.AddInt32(&pushTracker.samplesFailed, 1) == 1 {
				pushTracker.err <- err
			}
		} else {
			if atomic.AddInt32(&messageTrackers[i].succeeded, 1) != int32(messageTrackers[i].minSuccess) {
				continue
			}
			if atomic.AddInt32(&pushTracker.samplesPending, -1) == 0 {
				pushTracker.done <- struct{}{}
			}
		}
	}
}

func (d *Distributor) sendMessagesErr(ctx context.Context, ingester *ring.IngesterDesc, messages []*messageTracker) error {
	c, err := d.getClientFor(ingester)
	if err != nil {
		return err
	}

	req := &client.WriteRequest{
		Messages: make([]client.MessageTracker, 0, len(messages)),
	}
	for _, msg := range messages {
		var tmpmsg = client.MessageTracker{
			Labels:    string(msg.labels),
			Timestamp: int64(model.Now()),
		}

		b, err := json.Marshal(msg.message)
		if err != nil {
			log.Print(err)
			// return
			tmpmsg.Value = ""
		} else {
			log.Print(string(b))
			tmpmsg.Value = string(b)
		}
		// var buf bytes.Buffer
		// json.NewEncoder(snappy.NewWriter(&buf)).Encode(msg.message)
		// tmpmsg.Value = buf.String()
		level.Info(util.Logger).Log("msg", "pushing json message into message tracker new", "json", tmpmsg.Value)
		//TODO: Moteesh - decode and print the tmpmsg.Value
		req.Messages = append(req.Messages, tmpmsg)
	}

	err = instrument.TimeRequestHistogram(ctx, "Distributor.sendSamples", d.sendDuration, func(ctx context.Context) error {
		log.Print("MMR: pushing message to ingester client")
		_, err := c.Push(ctx, req)
		return err
	})
	d.ingesterAppends.WithLabelValues(ingester.Addr).Inc()
	if err != nil {
		d.ingesterAppendFailures.WithLabelValues(ingester.Addr).Inc()
	}
	return err
}

// Query implements Querier.
func (d *Distributor) Query(ctx context.Context, from, to model.Time, matchers ...*labels.Matcher) (alertmodel.Matrix, error) {
	var matrix alertmodel.Matrix
	err := instrument.TimeRequestHistogram(ctx, "Distributor.Query", d.queryDuration, func(ctx context.Context) error {
		userID, err := user.ExtractOrgID(ctx)
		if err != nil {
			return err
		}

		metricNameMatcher, _, ok := util.ExtractMetricNameMatcherFromMatchers(matchers)

		req, err := util.ToAlertQueryRequest(from, to, matchers)
		if err != nil {
			return err
		}

		// Get ingesters by metricName if one exists, otherwise get all ingesters
		var ingesters []*ring.IngesterDesc
		if ok && metricNameMatcher.Type == labels.MatchEqual {
			ingesters, err = d.ring.Get(tokenFor(userID, []byte(metricNameMatcher.Value)), d.cfg.ReplicationFactor, ring.Read)
			if err != nil {
				return promql.ErrStorage(err)
			}
		} else {
			ingesters = d.ring.GetAll()
		}

		matrix, err = d.queryIngesters(ctx, ingesters, d.cfg.ReplicationFactor, req)
		return promql.ErrStorage(err)
	})
	return matrix, err
}

// Query implements Querier.
func (d *Distributor) queryIngesters(ctx context.Context, ingesters []*ring.IngesterDesc, replicationFactor int, req *client.QueryRequest) (alertmodel.Matrix, error) {
	// We need a response from a quorum of ingesters, where maxErrs is n/2, where n is the replicationFactor
	maxErrs := replicationFactor / 2
	minSuccess := len(ingesters) - maxErrs
	if len(ingesters) < minSuccess {
		return nil, fmt.Errorf("could only find %d ingesters for query. Need at least %d", len(ingesters), minSuccess)
	}

	// Fetch samples from multiple ingesters
	var numErrs int32
	errReceived := make(chan error)
	results := make(chan alertmodel.Matrix, len(ingesters))

	for _, ing := range ingesters {
		go func(ing *ring.IngesterDesc) {
			result, err := d.queryIngester(ctx, ing, req)
			if err != nil {
				if atomic.AddInt32(&numErrs, 1) == int32(maxErrs+1) {
					errReceived <- err
				}
			} else {
				results <- result
			}
		}(ing)
	}

	// Only wait for minSuccess ingesters (or an error), and accumulate the samples
	// by fingerprint, merging them into any existing samples.
	fpToSampleStream := map[model.Fingerprint]*alertmodel.AlertStream{}
	for i := 0; i < minSuccess; i++ {
		select {
		case err := <-errReceived:
			return nil, err

		case result := <-results:
			for _, ss := range result {
				fp := ss.Metric.Fingerprint()
				mss, ok := fpToSampleStream[fp]
				if !ok {
					mss = &alertmodel.AlertStream{
						Metric: ss.Metric,
					}
					fpToSampleStream[fp] = mss
				}
				mss.Values = util.MergeAlertSets(mss.Values, ss.Values)
			}
		}
	}

	result := alertmodel.Matrix{}
	for _, ss := range fpToSampleStream {
		result = append(result, ss)
	}
	return result, nil
}

func (d *Distributor) queryIngester(ctx context.Context, ing *ring.IngesterDesc, req *client.QueryRequest) (alertmodel.Matrix, error) {
	client, err := d.getClientFor(ing)
	if err != nil {
		return nil, err
	}

	resp, err := client.Query(ctx, req)
	d.ingesterQueries.WithLabelValues(ing.Addr).Inc()
	if err != nil {
		d.ingesterQueryFailures.WithLabelValues(ing.Addr).Inc()
		return nil, err
	}

	return util.FromAlertQueryResponse(resp), nil
}

// forAllIngesters runs f, in parallel, for all ingesters
func (d *Distributor) forAllIngesters(f func(client.AlertIngesterClient) (interface{}, error)) ([]interface{}, error) {
	resps, errs := make(chan interface{}), make(chan error)
	ingesters := d.ring.GetAll()
	for _, ingester := range ingesters {
		go func(ingester *ring.IngesterDesc) {
			client, err := d.getClientFor(ingester)
			if err != nil {
				errs <- err
				return
			}

			resp, err := f(client)
			if err != nil {
				errs <- err
			} else {
				resps <- resp
			}
		}(ingester)
	}

	var lastErr error
	result, numErrs := []interface{}{}, 0
	for range ingesters {
		select {
		case resp := <-resps:
			result = append(result, resp)
		case lastErr = <-errs:
			numErrs++
		}
	}
	if numErrs > d.cfg.ReplicationFactor/2 {
		return nil, lastErr
	}
	return result, nil
}

// LabelValuesForLabelName returns all of the label values that are associated with a given label name.
func (d *Distributor) LabelValuesForLabelName(ctx context.Context, labelName model.LabelName) (model.LabelValues, error) {
	req := &client.LabelValuesRequest{
		LabelName: string(labelName),
	}
	resps, err := d.forAllIngesters(func(client client.AlertIngesterClient) (interface{}, error) {
		return client.LabelValues(ctx, req)
	})
	if err != nil {
		return nil, err
	}

	valueSet := map[model.LabelValue]struct{}{}
	for _, resp := range resps {
		for _, v := range resp.(*client.LabelValuesResponse).LabelValues {
			valueSet[model.LabelValue(v)] = struct{}{}
		}
	}

	values := make(model.LabelValues, 0, len(valueSet))
	for v := range valueSet {
		values = append(values, v)
	}
	return values, nil
}

// MetricsForLabelMatchers gets the metrics that match said matchers
func (d *Distributor) MetricsForLabelMatchers(ctx context.Context, from, through model.Time, matchers ...*labels.Matcher) ([]metric.Metric, error) {
	req, err := util.ToAlertsForLabelMatchersRequest(from, through, matchers)
	if err != nil {
		return nil, err
	}

	resps, err := d.forAllIngesters(func(client client.AlertIngesterClient) (interface{}, error) {
		return client.MetricsForLabelMatchers(ctx, req)
	})
	if err != nil {
		return nil, err
	}

	metrics := map[model.Fingerprint]model.Metric{}
	for _, resp := range resps {
		ms := util.FromAlertsForLabelMatchersResponse(resp.(*client.MetricsForLabelMatchersResponse))
		for _, m := range ms {
			metrics[m.Fingerprint()] = m
		}
	}

	result := make([]metric.Metric, 0, len(metrics))
	for _, m := range metrics {
		result = append(result, metric.Metric{
			Metric: m,
		})
	}
	return result, nil
}

// UserStats returns statistics about the current user.
func (d *Distributor) UserStats(ctx context.Context) (*UserStats, error) {
	req := &client.UserStatsRequest{}
	resps, err := d.forAllIngesters(func(client client.AlertIngesterClient) (interface{}, error) {
		return client.UserStats(ctx, req)
	})
	if err != nil {
		return nil, err
	}

	totalStats := &UserStats{}
	for _, resp := range resps {
		totalStats.IngestionRate += resp.(*client.UserStatsResponse).IngestionRate
		totalStats.NumSeries += resp.(*client.UserStatsResponse).NumSeries
	}

	totalStats.IngestionRate /= float64(d.cfg.ReplicationFactor)
	totalStats.NumSeries /= uint64(d.cfg.ReplicationFactor)

	return totalStats, nil
}

// Describe implements prometheus.Collector.
func (d *Distributor) Describe(ch chan<- *prometheus.Desc) {
	d.queryDuration.Describe(ch)
	ch <- d.receivedSamples.Desc()
	d.sendDuration.Describe(ch)
	d.ring.Describe(ch)
	ch <- numClientsDesc
	d.ingesterAppends.Describe(ch)
	d.ingesterAppendFailures.Describe(ch)
	d.ingesterQueries.Describe(ch)
	d.ingesterQueryFailures.Describe(ch)
}

// Collect implements prometheus.Collector.
func (d *Distributor) Collect(ch chan<- prometheus.Metric) {
	d.queryDuration.Collect(ch)
	ch <- d.receivedSamples
	d.sendDuration.Collect(ch)
	d.ring.Collect(ch)
	d.ingesterAppends.Collect(ch)
	d.ingesterAppendFailures.Collect(ch)
	d.ingesterQueries.Collect(ch)
	d.ingesterQueryFailures.Collect(ch)
	d.clientsMtx.RLock()
	defer d.clientsMtx.RUnlock()
	ch <- prometheus.MustNewConstMetric(
		numClientsDesc,
		prometheus.GaugeValue,
		float64(len(d.clients)),
	)
}
