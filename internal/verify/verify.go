// Package verify checks a recorded scenario against what was written down before it was recorded:
// the gate decision, which alerts fire and how fast, the forecast, and the top error source.
package verify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sonujha78/error-budget-guard/internal/alert"
	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/forecast"
	"github.com/sonujha78/error-budget-guard/internal/gate"
	"github.com/sonujha78/error-budget-guard/internal/report"
	"github.com/sonujha78/error-budget-guard/internal/slo"
	"github.com/sonujha78/error-budget-guard/internal/sources"
)

const replayStep = 500 * time.Millisecond

// Config names the configuration files the recorded data is judged with.
type Config struct{ SLO, Alerts, Policy string }

// EpisodeSpec is one alert that must appear exactly once. MaxDelayS 0 turns the delay check off.
type EpisodeSpec struct {
	SLO       string  `yaml:"slo"`
	Rule      string  `yaml:"rule"`
	MaxDelayS float64 `yaml:"max_delay_s"`
}

// ForecastSpec is the true time to exhaustion of one SLO, worked out by hand.
type ForecastSpec struct {
	SLO         string  `yaml:"slo"`
	TrueSeconds float64 `yaml:"true_seconds"`
	Tolerance   float64 `yaml:"tolerance"`
}

// TopSpec names the endpoint that must cause most of the errors.
type TopSpec struct {
	SLO      string `yaml:"slo"`
	Endpoint string `yaml:"endpoint"`
}

// Spec is expected.yaml.
type Spec struct {
	Gate            *int          `yaml:"gate"`
	ForecastDivisor int           `yaml:"forecast_divisor"`
	OnsetSeconds    *float64      `yaml:"onset_seconds"`
	Episodes        []EpisodeSpec `yaml:"episodes"`
	Forecast        *ForecastSpec `yaml:"forecast"`
	TopEndpoint     *TopSpec      `yaml:"top_endpoint"`
	Insufficient    bool          `yaml:"insufficient"`
}

func (s *Spec) validate() error {
	var errs []error
	if s.Gate == nil {
		errs = append(errs, errors.New("gate is required (0 allow, 1 warn, 2 block)"))
	} else if *s.Gate < 0 || *s.Gate > 2 {
		errs = append(errs, errors.New("gate must be 0, 1 or 2"))
	}
	if s.ForecastDivisor < 0 {
		errs = append(errs, errors.New("forecast_divisor must not be negative"))
	}
	seen := map[string]bool{}
	for i, e := range s.Episodes {
		if e.SLO == "" || e.Rule == "" {
			errs = append(errs, fmt.Errorf("episode %d: slo and rule are required", i+1))
			continue
		}
		if k := e.SLO + "/" + e.Rule; seen[k] {
			errs = append(errs, fmt.Errorf("episode %s is listed twice", k))
		} else {
			seen[k] = true
		}
		if e.MaxDelayS < 0 {
			errs = append(errs, fmt.Errorf("episode %d: max_delay_s must not be negative", i+1))
		}
		if e.MaxDelayS > 0 && s.OnsetSeconds == nil {
			errs = append(errs, fmt.Errorf("episode %d: max_delay_s needs onset_seconds", i+1))
		}
	}
	if f := s.Forecast; f != nil && (f.SLO == "" || f.TrueSeconds <= 0 || f.Tolerance <= 0 || f.Tolerance > 1) {
		errs = append(errs, errors.New("forecast needs slo, true_seconds > 0 and tolerance in (0, 1]"))
	}
	if t := s.TopEndpoint; t != nil && (t.SLO == "" || t.Endpoint == "") {
		errs = append(errs, errors.New("top_endpoint needs slo and endpoint"))
	}
	return errors.Join(errs...)
}

