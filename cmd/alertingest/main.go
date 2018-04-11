package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip" // get gzip compressor registered

	"github.com/weaveworks/common/middleware"
	"github.com/weaveworks/common/server"
	"github.com/weaveworks/cortex/pkg/alert"
	"github.com/weaveworks/cortex/pkg/alert/storage"
	"github.com/weaveworks/cortex/pkg/alertingest"
	"github.com/weaveworks/cortex/pkg/alertingest/client"
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
		alertStoreConfig alert.StoreConfig
		schemaConfig     alert.SchemaConfig
		storageConfig    storage.Config
		ingesterConfig   alertingest.Config
		logLevel         util.LogLevel
	)
	// Ingester needs to know our gRPC listen port.
	ingesterConfig.ListenPort = &serverConfig.GRPCListenPort
	util.RegisterFlags(&serverConfig, &alertStoreConfig, &storageConfig,
		&schemaConfig, &ingesterConfig, &logLevel)
	flag.Parse()
	schemaConfig.MaxChunkAge = ingesterConfig.MaxChunkAge

	util.InitLogger(logLevel.AllowedLevel)
	log.Printf("before server.New")
	server, err := server.New(serverConfig)
	if err != nil {
		level.Error(util.Logger).Log("msg", "error initializing server", "err", err)
		os.Exit(1)
	}
	defer server.Shutdown()
	log.Printf("after server.New")
	storageClient, err := storage.NewStorageClient(storageConfig, schemaConfig)
	if err != nil {
		level.Error(util.Logger).Log("msg", "error initializing storage client", "err", err)
		os.Exit(1)
	}
	log.Printf("after storage.NewStorageClient")
	alertStore, err := alert.NewStore(alertStoreConfig, schemaConfig, storageClient)
	if err != nil {
		level.Error(util.Logger).Log("err", err)
		os.Exit(1)
	}
	defer alertStore.Stop()
	log.Printf("after alert.NewStore")
	ingester, err := alertingest.New(ingesterConfig, alertStore)
	if err != nil {
		level.Error(util.Logger).Log("err", err)
		os.Exit(1)
	}
	prometheus.MustRegister(ingester)
	defer ingester.Shutdown()
	log.Printf("after alertingest.New")
	client.RegisterAlertIngesterServer(server.GRPC, ingester)
	log.Printf("after client.RegisterAlertIngesterServer")
	server.HTTP.Path("/ready").Handler(http.HandlerFunc(ingester.ReadinessHandler))
	server.HTTP.Path("/flush").Handler(http.HandlerFunc(ingester.FlushHandler))
	server.Run()
}
