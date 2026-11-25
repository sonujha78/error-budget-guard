// Package budget turns events into SLI, error budget and burn rate numbers.
//
// Definitions (request based SLO):
//
//	SLI              = good / valid
//	budget ratio     = 1 - objective            (e.g. 0.001 for 99.9%)
//	budget total     = valid x budget ratio      (in events)
//	budget used      = bad events = valid - good
//	burn rate        = (bad / valid) / budget ratio over some window
//	budget consumed  = burn x duration / SLO window
package budget

import (
	"fmt"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

// Tolerance is the absolute tolerance used when comparing ratios in tests and worked examples.
const Tolerance = 1e-9

// Counts is the sum of good and valid events over some period.
type Counts struct {
	Good  int64
	Valid int64
}

// Bad is the number of valid events that were not good.
func (c Counts) Bad() int64 { return c.Valid - c.Good }

// Sum adds up events of one service and SLI type in the window (after, upTo].
// The start is exclusive and the end inclusive, so adjacent windows never double count.
func Sum(evs []event.Event, service, sli string, after, upTo time.Time) Counts {
	var c Counts
	for _, e := range evs {
		if e.Service != service || e.SLI != sli {
			continue
		}
		if !e.Timestamp.After(after) || e.Timestamp.After(upTo) {
			continue
		}
		c.Good += e.Good
		c.Valid += e.Valid
	}
	return c
}

// SLI returns good/valid. ok is false when there are no valid events: no data is not 100%.
func SLI(c Counts) (float64, bool) {
	if c.Valid <= 0 {
		return 0, false
	}
	return float64(c.Good) / float64(c.Valid), true
}

// BurnRate is the error rate divided by the allowed error rate.
// 1.0 means the budget would be used up exactly at the end of the SLO window.
func BurnRate(c Counts, budgetRatio float64) (float64, bool) {
	if c.Valid <= 0 || budgetRatio <= 0 {
		return 0, false
	}
	return (float64(c.Bad()) / float64(c.Valid)) / budgetRatio, true
}

// ConsumedByBurn is the fraction of the whole budget used by burning at `burn` for `d`.
func ConsumedByBurn(burn float64, d, window time.Duration) float64 {
	if window <= 0 {
		return 0
	}
	return burn * float64(d) / float64(window)
}

// BurnOver computes the burn rate of an SLO over the last `w` ending at `now`.
func BurnOver(s slo.SLO, evs []event.Event, now time.Time, w time.Duration) (float64, bool) {
	c := Sum(evs, s.Service, s.SLI, now.Add(-w), now)
	return BurnRate(c, s.ErrorBudgetRatio())
}

// Status is the budget picture of one SLO at one moment.
type Status struct {
	SLO       string
	Service   string
	SLIType   string
	Objective float64
	Window    time.Duration
	Counts    Counts

	// HasData is false when there were no valid events. All numbers below are then zero.
	HasData bool
	SLI     float64

	BudgetTotal float64 // events
	BudgetUsed  float64 // events
	// BudgetRemaining is clamped at 0. When the budget is overspent, see Overspent and UsedFraction.
	BudgetRemaining float64
	UsedFraction    float64 // 0.5 = 50% used, 2.0 = 200% used
	Overspent       bool

	// HistoryCoverage is how much of the SLO window the history covers: (now - oldest event) / window, at most 1.
	HistoryCoverage float64
	// Insufficient means the numbers above must not be trusted: no data, a budget smaller than one
	// event, or too little history. Reason says which.
	Insufficient bool
	Reason       string
}

// History is how long ago the oldest event of a service and SLI happened. Events after now are ignored.
func History(evs []event.Event, service, sli string, now time.Time) (time.Duration, bool) {
	var first time.Time
	found := false
	for _, e := range evs {
		if e.Service != service || e.SLI != sli || e.Timestamp.After(now) {
			continue
		}
		if !found || e.Timestamp.Before(first) {
			first, found = e.Timestamp, true
		}
	}
	if !found {
		return 0, false
	}
	return now.Sub(first), true
}

// CoverageOf is history / window, limited to 0..1.
func CoverageOf(history, window time.Duration) float64 {
	if window <= 0 || history <= 0 {
		return 0
	}
	r := float64(history) / float64(window)
	if r > 1 {
		return 1
	}
	return r
}

// Compute calculates the SLI and error budget over the SLO's own window ending at now.
// It asks for no minimum history; see ComputeWith.
func Compute(s slo.SLO, evs []event.Event, now time.Time) Status {
	return ComputeWith(s, evs, now, 0)
}

// ComputeWith is Compute that also marks the status Insufficient when the history covers less
// than minCoverage of the SLO window (0 turns that check off).
func ComputeWith(s slo.SLO, evs []event.Event, now time.Time, minCoverage float64) Status {
	w := s.Window.Std()
	c := Sum(evs, s.Service, s.SLI, now.Add(-w), now)
	st := Status{
		SLO: s.Name, Service: s.Service, SLIType: s.SLI,
		Objective: s.Objective, Window: w, Counts: c,
	}
	if hist, ok := History(evs, s.Service, s.SLI, now); ok {
		st.HistoryCoverage = CoverageOf(hist, w)
	}
	sli, ok := SLI(c)
	if !ok {
		st.Insufficient = true
		st.Reason = "no valid events in the window"
		return st
	}
	st.HasData = true
	st.SLI = sli
	st.BudgetTotal = float64(c.Valid) * s.ErrorBudgetRatio()
	st.BudgetUsed = float64(c.Bad())
	if st.BudgetTotal > 0 {
		st.UsedFraction = st.BudgetUsed / st.BudgetTotal
	}
	st.Overspent = st.BudgetUsed > st.BudgetTotal
	if rem := st.BudgetTotal - st.BudgetUsed; rem > 0 {
		st.BudgetRemaining = rem
	}
	switch {
	case st.BudgetTotal < 1:
		st.Insufficient = true
		st.Reason = fmt.Sprintf("only %d valid events at %g%%: the whole budget is less than one event, so one failure would overspend it", c.Valid, s.Objective)
	case minCoverage > 0 && st.HistoryCoverage+Tolerance < minCoverage:
		st.Insufficient = true
		st.Reason = fmt.Sprintf("history covers %.1f%% of the %s window, need at least %.0f%%", st.HistoryCoverage*100, w, minCoverage*100)
	}
	return st
}
