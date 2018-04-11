package storage

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/go-kit/kit/log/level"
	"github.com/weaveworks/cortex/pkg/alert"
	"github.com/weaveworks/cortex/pkg/alert/gcp"
	"github.com/weaveworks/cortex/pkg/util"
)

// Config chooses which storage client to use.
type Config struct {
	StorageClient string
	alert.AWSStorageConfig
	GCPStorageConfig gcp.Config
}

// RegisterFlags adds the flags required to configure this flag set.
func (cfg *Config) RegisterFlags(f *flag.FlagSet) {
	flag.StringVar(&cfg.StorageClient, "chunk.storage-client", "aws", "Which storage client to use (aws, gcp, inmemory).")
	cfg.AWSStorageConfig.RegisterFlags(f)
	cfg.GCPStorageConfig.RegisterFlags(f)
}

// NewStorageClient makes a storage client based on the configuration.
func NewStorageClient(cfg Config, schemaCfg alert.SchemaConfig) (alert.StorageClient, error) {
	switch cfg.StorageClient {
	case "inmemory":
		return alert.NewMockStorage(), nil
	case "aws":
		path := strings.TrimPrefix(cfg.DynamoDB.URL.Path, "/")
		if len(path) > 0 {
			level.Warn(util.Logger).Log("msg", "ignoring DynamoDB URL path", "path", path)
		}
		return alert.NewAWSStorageClient(cfg.AWSStorageConfig, schemaCfg)
	case "gcp":
		return gcp.NewStorageClient(context.Background(), cfg.GCPStorageConfig, schemaCfg)
	default:
		return nil, fmt.Errorf("Unrecognized storage client %v, choose one of: aws, gcp, inmemory", cfg.StorageClient)
	}
}

// NewTableClient makes a new table client based on the configuration.
func NewTableClient(cfg Config) (alert.TableClient, error) {
	switch cfg.StorageClient {
	case "inmemory":
		return alert.NewMockStorage(), nil
	case "aws":
		path := strings.TrimPrefix(cfg.DynamoDB.URL.Path, "/")
		if len(path) > 0 {
			level.Warn(util.Logger).Log("msg", "ignoring DynamoDB URL path", "path", path)
		}
		return alert.NewDynamoDBTableClient(cfg.AWSStorageConfig.DynamoDBConfig)
	case "gcp":
		return gcp.NewTableClient(context.Background(), cfg.GCPStorageConfig)
	default:
		return nil, fmt.Errorf("Unrecognized storage client %v, choose one of: aws, gcp, inmemory", cfg.StorageClient)
	}
}
