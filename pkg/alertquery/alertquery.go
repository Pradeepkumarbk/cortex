package alertquery

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/weaveworks/cortex/pkg/alertingest/client"
	"github.com/weaveworks/cortex/pkg/alertmodel"
	"github.com/weaveworks/cortex/pkg/alertstorage"
	"github.com/weaveworks/cortex/pkg/prom1/storage/metric"

	"github.com/weaveworks/cortex/pkg/util"
)

// ChunkStore is the interface we need to get chunks
type ChunkStore interface {
	Get(ctx context.Context, from, through model.Time, matchers ...*labels.Matcher) (alertmodel.Matrix, error)
}

// NewEngine creates a new promql.Engine for cortex.
// func NewEngine(distributor Querier, chunkStore ChunkStore) *promql.Engine {
// 	queryable := NewQueryable(distributor, chunkStore, false)
// 	return promql.NewEngine(queryable, nil)
// }

// NewQueryable creates a new Queryable for cortex.
func NewQueryable(distributor Querier, chunkStore ChunkStore, mo bool) MergeQueryable {
	return MergeQueryable{
		queriers: []Querier{
			distributor,
			&chunkQuerier{
				store: chunkStore,
			},
		},
		metadataOnly: mo,
	}
}

// A Querier allows querying an underlying storage for time series samples or metadata.
type Querier interface {
	Query(ctx context.Context, from, to model.Time, matchers ...*labels.Matcher) (alertmodel.Matrix, error)
	MetricsForLabelMatchers(ctx context.Context, from, through model.Time, matchers ...*labels.Matcher) ([]metric.Metric, error)
	LabelValuesForLabelName(context.Context, model.LabelName) (model.LabelValues, error)
}

// A chunkQuerier is a Querier that fetches samples from a ChunkStore.
type chunkQuerier struct {
	store ChunkStore
}

// Query implements Querier and transforms a list of chunks into sample
// matrices.
func (q *chunkQuerier) Query(ctx context.Context, from, to model.Time, matchers ...*labels.Matcher) (alertmodel.Matrix, error) {
	// Get iterators for all matching series from ChunkStore.
	// log.Print("inside chunkQuerier Query function")
	matrix, err := q.store.Get(ctx, from, to, matchers...)
	if err != nil {
		return nil, promql.ErrStorage(err)
	}

	return matrix, nil
}

// LabelValuesForLabelName returns all of the label values that are associated with a given label name.
func (q *chunkQuerier) LabelValuesForLabelName(ctx context.Context, ln model.LabelName) (model.LabelValues, error) {
	// TODO: Support querying historical label values at some point?
	return nil, nil
}

// MetricsForLabelMatchers is a noop for chunk querier.
func (q *chunkQuerier) MetricsForLabelMatchers(ctx context.Context, from, through model.Time, matcherSets ...*labels.Matcher) ([]metric.Metric, error) {
	return nil, nil
}

