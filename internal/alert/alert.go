// Package alert evaluates multi-window, multi-burn-rate alerts (Google SRE workbook style).
//
// A rule fires only when BOTH its long and its short window burn at or above the threshold.
// The long window shows that real budget is going. The short window shows it is still going
// now, so an alert stops soon after the problem stops.
package alert

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

// Severities.
const (
	SeverityPage   = "page"
	SeverityTicket = "ticket"
)

const maxSteps = 1_000_000

// Defaults for the "is there enough data" checks. They come from design, not from tuning on a dataset.
const (
	DefaultMinCoverage       = 0.8 // history must cover 80% of a rule's long window
	DefaultMinBadAtThreshold = 5   // the long window needs enough traffic for >= 5 bad events at the threshold burn
)

// Rule is one burn-rate rule. Windows are written for the policy's reference window.
type Rule struct {
	Name     string       `yaml:"name"`
	Severity string       `yaml:"severity"`
	Burn     float64      `yaml:"burn"`
	Long     slo.Duration `yaml:"long"`
	Short    slo.Duration `yaml:"short"`
	// MinValid overrides the minimum number of valid events in the long window (0 = computed).
	MinValid int64 `yaml:"min_valid"`
}

// Policy is the whole alert policy file.
type Policy struct {
	ReferenceWindow slo.Duration `yaml:"reference_window"`
	Rules           []Rule       `yaml:"rules"`
	// MinCoverage is the share of a rule's long window the history must cover (default 0.8).
	MinCoverage float64 `yaml:"min_coverage"`
	// MinBadAtThreshold sizes the minimum traffic of a long window (default 5).
	MinBadAtThreshold float64 `yaml:"min_bad_at_threshold"`
}

// Validate returns every problem of the policy at once.
func (p *Policy) Validate() error {
	var errs []error
	if p.ReferenceWindow <= 0 {
		errs = append(errs, errors.New("reference_window must be positive"))
	}
	if len(p.Rules) == 0 {
		errs = append(errs, errors.New("no rules defined"))
	}
	if p.MinCoverage <= 0 || p.MinCoverage > 1 {
		errs = append(errs, errors.New("min_coverage must be above 0 and at most 1"))
	}
	if p.MinBadAtThreshold <= 0 {
		errs = append(errs, errors.New("min_bad_at_threshold must be positive"))
	}
	seen := map[string]bool{}
	for i, r := range p.Rules {
		add := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("rule %q (#%d): "+format, append([]any{r.Name, i + 1}, a...)...))
		}
		if r.Name == "" {
			add("name is required")
		} else if seen[r.Name] {
			add("duplicate name")
		}
		seen[r.Name] = true
		if r.Severity != SeverityPage && r.Severity != SeverityTicket {
			add("severity must be %q or %q, got %q", SeverityPage, SeverityTicket, r.Severity)
		}
		if r.Burn <= 0 || math.IsNaN(r.Burn) || math.IsInf(r.Burn, 0) {
			add("burn must be > 0")
		}
		if r.Long <= 0 {
			add("long window must be positive")
		}
		if r.Short <= 0 {
			add("short window must be positive")
		}
		if r.Long > 0 && r.Short > 0 && r.Short >= r.Long {
			add("short window must be shorter than the long window")
		}
		if r.Long > p.ReferenceWindow {
			add("long window must not exceed the reference window")
		}
		if r.MinValid < 0 {
			add("min_valid must not be negative")
		}
	}
	return errors.Join(errs...)
}

