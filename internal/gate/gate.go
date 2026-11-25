// Package gate turns budget, alerts and forecast into allow, warn or block, with reasons.
package gate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/sonujha78/error-budget-guard/internal/alert"
	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/forecast"
)

// Levels of a decision.
const (
	Allow = "allow"
	Warn  = "warn"
	Block = "block"
)

// MinOverrideReason is the shortest accepted override reason, in characters.
const MinOverrideReason = 10

const tol = 1e-9

func rank(l string) int {
	switch l {
	case Block:
		return 2
	case Warn:
		return 1
	}
	return 0
}

// Policy is the error budget policy.
type Policy struct {
	BlockBelowRemaining   float64 `yaml:"block_below_remaining"`
	WarnBelowRemaining    float64 `yaml:"warn_below_remaining"`
	OnInsufficient        string  `yaml:"on_insufficient"`
	MinHistoryCoverage    float64 `yaml:"min_history_coverage"`
	WarnIfExhaustedWithin float64 `yaml:"warn_if_exhausted_within"`
}

// Validate returns every problem at once. Required fields have no default, so a missing field
// can never turn into "allow".
func (p *Policy) Validate() error {
	var errs []error
	if p.BlockBelowRemaining <= 0 || p.BlockBelowRemaining > 1 {
		errs = append(errs, errors.New("block_below_remaining must be above 0 and at most 1"))
	}
	if p.WarnBelowRemaining < p.BlockBelowRemaining || p.WarnBelowRemaining > 1 {
		errs = append(errs, errors.New("warn_below_remaining must be between block_below_remaining and 1"))
	}
	if p.OnInsufficient != Warn && p.OnInsufficient != Block {
		errs = append(errs, fmt.Errorf("on_insufficient must be %q or %q (never allow), got %q", Warn, Block, p.OnInsufficient))
	}
	if p.MinHistoryCoverage <= 0 || p.MinHistoryCoverage > 1 {
		errs = append(errs, errors.New("min_history_coverage must be above 0 and at most 1"))
	}
	if p.WarnIfExhaustedWithin < 0 || p.WarnIfExhaustedWithin > 1 {
		errs = append(errs, errors.New("warn_if_exhausted_within must be between 0 and 1"))
	}
	return errors.Join(errs...)
}

// Parse reads the policy strictly: unknown fields are errors.
func Parse(data []byte) (*Policy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var p Policy
	if err := dec.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("policy file is empty")
		}
		return nil, fmt.Errorf("parse policy: %w", err)
	}
	if p.MinHistoryCoverage == 0 {
		p.MinHistoryCoverage = forecast.MinHistoryCoverage
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

// SLOState is everything the gate looks at for one SLO.
type SLOState struct {
	Status   budget.Status
	Alerts   []alert.Alert
	Forecast forecast.Forecast
}

// Reason is one line of the explanation.
type Reason struct {
	SLO   string
	Level string
	Text  string
}

func (r Reason) String() string {
	if r.SLO == "" {
		return fmt.Sprintf("[%s] %s", r.Level, r.Text)
	}
	return fmt.Sprintf("[%s] %s: %s", r.Level, r.SLO, r.Text)
}

// Decision is the gate result. Level is what the policy says. Effective is what the exit code
// follows, which differs only after an override.
type Decision struct {
	Level          string
	Effective      string
	Reasons        []Reason
	Overridden     bool
	OverrideReason string
	OverrideBy     string
}

// Decide applies the policy. The worst level of any reason wins.
func Decide(p *Policy, states []SLOState) Decision {
	d := Decision{Level: Allow}
	add := func(name, level, text string) {
		d.Reasons = append(d.Reasons, Reason{SLO: name, Level: level, Text: text})
		if rank(level) > rank(d.Level) {
			d.Level = level
		}
	}
	if len(states) == 0 {
		add("", p.OnInsufficient, "no SLOs were evaluated, so nothing can be judged")
	}
	for _, st := range states {
		name := st.Status.SLO
		for _, a := range st.Alerts {
			switch {
			case a.Insufficient:
				add(name, Allow, fmt.Sprintf("alert rule %s: %s", a.Rule, a.Explain()))
			case a.Firing && a.Severity == alert.SeverityPage:
				add(name, Block, fmt.Sprintf("page alert %s is firing: %s", a.Rule, a.Explain()))
			case a.Firing:
				add(name, Warn, fmt.Sprintf("%s alert %s is firing: %s", a.Severity, a.Rule, a.Explain()))
			}
		}

		s := st.Status
		if s.Insufficient || !s.HasData {
			add(name, p.OnInsufficient, "insufficient data, the budget cannot be judged: "+s.Reason)
		} else {
			frac := 0.0
			if s.BudgetTotal > 0 {
				frac = s.BudgetRemaining / s.BudgetTotal
			}
			text := fmt.Sprintf("%.1f%% of the error budget remains (%.1f of %.1f events used)", frac*100, s.BudgetUsed, s.BudgetTotal)
			switch {
			case s.Overspent || frac < p.BlockBelowRemaining-tol:
				add(name, Block, fmt.Sprintf("%s; below the block threshold of %.0f%%", text, p.BlockBelowRemaining*100))
			case frac < p.WarnBelowRemaining-tol:
				add(name, Warn, fmt.Sprintf("%s; below the warn threshold of %.0f%%", text, p.WarnBelowRemaining*100))
			default:
				add(name, Allow, text)
			}
		}

		fc := st.Forecast
		if fc.Known && !fc.Exhausted && !fc.NoExhaustion && p.WarnIfExhaustedWithin > 0 && s.Window > 0 &&
			float64(fc.TimeToExhaustion) < p.WarnIfExhaustedWithin*float64(s.Window) {
			add(name, Warn, fmt.Sprintf("forecast: %s; that is less than %.0f%% of the %s window", fc.Explain(), p.WarnIfExhaustedWithin*100, s.Window))
		} else {
			add(name, Allow, "forecast: "+fc.Explain())
		}
	}
	d.Effective = d.Level
	return d
}

// ExitCode is 0 for allow, 1 for warn, 2 for block. Anything unknown fails closed (2).
func (d Decision) ExitCode() int {
	switch d.Effective {
	case Allow:
		return 0
	case Warn:
		return 1
	}
	return 2
}

// WithOverride lets the release go ahead despite the decision. A reason and a name are required
// and are validated even when there is nothing to override. The original level and reasons stay.
func (d Decision) WithOverride(reason, by string) (Decision, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	by = strings.Join(strings.Fields(by), " ")
	if utf8.RuneCountInString(reason) < MinOverrideReason {
		return d, fmt.Errorf("override reason must be at least %d characters: say why the gate is being bypassed", MinOverrideReason)
	}
	if by == "" {
		return d, errors.New("an override needs a name (--override-by)")
	}
	if d.Level == Allow {
		return d, nil
	}
	d.Overridden, d.OverrideReason, d.OverrideBy = true, reason, by
	d.Effective = Allow
	return d, nil
}

// Summary is the headline, including the override when there is one.
func (d Decision) Summary() string {
	s := strings.ToUpper(d.Level)
	if d.Overridden {
		s += fmt.Sprintf(", OVERRIDDEN by %s: %q -> %s", d.OverrideBy, d.OverrideReason, strings.ToUpper(d.Effective))
	}
	return s
}
