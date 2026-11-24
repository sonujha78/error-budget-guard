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
}

// Compute calculates the SLI and error budget over the SLO's own window ending at now.
func Compute(s slo.SLO, evs []event.Event, now time.Time) Status {
	w := s.Window.Std()
	c := Sum(evs, s.Service, s.SLI, now.Add(-w), now)
	st := Status{
		SLO: s.Name, Service: s.Service, SLIType: s.SLI,
		Objective: s.Objective, Window: w, Counts: c,
	}
	sli, ok := SLI(c)
	if !ok {
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
	return st
}