// ParseSpec reads expected.yaml strictly: unknown fields are errors.
func ParseSpec(data []byte) (*Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("expected.yaml is empty")
		}
		return nil, fmt.Errorf("parse expected.yaml: %w", err)
	}
	if s.ForecastDivisor == 0 {
		s.ForecastDivisor = forecast.DefaultDivisor
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Check is one line of the result.
type Check struct {
	Name   string
	Pass   bool
	Detail string
}

// Result is everything checked for one scenario directory.
type Result struct {
	Dir    string
	Checks []Check
}

// OK is true when every check passed.
func (r Result) OK() bool {
	for _, c := range r.Checks {
		if !c.Pass {
			return false
		}
	}
	return len(r.Checks) > 0
}

func (s *Spec) names() []string {
	var n []string
	for _, e := range s.Episodes {
		n = append(n, e.SLO)
	}
	if s.Forecast != nil {
		n = append(n, s.Forecast.SLO)
	}
	if s.TopEndpoint != nil {
		n = append(n, s.TopEndpoint.SLO)
	}
	return n
}

// Run verifies one scenario directory: access.log, truth.json and expected.yaml.
// Nothing here reads the wall clock, so the same recording always gives the same result.
func Run(dir string, c Config) (Result, error) {
	res := Result{Dir: dir}
	f, err := slo.Load(c.SLO)
	if err != nil {
		return res, err
	}
	ap, err := alert.Load(c.Alerts)
	if err != nil {
		return res, err
	}
	pol, err := gate.Load(c.Policy)
	if err != nil {
		return res, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "expected.yaml"))
	if err != nil {
		return res, err
	}
	spec, err := ParseSpec(data)
	if err != nil {
		return res, err
	}
	known := map[string]slo.SLO{}
	for _, s := range f.SLOs {
		known[s.Name] = s
	}
	for _, n := range spec.names() {
		if _, ok := known[n]; !ok {
			return res, fmt.Errorf("expected.yaml names unknown slo %q", n)
		}
	}
	var truth struct {
		StartUnix float64 `json:"start_unix"`
	}
	td, err := os.ReadFile(filepath.Join(dir, "truth.json"))
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(td, &truth); err != nil {
		return res, errors.New("truth.json is not valid JSON")
	}
	fh, err := os.Open(filepath.Join(dir, "access.log"))
	if err != nil {
		return res, err
	}
	defer fh.Close()
	col, err := sources.Collect(fh, sources.FormatJSON, "", f.SLOs)
	if err != nil {
		return res, err
	}
	evs := col.Events
	if len(evs) == 0 {
		return res, errors.New("access.log gave no events")
	}
	first, now := evs[0].Timestamp, evs[0].Timestamp
	for _, e := range evs {
		if e.Timestamp.Before(first) {
			first = e.Timestamp
		}
		if e.Timestamp.After(now) {
			now = e.Timestamp
		}
	}
	add := func(name string, pass bool, format string, a ...any) {
		res.Checks = append(res.Checks, Check{Name: name, Pass: pass, Detail: fmt.Sprintf(format, a...)})
	}

	states := make([]gate.SLOState, 0, len(f.SLOs))
	byName := map[string]gate.SLOState{}
	for _, s := range f.SLOs {
		st := budget.ComputeWith(s, evs, now, pol.MinHistoryCoverage)
		gs := gate.SLOState{Status: st, Alerts: alert.Evaluate(ap, s, evs, now), Forecast: forecast.Compute(s, evs, now, st, spec.ForecastDivisor)}
		states = append(states, gs)
		byName[s.Name] = gs
	}
	d := gate.Decide(pol, states)
	add("gate decision", d.ExitCode() == *spec.Gate, "got %s (exit %d), want exit %d", strings.ToUpper(d.Level), d.ExitCode(), *spec.Gate)

	eps, err := alert.AllEpisodes(ap, f.SLOs, evs, first, now, replayStep)
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	for _, e := range spec.Episodes {
		want[e.SLO+"/"+e.Rule] = true
	}
	count := map[string]int{}
	startOf := map[string]time.Time{}
	unexpected := 0
	for _, e := range eps {
		k := e.SLO + "/" + e.Rule
		count[k]++
		if count[k] == 1 {
			startOf[k] = e.Start
		}
		if !want[k] && count[k] == 1 {
			unexpected++
			add("unexpected alert "+k, false, "%s fired from %s but was not expected", e.Severity, e.Start.UTC().Format(time.RFC3339Nano))
		}
	}
	if unexpected == 0 {
		add("no unexpected alerts", true, "%d episodes, all expected", len(eps))
	}
	var onset time.Time
	if spec.OnsetSeconds != nil {
		onset = time.UnixMilli(int64(math.Round(truth.StartUnix * 1000))).Add(time.Duration(*spec.OnsetSeconds * float64(time.Second)))
	}
	for _, e := range spec.Episodes {
		k := e.SLO + "/" + e.Rule
		add("alert "+k+" appears once (no flapping)", count[k] == 1, "%d episodes", count[k])
		if count[k] == 0 || e.MaxDelayS <= 0 {
			continue
		}
		delay := startOf[k].Sub(onset)
		max := time.Duration(e.MaxDelayS * float64(time.Second))
		add("alert "+k+" starts within "+max.String()+" of the fault", delay >= -500*time.Millisecond && delay <= max, "delay %.2fs", delay.Seconds())
	}

	if fc := spec.Forecast; fc != nil {
		f := byName[fc.SLO].Forecast
		if !f.Known || f.Exhausted || f.NoExhaustion {
			add("forecast "+fc.SLO, false, "no usable forecast: %s", f.Explain())
		} else {
			got := f.TimeToExhaustion.Seconds()
			e := math.Abs(got-fc.TrueSeconds) / fc.TrueSeconds
			add("forecast "+fc.SLO+" within tolerance", e <= fc.Tolerance, "forecast %.2fs, true %.2fs, error %.1f%% (allowed %.0f%%)", got, fc.TrueSeconds, e*100, fc.Tolerance*100)
		}
	}
	if t := spec.TopEndpoint; t != nil {
		s := known[t.SLO]
		e := report.TopErrors(evs, s.Service, s.SLI, now.Add(-s.Window.Std()), now, 1)
		got := ""
		if len(e.ByEndpoint) > 0 {
			got = e.ByEndpoint[0].Key
		}
		add("top error endpoint "+t.SLO, got == t.Endpoint, "got %q, want %q", got, t.Endpoint)
	}
	if spec.Insufficient {
		ok, why := len(eps) == 0, ""
		for _, st := range states {
			if !st.Status.Insufficient {
				ok, why = false, st.Status.SLO+" has a budget number"
			}
			if st.Forecast.Known {
				ok, why = false, st.Status.SLO+" has a forecast"
			}
			for _, a := range st.Alerts {
				if !a.Insufficient {
					ok, why = false, st.Status.SLO+"/"+a.Rule+" was judged"
				}
			}
		}
		add("insufficient data is reported everywhere", ok, "%s", map[bool]string{true: "budget, forecast and every alert rule say insufficient", false: "not insufficient: " + why}[ok])
	}
	return res, nil
}
