package alertstorage

import (
	"context"

	"github.com/prometheus/prometheus/pkg/labels"
)

// Storage ingests and manages samples, along with various indexes. All methods
// are goroutine-safe. Storage implements alertstorage.AlertAppender.
type Storage interface {
	// StartTime returns the oldest timestamp stored in the storage.
	StartTime() (int64, error)

	// Querier returns a new Querier on the storage.
	Querier(ctx context.Context, mint, maxt int64) (Querier, error)

	// Appender returns a new appender against the storage.
	Appender() (Appender, error)

	// Close closes the storage and all its underlying resources.
	Close() error
}

// Querier provides reading access to time series data.
type Querier interface {
	// Select returns a set of series that matches the given label matchers.
	Select(...*labels.Matcher) SeriesSet

	// LabelValues returns all potential values for a label name.
	LabelValues(name string) ([]string, error)

	// Close releases the resources of the Querier.
	Close() error
}

// Appender provides batched appends against a storage.
type Appender interface {
	Add(l labels.Labels, t int64, v float64) (uint64, error)

	AddFast(l labels.Labels, ref uint64, t int64, v float64) error

	// Commit submits the collected samples and purges the batch.
	Commit() error

	Rollback() error
}

// SeriesSet contains a set of series.
type SeriesSet interface {
	Next() bool
	At() Series
	Err() error
}

// Series represents a single time series.
type Series interface {
	// Labels returns the complete set of labels identifying the series.
	Labels() labels.Labels

	// Iterator returns a new iterator of the data of the series.
	Iterator() AlertIterator
}

// AlertIterator iterates over the data of a time series.
type AlertIterator interface {
	// Seek advances the iterator forward to the value at or after
	// the given timestamp.
	Seek(t int64) bool
	// At returns the current timestamp/value pair.
	At() (t int64, v string)
	// Next advances the iterator by one.
	Next() bool
	// Err returns the current error.
	Err() error
}

// dedupedSeriesSet takes two series sets and returns them deduplicated.
// The input sets must be sorted and identical if two series exist in both, i.e.
// if their label sets are equal, the datapoints must be equal as well.
type dedupedSeriesSet struct {
	a, b SeriesSet

	cur          Series
	adone, bdone bool
}

// DeduplicateSeriesSet merges two SeriesSet and removes duplicates.
// If two series exist in both sets, their datapoints must be equal.
func DeduplicateSeriesSet(a, b SeriesSet) SeriesSet {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}

	s := &dedupedSeriesSet{a: a, b: b}
	s.adone = !s.a.Next()
	s.bdone = !s.b.Next()

	return s
}

func (s *dedupedSeriesSet) At() Series {
	return s.cur
}

func (s *dedupedSeriesSet) Err() error {
	if s.a.Err() != nil {
		return s.a.Err()
	}
	return s.b.Err()
}

func (s *dedupedSeriesSet) compare() int {
	if s.adone {
		return 1
	}
	if s.bdone {
		return -1
	}
	return labels.Compare(s.a.At().Labels(), s.b.At().Labels())
}

func (s *dedupedSeriesSet) Next() bool {
	if s.adone && s.bdone || s.Err() != nil {
		return false
	}

	d := s.compare()

	// Both sets contain the current series. Chain them into a single one.
	if d > 0 {
		s.cur = s.b.At()
		s.bdone = !s.b.Next()
	} else if d < 0 {
		s.cur = s.a.At()
		s.adone = !s.a.Next()
	} else {
		s.cur = s.a.At()
		s.adone = !s.a.Next()
		s.bdone = !s.b.Next()
	}
	return true
}
