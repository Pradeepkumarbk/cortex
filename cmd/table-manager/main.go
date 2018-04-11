package main

import (
	"flag"
	"log"
	"os"

	"github.com/go-kit/kit/log/level"
	"google.golang.org/grpc"

	"github.com/weaveworks/common/middleware"
	"github.com/weaveworks/common/server"
	"github.com/weaveworks/cortex/pkg/alert"
	alert_storage "github.com/weaveworks/cortex/pkg/alert/storage"
	"github.com/weaveworks/cortex/pkg/util"
)

func main() {
	log.Print("hellow")
	var (
		serverConfig = server.Config{
			MetricsNamespace: "cortex",
			GRPCMiddleware: []grpc.UnaryServerInterceptor{
				middleware.ServerUserHeaderInterceptor,
			},
		}

		// storageConfig      storage.Config
		alertStorageConfig alert_storage.Config
		// schemaConfig       chunk.SchemaConfig
		alertSchemaConfig alert.SchemaConfig
		logLevel          util.LogLevel
	)
	// util.RegisterFlags(&serverConfig, &storageConfig, &alertStorageConfig, &schemaConfig, &alertSchemaConfig, &logLevel)
	util.RegisterFlags(&serverConfig, &alertStorageConfig, &alertSchemaConfig, &logLevel)
	flag.Parse()

	util.InitLogger(logLevel.AllowedLevel)
	log.Print("before schemaconfig")
	// if (schemaConfig.ChunkTables.WriteScale.Enabled ||
	// 	schemaConfig.IndexTables.WriteScale.Enabled ||
	// 	schemaConfig.ChunkTables.InactiveWriteScale.Enabled ||
	// 	schemaConfig.IndexTables.InactiveWriteScale.Enabled) &&
	// 	storageConfig.ApplicationAutoScaling.URL == nil {
	// 	level.Error(util.Logger).Log("msg", "WriteScale is enabled but no ApplicationAutoScaling URL has been provided")
	// 	os.Exit(1)
	// }

	if (alertSchemaConfig.ChunkTables.WriteScale.Enabled ||
		alertSchemaConfig.IndexTables.WriteScale.Enabled ||
		alertSchemaConfig.ChunkTables.InactiveWriteScale.Enabled ||
		alertSchemaConfig.IndexTables.InactiveWriteScale.Enabled) &&
		alertStorageConfig.ApplicationAutoScaling.URL == nil {
		level.Error(util.Logger).Log("msg", "Alert WriteScale is enabled but no ApplicationAutoScaling URL has been provided")
		os.Exit(1)
	}
	log.Print("before storage.NewTableClient")
	// tableClient, err := storage.NewTableClient(storageConfig)
	// if err != nil {
	// 	level.Error(util.Logger).Log("msg", "error initializing DynamoDB table client", "err", err)
	// 	os.Exit(1)
	// }
	log.Print("before alert_storage.NewTableClient")
	alerttableClient, err := alert_storage.NewTableClient(alertStorageConfig)
	if err != nil {
		level.Error(util.Logger).Log("msg", "alert error initializing DynamoDB table client", "err", err)
		os.Exit(1)
	}
	log.Print("before chunk.NewTableManager")
	// tableManager, err := chunk.NewTableManager(schemaConfig, tableClient)
	// if err != nil {
	// 	level.Error(util.Logger).Log("msg", "error initializing DynamoDB table manager", "err", err)
	// 	os.Exit(1)
	// }
	// tableManager.Start()
	// defer tableManager.Stop()
	log.Print("before alert.NewTableManager")
	alerttableManager, err := alert.NewTableManager(alertSchemaConfig, alerttableClient)
	if err != nil {
		level.Error(util.Logger).Log("msg", "alert error initializing DynamoDB table manager", "err", err)
		os.Exit(1)
	}
	alerttableManager.Start()
	defer alerttableManager.Stop()
	log.Print("before server.New")
	server, err := server.New(serverConfig)
	if err != nil {
		level.Error(util.Logger).Log("msg", "error initializing server", "err", err)
		os.Exit(1)
	}
	defer server.Shutdown()
	log.Print("before server.Run")
	server.Run()
}
