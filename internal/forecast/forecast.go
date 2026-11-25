// Package forecast says how long the error budget lasts at the current burn rate.
//
//	time left = remaining fraction x SLO window / burn
//
// It is exact when the burn is steady and the window is full. It says "unknown" instead of
// guessing when the data is too thin.
package forecast

import (
	"fmt"
	"math"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

const (
	// DefaultDivisor: the burn is measured over the last (SLO window / 12).
	DefaultDivisor = 12
	// MinHistoryCoverage: the history must cover this share of the forecast window.
	MinHistoryCoverage = 0.8
	// MaxHorizon: anything longer is reported as "not exhausted".
	MaxHorizon = 100 * 365 * 24 * time.Hour
)

// Forecast is the outlook of one SLO.
type Forecast struct {
	Known             bool
	Reason            string // why it is not known
	Window            time.Duration
	Burn              float64
	RemainingFraction float64
	Exhausted         bool // the budget is already used up
	NoExhaustion      bool // burn is 0 (or so small it lasts beyond the horizon)
	TimeToExhaustion  time.Duration
}

// TimeToExhaustion is remaining x window / burn. ok is false when burn is not positive.
func TimeToExhaustion(remainingFraction, burn float64, window time.Duration) (time.Duration, bool) {
	if burn <= 0 || window <= 0 {
		return 0, false
	}
	if remainingFraction <= 0 {
		return 0, true
	}
	v := remainingFraction * float64(window) / burn
	if v >= float64(MaxHorizon) {
		return MaxHorizon, true
	}
	return time.Duration(math.Round(v)), true
}

// Compute forecasts one SLO. st must be the budget status at the same moment.
func Compute(s slo.SLO, evs []event.Event, now time.Time, st budget.Status, divisor int) Forecast {
	if divisor <= 0 {
		divisor = DefaultDivisor
	}
	w := s.Window.Std() / time.Duration(divisor)
	if w <= 0 {
		w = s.Window.Std()
	}
	f := Forecast{Window: w}
	if st.Insufficient || !st.HasData {
		f.Reason = "cannot forecast, insufficient data: " + st.Reason
		return f
	}
	if st.BudgetTotal > 0 {
		f.RemainingFraction = st.BudgetRemaining / st.BudgetTotal
	}
	if st.Overspent || st.BudgetRemaining <= 0 {
		f.Known, f.Exhausted, f.RemainingFraction = true, true, 0
		return f
	}
	hist, ok := budget.History(evs, s.Service, s.SLI, now)
	cov := 0.0
	if ok {
		cov = budget.CoverageOf(hist, w)
	}
	if cov+budget.Tolerance < MinHistoryCoverage {
		f.Reason = fmt.Sprintf("cannot forecast: history covers %.1f%% of the %s forecast window, need at least %.0f%%", cov*100, w, MinHistoryCoverage*100)
		return f
	}
	c := budget.Sum(evs, s.Service, s.SLI, now.Add(-w), now)
	burn, ok := budget.BurnRate(c, s.ErrorBudgetRatio())
	if !ok {
		f.Reason = fmt.Sprintf("cannot forecast: no valid events in the last %s", w)
		return f
	}
	f.Known, f.Burn = true, burn
	tte, ok := TimeToExhaustion(f.RemainingFraction, burn, s.Window.Std())
	if !ok || tte >= MaxHorizon {
		f.NoExhaustion = true
		return f
	}
	f.TimeToExhaustion = tte
	return f
}

// Explain is one sentence a person can check.
func (f Forecast) Explain() string {
	switch {
	case !f.Known:
		return f.Reason
	case f.Exhausted:
		return "the error budget is already used up"
	case f.NoExhaustion:
		return fmt.Sprintf("budget not used up at the current rate (burn %.2fx over the last %s)", f.Burn, f.Window)
	}
	return fmt.Sprintf("at burn %.2fx over the last %s, the remaining %.1f%% of the budget lasts %s (%.1f days)",
		f.Burn, f.Window, f.RemainingFraction*100, f.TimeToExhaustion.Round(time.Millisecond), f.TimeToExhaustion.Hours()/24)
}