// Parse reads policy YAML strictly: unknown fields are errors.
func Parse(data []byte) (*Policy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var p Policy
	if err := dec.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("alert policy file is empty")
		}
		return nil, fmt.Errorf("parse alert policy: %w", err)
	}
	if p.MinCoverage == 0 {
		p.MinCoverage = DefaultMinCoverage
	}
	if p.MinBadAtThreshold == 0 {
		p.MinBadAtThreshold = DefaultMinBadAtThreshold
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Load reads and parses a policy file.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Scale is the factor that shrinks the reference window to an SLO's own window.
func (p *Policy) Scale(sloWindow time.Duration) float64 {
	return float64(sloWindow) / float64(p.ReferenceWindow.Std())
}

// ScaleDuration scales a window and rounds it to whole milliseconds (never below 1 ms).
func ScaleDuration(d time.Duration, f float64) time.Duration {
	ms := math.Round(float64(d) * f / float64(time.Millisecond))
	if ms < 1 {
		ms = 1
	}
	return time.Duration(ms) * time.Millisecond
}

// Alert is the state of one rule for one SLO at one moment.
type Alert struct {
	SLO       string
	Service   string
	Rule      string
	Severity  string
	Threshold float64

	Long, Short           time.Duration // the scaled windows actually used
	LongBurn, ShortBurn   float64
	LongKnown, ShortKnown bool // false when the window had no valid events
	LongValid, ShortValid int64

	// Insufficient means there is too little data to judge this rule. It never fires then.
	Insufficient  bool
	Reason        string  // why it is insufficient
	Coverage      float64 // how much of the long window the history covers (0..1)
	RequiredValid int64   // minimum valid events the long window needs

	Firing bool
	// BudgetConsumed is the fraction of the whole SLO budget burned during the long window
	// at the long window's burn rate (burn x long / SLO window). Zero when insufficient.
	BudgetConsumed float64
}

func burnText(b float64, known bool) string {
	if !known {
		return "n/a (no data)"
	}
	return fmt.Sprintf("%.1fx", b)
}

// Explain says which windows fired and how much budget was used, or why it cannot judge.
func (a Alert) Explain() string {
	if a.Insufficient {
		return "insufficient data: " + a.Reason
	}
	if a.Firing {
		return fmt.Sprintf("burn %.1fx over %s and %.1fx over %s, both >= %.1fx; %.2f%% of the error budget used in the last %s",
			a.LongBurn, a.Long, a.ShortBurn, a.Short, a.Threshold, a.BudgetConsumed*100, a.Long)
	}
	return fmt.Sprintf("burn %s over %s and %s over %s (fires when both >= %.1fx)",
		burnText(a.LongBurn, a.LongKnown), a.Long, burnText(a.ShortBurn, a.ShortKnown), a.Short, a.Threshold)
}

// RequiredValid is the minimum number of valid events the long window of a rule needs: enough
// traffic that, at the threshold burn, at least MinBadAtThreshold events would be bad.
func (p *Policy) RequiredValid(r Rule, budgetRatio float64) int64 {
	if r.MinValid > 0 {
		return r.MinValid
	}
	if budgetRatio <= 0 || r.Burn <= 0 {
		return 0
	}
	return int64(math.Ceil(p.MinBadAtThreshold/(budgetRatio*r.Burn) - 1e-9))
}

// Evaluate checks every rule of the policy for one SLO at time now, in policy order.
// A rule with too little data (no data, too little history, too little traffic) is marked
// Insufficient and does not fire.
func Evaluate(p *Policy, s slo.SLO, evs []event.Event, now time.Time) []Alert {
	f := p.Scale(s.Window.Std())
	ratio := s.ErrorBudgetRatio()
	hist, hasHist := budget.History(evs, s.Service, s.SLI, now)
	out := make([]Alert, 0, len(p.Rules))
	for _, r := range p.Rules {
		long, short := ScaleDuration(r.Long.Std(), f), ScaleDuration(r.Short.Std(), f)
		lc := budget.Sum(evs, s.Service, s.SLI, now.Add(-long), now)
		sc := budget.Sum(evs, s.Service, s.SLI, now.Add(-short), now)
		lb, lok := budget.BurnRate(lc, ratio)
		sb, sok := budget.BurnRate(sc, ratio)
		cov := 0.0
		if hasHist {
			cov = budget.CoverageOf(hist, long)
		}
		req := p.RequiredValid(r, ratio)
		a := Alert{
			SLO: s.Name, Service: s.Service, Rule: r.Name, Severity: r.Severity, Threshold: r.Burn,
			Long: long, Short: short,
			LongBurn: lb, ShortBurn: sb, LongKnown: lok, ShortKnown: sok,
			LongValid: lc.Valid, ShortValid: sc.Valid,
			Coverage: cov, RequiredValid: req,
		}
		switch {
		case !lok:
			a.Insufficient, a.Reason = true, fmt.Sprintf("no data in the last %s", long)
		case cov+budget.Tolerance < p.MinCoverage:
			a.Insufficient, a.Reason = true, fmt.Sprintf("history covers %.1f%% of the %s window, need at least %.0f%%", cov*100, long, p.MinCoverage*100)
		case lc.Valid < req:
			a.Insufficient, a.Reason = true, fmt.Sprintf("only %d valid events in the last %s, need at least %d", lc.Valid, long, req)
		}
		if !a.Insufficient {
			a.Firing = sok && lb >= r.Burn-budget.Tolerance && sb >= r.Burn-budget.Tolerance
			a.BudgetConsumed = budget.ConsumedByBurn(lb, long, s.Window.Std())
		}
		out = append(out, a)
	}
	return out
}

// Episode is one continuous alert, possibly made of several firing moments.
type Episode struct {
	SLO, Service, Rule, Severity string
	Start                        time.Time // first evaluation at which the rule fired
	End                          time.Time // last evaluation at which the rule fired
	Open                         bool      // still firing at the last evaluation
	PeakBurn                     float64   // highest long window burn seen
}

type point struct {
	at     time.Time
	firing bool
	burn   float64
}

type span struct {
	start, end time.Time
	open       bool
	peak       float64
}

// buildSpans merges firing points into episodes. A pause no longer than `hold` does not end an
// episode, so an alert that blinks on and off is still one alert (no flapping).
func buildSpans(pts []point, hold time.Duration) []span {
	var out []span
	var cur *span
	var last time.Time
	for _, p := range pts {
		if p.firing {
			if cur == nil {
				out = append(out, span{start: p.at, peak: p.burn})
				cur = &out[len(out)-1]
			}
			cur.end, last = p.at, p.at
			if p.burn > cur.peak {
				cur.peak = p.burn
			}
			continue
		}
		if cur != nil && p.at.Sub(last) > hold {
			cur = nil
		}
	}
	if cur != nil && len(pts) > 0 && last.Equal(pts[len(pts)-1].at) {
		cur.open = true
	}
	return out
}

func sortEpisodes(eps []Episode) {
	sort.SliceStable(eps, func(i, j int) bool {
		a, b := eps[i], eps[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.SLO != b.SLO {
			return a.SLO < b.SLO
		}
		return a.Rule < b.Rule
	})
}

// Episodes replays the policy for one SLO from `from` to `to` (both included) every `step`
// and returns the alert episodes. The last evaluation is always exactly `to`.
// The hold time of a rule is its scaled short window.
func Episodes(p *Policy, s slo.SLO, evs []event.Event, from, to time.Time, step time.Duration) ([]Episode, error) {
	if step <= 0 {
		return nil, errors.New("step must be positive")
	}
	if to.Before(from) {
		return nil, errors.New("end of the replay must not be before its start")
	}
	if n := to.Sub(from) / step; n > maxSteps {
		return nil, fmt.Errorf("too many evaluation steps (%d): use a larger step", int64(n))
	}
	own := make([]event.Event, 0, len(evs))
	for _, e := range evs {
		if e.Service == s.Service && e.SLI == s.SLI {
			own = append(own, e)
		}
	}
	var times []time.Time
	for t := from; t.Before(to); t = t.Add(step) {
		times = append(times, t)
	}
	times = append(times, to)

	pts := make([][]point, len(p.Rules))
	for _, t := range times {
		for i, a := range Evaluate(p, s, own, t) {
			pts[i] = append(pts[i], point{at: t, firing: a.Firing, burn: a.LongBurn})
		}
	}
	f := p.Scale(s.Window.Std())
	var out []Episode
	for i, r := range p.Rules {
		hold := ScaleDuration(r.Short.Std(), f)
		for _, sp := range buildSpans(pts[i], hold) {
			out = append(out, Episode{
				SLO: s.Name, Service: s.Service, Rule: r.Name, Severity: r.Severity,
				Start: sp.start, End: sp.end, Open: sp.open, PeakBurn: sp.peak,
			})
		}
	}
	sortEpisodes(out)
	return out, nil
}

// AllEpisodes runs Episodes for every SLO and returns one sorted list.
func AllEpisodes(p *Policy, slos []slo.SLO, evs []event.Event, from, to time.Time, step time.Duration) ([]Episode, error) {
	var out []Episode
	for _, s := range slos {
		eps, err := Episodes(p, s, evs, from, to, step)
		if err != nil {
			return nil, err
		}
		out = append(out, eps...)
	}
	sortEpisodes(out)
	return out, nil
}
