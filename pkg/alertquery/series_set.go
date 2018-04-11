// Some of the code in this file was adapted from Prometheus (https://github.com/prometheus/prometheus).
// The original license header is included below:
//
// Copyright 2017 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package alertquery

import (
	"sort"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/weaveworks/cortex/pkg/alertingest/client"
	"github.com/weaveworks/cortex/pkg/alertmodel"
	"github.com/weaveworks/cortex/pkg/alertstorage"
	"github.com/weaveworks/cortex/pkg/prom1/storage/metric"
)

// errSeriesSet implements alertstorage.SeriesSet, just returning an error.
type errSeriesSet struct {
	err error
}

func (errSeriesSet) Next() bool {
	return false
}

func (errSeriesSet) At() alertstorage.Series {
	return nil
}

func (e errSeriesSet) Err() error {
	return e.err
}

// concreteSeriesSet implements alertstorage.SeriesSet.
type concreteSeriesSet struct {
	cur    int
	series []alertstorage.Series
}

func (c *concreteSeriesSet) Next() bool {
	c.cur++
	return c.cur-1 < len(c.series)
}

func (c *concreteSeriesSet) At() alertstorage.Series {
	return c.series[c.cur-1]
}

func (c *concreteSeriesSet) Err() error {
	return nil
}

// concreteSeries implements alertstorage.Series.
type concreteSeries struct {
	labels  labels.Labels
	samples []client.MessageTracker
}

func (c *concreteSeries) Labels() labels.Labels {
	return c.labels
}

func (c *concreteSeries) Iterator() alertstorage.AlertIterator {
	return newConcreteSeriesIterator(c)
}

// concreteSeriesIterator implements alertstorage.AlertIterator.
type concreteSeriesIterator struct {
	cur    int
	series *concreteSeries
}

func newConcreteSeriesIterator(series *concreteSeries) alertstorage.AlertIterator {
	return &concreteSeriesIterator{
		cur:    -1,
		series: series,
	}
}

func (c *concreteSeriesIterator) Seek(t int64) bool {
	c.cur = sort.Search(len(c.series.samples), func(n int) bool {
		return model.Time(c.series.samples[n].Timestamp) >= model.Time(t)
	})
	return c.cur < len(c.series.samples)
}

func (c *concreteSeriesIterator) At() (t int64, v string) {
	s := c.series.samples[c.cur]
	return int64(s.Timestamp), (s.Value)
}

func (c *concreteSeriesIterator) Next() bool {
	c.cur++
	return c.cur < len(c.series.samples)
}

func (c *concreteSeriesIterator) Err() error {
	return nil
}

func metricsToSeriesSet(ms []metric.Metric) alertstorage.SeriesSet {
	series := make([]alertstorage.Series, 0, len(ms))
	for _, m := range ms {
		series = append(series, &concreteSeries{
			labels:  metricToLabels(m.Metric),
			samples: nil,
		})
	}
	return &concreteSeriesSet{
		series: series,
	}
}

func matrixToSeriesSet(m alertmodel.Matrix) alertstorage.SeriesSet {
	series := make([]alertstorage.Series, 0, len(m))
	for _, ss := range m {
		series = append(series, &concreteSeries{
			labels:  metricToLabels(ss.Metric),
			samples: ss.Values,
		})
	}
	return &concreteSeriesSet{
		series: series,
	}
}

func metricToLabels(m model.Metric) labels.Labels {
	ls := make(labels.Labels, 0, len(m))
	for k, v := range m {
		ls = append(ls, labels.Label{
			Name:  string(k),
			Value: string(v),
		})
	}
	// PromQL expects all labels to be sorted! In general, anyone constructing
	// a labels.Labels list is responsible for sorting it during construction time.
	sort.Sort(ls)
	return ls
}