func mergeMatrices(matrices chan alertmodel.Matrix, errors chan error, n int) (alertmodel.Matrix, error) {
	// Group samples from all matrices by fingerprint.
	fpToSS := map[model.Fingerprint]*alertmodel.AlertStream{}
	var lastErr error
	for i := 0; i < n; i++ {
		select {
		case err := <-errors:
			lastErr = err

		case matrix := <-matrices:
			for _, ss := range matrix {
				fp := ss.Metric.Fingerprint()
				if fpSS, ok := fpToSS[fp]; !ok {
					fpToSS[fp] = ss
				} else {
					fpSS.Values = util.MergeAlertSets(fpSS.Values, ss.Values)
				}
			}
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}

	matrix := make(alertmodel.Matrix, 0, len(fpToSS))
	for _, ss := range fpToSS {
		matrix = append(matrix, ss)
	}
	return matrix, nil
}

// A MergeQueryable is a storage.Queryable that produces a storage.Querier which merges
// results from multiple underlying Queriers.
type MergeQueryable struct {
	queriers     []Querier
	metadataOnly bool
}

// Querier implements alertstorage.Queryable.
func (q MergeQueryable) Querier(ctx context.Context, mint, maxt int64) (alertstorage.Querier, error) {
	return mergeQuerier{
		ctx:          ctx,
		queriers:     q.queriers,
		mint:         mint,
		maxt:         maxt,
		metadataOnly: q.metadataOnly,
	}, nil
}

// RemoteReadHandler handles Prometheus remote read requests.
func (q MergeQueryable) RemoteReadHandler(w http.ResponseWriter, r *http.Request) {
	log.Print("Coming inside remotereadhandler")
	// compressionType := util.CompressionTypeFor(r.Header.Get("X-Prometheus-Remote-Read-Version"))

	// ctx := r.Context()
	// var req client.ReadRequest
	// logger := util.WithContext(r.Context(), util.Logger)
	// if _, err := util.ParseProtoRequest(ctx, r, &req, compressionType); err != nil {
	// 	level.Error(logger).Log("err", err.Error())
	// 	http.Error(w, err.Error(), http.StatusBadRequest)
	// 	return
	// }

	// Fetch samples for all queries in parallel.
	// resp := client.ReadResponse{
	// 	Results: make([]*client.QueryResponse, len(req.Queries)),
	// }
	// errors := make(chan error)
	// for i, qr := range req.Queries {
	// 	go func(i int, qr *client.QueryRequest) {
	// 		from, to, matchers, err := util.FromAlertQueryRequest(qr)
	// 		if err != nil {
	// 			errors <- err
	// 			return
	// 		}
	// 		log.Printf("from = %v, to = %v", int64(from), int64(to))
	// 		querier, err := q.Querier(ctx, int64(from), int64(to))
	// 		if err != nil {
	// 			errors <- err
	// 			return
	// 		}

	// 		matrix, err := querier.(mergeQuerier).selectSamplesMatrix(matchers...)
	// 		if err != nil {
	// 			errors <- err
	// 			return
	// 		}

	// resp.Results[i] = util.ToAlertQueryResponse(matrix)
	// 		errors <- nil
	// 	}(i, qr)
	// }

	// var lastErr error
	// for range req.Queries {
	// 	err := <-errors
	// 	if err != nil {
	// 		lastErr = err
	// 	}
	// }
	// if lastErr != nil {
	// 	http.Error(w, lastErr.Error(), http.StatusBadRequest)
	// 	return
	// }

	// if err := util.SerializeProtoResponse(w, &resp, compressionType); err != nil {
	// 	level.Error(logger).Log("msg", "error sending remote read response", "err", err)
	// }

	//MOTEESH: FROM HERE
	// userID, _, err := user.ExtractOrgIDFromHTTPRequest(r)
	// if err != nil {
	// 	http.Error(w, err.Error(), http.StatusUnauthorized)
	// 	return
	// }

	logger := util.WithContext(r.Context(), util.Logger)
	ctx := r.Context()
	// cfg, err := a.db.GetConfig(userID)
	// from := int64(0)
	to := int64(model.Now())
	toH := r.Header.Get("X-Read-Till")
	if len(toH) != 0 {
		i, err := strconv.ParseInt(toH, 10, 64)
		if err != nil {
			log.Printf("error parsing to time Header value : %v", err)
			// panic(err)
		} else {
			log.Printf("read toH value as : %v", i)
			to = i
		}
		// from = int64(fromH)
	}

	from := to - 3600*1000 // last one hour
	fromH := r.Header.Get("X-Read-From")
	if len(fromH) != 0 {
		i, err := strconv.ParseInt(fromH, 10, 64)
		if err != nil {
			log.Printf("error parsing from time Header value : %v", err)
			// panic(err)
		} else {
			log.Printf("read fromH value as : %v", i)
			from = i
		}
		// from = int64(fromH)
	}

	log.Printf("from = %v, to = %v", int64(from), int64(to))
	querier, err := q.Querier(ctx, int64(from), int64(to))
	if err != nil {
		// errors <- err
		log.Print("error 1")
		level.Error(logger).Log("msg", "error getting config", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Print("after querier")
	matrix, err := querier.(mergeQuerier).selectSamplesMatrix()
	if err != nil {
		// errors <- err
		log.Print("error 2")
		level.Error(logger).Log("msg", "error getting config", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Print("after merge querier")
	resp := client.ReadResponse{
		// Results: make([]*client.QueryResponse, len(req.Queries)),
		Results: make([]*client.QueryResponse, 1), // this should be the number of alerts
	}

	resp.Results[0] = util.ToAlertQueryResponse(matrix)
	log.Printf("result = %v", len(resp.Results[0].Messages))
	if len(resp.Results[0].Messages) == 0 {
		http.Error(w, "No configuration", http.StatusNotFound)
	} else {
		alerts := []template.Alert{}
		var buffer bytes.Buffer
		for _, msg := range resp.Results[0].Messages {
			var buf bytes.Buffer
			var alert template.Alert
			buf.WriteString(msg.Value)
			buf2 := []byte(msg.Value)
			br := bytes.NewReader(buf2)
			if err := json.NewDecoder(br).Decode(&alert); err != nil {
				log.Printf("err in decode : %v ", err)
				// return false, err
			}
			alerts = append(alerts, alert)
			log.Printf("decoded Status : %v, GeneratorURL : %v ", alert.Status, alert.GeneratorURL)
			buffer.WriteString(msg.Value)
			// w.Write([]byte(msg.Value))
		}
		// w.Write([]byte(buffer.String()))

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(alerts); err != nil {
			level.Error(logger).Log("msg", "error encoding config", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

	}

	// w.Header().Set("Content-Type", "application/json")
	// if err := json.NewEncoder(w).Encode(cfg); err != nil {
	// 	// XXX: Untested
	// 	level.Error(logger).Log("msg", "error encoding config", "err", err)
	// 	http.Error(w, err.Error(), http.StatusInternalServerError)
	// 	return
	// }
	// if err := util.SerializeAlertProtoResponse(w, &resp); err != nil {
	// 	level.Error(logger).Log("msg", "error sending remote read response", "err", err)
	// }
}

type mergeQuerier struct {
	ctx      context.Context
	queriers []Querier
	mint     int64
	maxt     int64
	// Whether this querier should only load series metadata in Select().
	// Necessary for remote storage implementations of the storage.Querier
	// interface because both metadata and bulk data loading happens via
	// the Select() method.
	metadataOnly bool
}

func (mq mergeQuerier) Select(matchers ...*labels.Matcher) alertstorage.SeriesSet {
	if mq.metadataOnly {
		return mq.selectMetadata(matchers...)
	}
	return mq.selectSamples(matchers...)
}

func (mq mergeQuerier) selectMetadata(matchers ...*labels.Matcher) alertstorage.SeriesSet {
	// NB that we don't do this in parallel, as in practice we only have two queriers,
	// one of which is the chunk store, which doesn't implement this yet.
	var set alertstorage.SeriesSet
	for _, q := range mq.queriers {
		ms, err := q.MetricsForLabelMatchers(mq.ctx, model.Time(mq.mint), model.Time(mq.maxt), matchers...)
		if err != nil {
			return errSeriesSet{err: err}
		}
		ss := metricsToSeriesSet(ms)
		set = alertstorage.DeduplicateSeriesSet(set, ss)
	}

	return set
}

func (mq mergeQuerier) selectSamples(matchers ...*labels.Matcher) alertstorage.SeriesSet {
	matrix, err := mq.selectSamplesMatrix(matchers...)
	if err != nil {
		return errSeriesSet{
			err: err,
		}
	}
	return matrixToSeriesSet(matrix)
}

func (mq mergeQuerier) selectSamplesMatrix(matchers ...*labels.Matcher) (alertmodel.Matrix, error) {
	incomingMatrices := make(chan alertmodel.Matrix)
	incomingErrors := make(chan error)
	log.Printf("inside selectSamplesMatrix no.of queriers = %v", len(mq.queriers))
	for _, q := range mq.queriers {
		go func(q Querier) {
			matrix, err := q.Query(mq.ctx, model.Time(mq.mint), model.Time(mq.maxt), matchers...)
			if err != nil {
				incomingErrors <- err
			} else {
				incomingMatrices <- matrix
			}
		}(q)
	}

	mergedMatrix, err := mergeMatrices(incomingMatrices, incomingErrors, len(mq.queriers))
	if err != nil {
		level.Error(util.WithContext(mq.ctx, util.Logger)).Log("msg", "error in mergeQuerier.selectSamples", "err", err)
		return nil, err
	}
	return mergedMatrix, nil
}

func (mq mergeQuerier) LabelValues(name string) ([]string, error) {
	valueSet := map[string]struct{}{}
	for _, q := range mq.queriers {
		vals, err := q.LabelValuesForLabelName(mq.ctx, model.LabelName(name))
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			valueSet[string(v)] = struct{}{}
		}
	}

	values := make([]string, 0, len(valueSet))
	for v := range valueSet {
		values = append(values, v)
	}
	return values, nil
}

func (mq mergeQuerier) Close() error {
	return nil
}
