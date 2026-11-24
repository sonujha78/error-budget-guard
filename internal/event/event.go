// Package event defines the one common record every source is converted into.
package event

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// SLI types.
const (
	SLIAvailability = "availability"
	SLILatency      = "latency"
)

// Event is one aggregated bucket of good and valid request counts.
// A new source only has to produce Events; nothing else changes.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Service   string    `json:"service"`
	SLI       string    `json:"sli"`
	Good      int64     `json:"good"`
	Valid     int64     `json:"valid"`
	Source    string    `json:"source"`
	Label     string    `json:"label"`
}

// Validate checks that the event is internally consistent.
func (e Event) Validate() error {
	var errs []error
	if e.Timestamp.IsZero() {
		errs = append(errs, errors.New("timestamp is required"))
	}
	if e.Service == "" {
		errs = append(errs, errors.New("service is required"))
	}
	if e.SLI != SLIAvailability && e.SLI != SLILatency {
		errs = append(errs, fmt.Errorf("sli must be %q or %q, got %q", SLIAvailability, SLILatency, e.SLI))
	}
	if e.Good < 0 || e.Valid < 0 {
		errs = append(errs, errors.New("good and valid must not be negative"))
	}
	if e.Good > e.Valid {
		errs = append(errs, fmt.Errorf("good (%d) must not exceed valid (%d)", e.Good, e.Valid))
	}
	if e.Source == "" {
		errs = append(errs, errors.New("source is required"))
	}
	return errors.Join(errs...)
}

// Sort orders events by a total order so the same input always gives the same output.
func Sort(evs []Event) {
	sort.SliceStable(evs, func(i, j int) bool { return less(evs[i], evs[j]) })
}

func less(a, b Event) bool {
	if !a.Timestamp.Equal(b.Timestamp) {
		return a.Timestamp.Before(b.Timestamp)
	}
	if a.Service != b.Service {
		return a.Service < b.Service
	}
	if a.SLI != b.SLI {
		return a.SLI < b.SLI
	}
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	if a.Label != b.Label {
		return a.Label < b.Label
	}
	if a.Good != b.Good {
		return a.Good < b.Good
	}
	return a.Valid < b.Valid
}
