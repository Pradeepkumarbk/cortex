package main

import (
	"flag"
	"net/http"
	"os"

	"google.golang.org/grpc"

	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/weaveworks/common/middleware"
	"github.com/weaveworks/common/server"
	"github.com/weaveworks/common/tracing"
	"github.com/weaveworks/cortex/pkg/alert"
	"github.com/weaveworks/cortex/pkg/alert/storage"
	"github.com/weaveworks/cortex/pkg/alertdist"
	"github.com/weaveworks/cortex/pkg/alertquery"
	ring "github.com/weaveworks/cortex/pkg/alertring"
	"github.com/weaveworks/cortex/pkg/util"
)

func main() {
	var (
		serverConfig = server.Config{
			MetricsNamespace: "cortex",
			GRPCMiddleware: []grpc.UnaryServerInterceptor{
				middleware.ServerUserHeaderInterceptor,
			},
		}
		ringConfig        ring.Config
		distributorConfig alertdist.Config
		chunkStoreConfig  alert.StoreConfig
		schemaConfig      alert.SchemaConfig
		storageConfig     storage.Config
		logLevel          util.LogLevel
	)
	util.RegisterFlags(&serverConfig, &ringConfig, &distributorConfig,
		&chunkStoreConfig, &schemaConfig, &storageConfig, &logLevel)
	flag.Parse()

	// Setting the environment variable JAEGER_AGENT_HOST enables tracing
	jaegerAgentHost := os.Getenv("JAEGER_AGENT_HOST")
	trace := tracing.New(jaegerAgentHost, "alertquery")
	defer trace.Close()

	util.InitLogger(logLevel.AllowedLevel)

	r, err := ring.New(ringConfig)
	if err != nil {
		level.Error(util.Logger).Log("msg", "error initializing ring", "err", err)
		os.Exit(1)
	}
	defer r.Stop()

	dist, err := alertdist.New(distributorConfig, r)
	if err != nil {
		level.Error(util.Logger).Log("msg", "error initializing distributor", "err", err)
		os.Exit(1)
	}
	defer dist.Stop()
	prometheus.MustRegister(dist)

	server, err := server.New(serverConfig)
	if err != nil {
		level.Error(util.Logger).Log("msg", "error initializing server", "err", err)
		os.Exit(1)
	}
	defer server.Shutdown()
	server.HTTP.Handle("/alertring", r)

	storageClient, err := storage.NewStorageClient(storageConfig, schemaConfig)
	if err != nil {
		level.Error(util.Logger).Log("msg", "error initializing storage client", "err", err)
		os.Exit(1)
	}

	chunkStore, err := alert.NewStore(chunkStoreConfig, schemaConfig, storageClient)
	if err != nil {
		level.Error(util.Logger).Log("err", err)
		os.Exit(1)
	}
	defer chunkStore.Stop()

	sampleQueryable := alertquery.NewQueryable(dist, chunkStore, false)
	// metadataQueryable := alertquery.NewQueryable(dist, chunkStore, true)

	// server.HTTP.Handle("/api/prom/alerts/read", middleware.Merge(
	// 	middleware.Func(func(handler http.Handler) http.Handler {
	// 		return nethttp.Middleware(opentracing.GlobalTracer(), handler, operationNameFunc)
	// 	}),
	// 	middleware.AuthenticateUser,
	// ).Wrap(http.HandlerFunc(sampleQueryable.RemoteReadHandler)))

	subrouter := server.HTTP.PathPrefix("/api/prom/alerts").Subrouter()
	subrouter.Path("/read").Handler(middleware.AuthenticateUser.Wrap(http.HandlerFunc(sampleQueryable.RemoteReadHandler)))

	server.Run()
}
