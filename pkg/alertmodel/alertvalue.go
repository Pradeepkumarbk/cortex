package alertmodel

import (
	"github.com/prometheus/common/model"
	"github.com/weaveworks/cortex/pkg/alertingest/client"
)

// AlertStream is a stream of Values belonging to an attached COWMetric.
type AlertStream struct {
	Metric model.Metric            `json:"metric"`
	Values []client.MessageTracker `json:"values"`
}

// Matrix is a list of time series.
type Matrix []*AlertStream
