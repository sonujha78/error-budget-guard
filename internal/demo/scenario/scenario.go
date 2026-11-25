// Package scenario describes a load test as data: how many requests, to which paths, and which
// faults to inject when. The plan is deterministic (no random numbers), so the ground truth of a
// scenario is known exactly before it is run.
package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Fault kinds.
const (
	KindError = "error" // the request fails with HTTP 500
	KindSlow  = "slow"  // the request is delayed by delay_ms
)

// Limits.
const (
	MaxRPS     = 1000
	MaxDelayMS = 5000
)

// Dur is a non-negative duration in YAML, like 0s, 150s or 3m.
type Dur time.Duration

// Std returns the standard library duration.
func (d Dur) Std() time.Duration { return time.Duration(d) }

// UnmarshalYAML parses the duration.
func (d *Dur) UnmarshalYAML(v *yaml.Node) error {
	var s string
	if err := v.Decode(&s); err != nil {
		return err
	}
	x, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	if x < 0 {
		return fmt.Errorf("invalid duration %q: must not be negative", s)
	}
	*d = Dur(x)
	return nil
}

// PathWeight is one endpoint and its share of the traffic.
type PathWeight struct {
	Path   string `yaml:"path"`
	Weight int    `yaml:"weight"`
}

// Fault is injected into the requests that match it inside [From, To).
// Every is "every Nth matching request" (1 = all of them).
type Fault struct {
	Kind    string `yaml:"kind"`
	Path    string `yaml:"path"` // empty = every path
	From    Dur    `yaml:"from"`
	To      Dur    `yaml:"to"`
	Every   int    `yaml:"every"`
	DelayMS int    `yaml:"delay_ms"`
}

// Scenario is one load test.
type Scenario struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	RPS         float64      `yaml:"rps"`
	Duration    Dur          `yaml:"duration"`
	Paths       []PathWeight `yaml:"paths"`
	Faults      []Fault      `yaml:"faults"`
}

// Validate returns every problem at once.
func (s *Scenario) Validate() error {
	var errs []error
	add := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("scenario %q: "+format, append([]any{s.Name}, a...)...))
	}
	if s.Name == "" {
		add("name is required")
	}
	if s.RPS <= 0 || s.RPS > MaxRPS || math.IsNaN(s.RPS) {
		add("rps must be above 0 and at most %d", MaxRPS)
	}
	if s.Duration <= 0 {
		add("duration must be positive")
	}
	if len(s.Paths) == 0 {
		add("at least one path is required")
	}
	seen := map[string]bool{}
	for _, p := range s.Paths {
		if !strings.HasPrefix(p.Path, "/") {
			add("path %q must start with /", p.Path)
		}
		if p.Weight <= 0 {
			add("weight must be positive for path %q", p.Path)
		}
		if seen[p.Path] {
			add("path %q is listed twice", p.Path)
		}
		seen[p.Path] = true
	}
	for i, f := range s.Faults {
		n := i + 1
		switch f.Kind {
		case KindError:
		case KindSlow:
			if f.DelayMS <= 0 || f.DelayMS > MaxDelayMS {
				add("fault %d: slow needs delay_ms between 1 and %d", n, MaxDelayMS)
			}
		default:
			add("fault %d: kind must be %q or %q, got %q", n, KindError, KindSlow, f.Kind)
		}
		if f.To <= f.From {
			add("fault %d: to must be after from", n)
		}
		if f.To > s.Duration {
			add("fault %d: to must be within the duration", n)
		}
		if f.Every < 0 {
			add("fault %d: every must not be negative", n)
		}
		if f.Path != "" && !seen[f.Path] {
			add("fault %d: path %q is not one of the scenario paths", n, f.Path)
		}
	}
	return errors.Join(errs...)
}

// Parse reads a scenario strictly: unknown fields are errors.
func Parse(data []byte) (*Scenario, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("scenario file is empty")
		}
		return nil, fmt.Errorf("parse scenario: %w", err)
	}
	for i := range s.Faults {
		if s.Faults[i].Every == 0 {
			s.Faults[i].Every = 1
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Load reads and parses a scenario file.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Req is one planned request.
type Req struct {
	At      time.Duration // offset from the start of the run
	Path    string
	Fail    bool
	DelayMS int
}

// Interval is the time between two requests.
func (s *Scenario) Interval() time.Duration { return time.Duration(float64(time.Second) / s.RPS) }

// FaultSummary is the ground truth of one fault.
type FaultSummary struct {
	Kind        string  `json:"kind"`
	Path        string  `json:"path,omitempty"`
	FromSeconds float64 `json:"from_seconds"`
	ToSeconds   float64 `json:"to_seconds"`
	Every       int     `json:"every"`
	DelayMS     int     `json:"delay_ms,omitempty"`
	Matched     int     `json:"matched"`
	Applied     int     `json:"applied"`
}

// Summary is the ground truth of a whole plan.
type Summary struct {
	Total  int            `json:"total"`
	Errors int            `json:"errors"`
	Slow   int            `json:"slow"`
	ByPath map[string]int `json:"by_path"`
	Faults []FaultSummary `json:"faults"`
}

func (s *Scenario) build() ([]Req, []FaultSummary) {
	iv := s.Interval()
	n := int(s.Duration.Std() / iv)
	var cycle []string
	for _, p := range s.Paths {
		for i := 0; i < p.Weight; i++ {
			cycle = append(cycle, p.Path)
		}
	}
	matched := make([]int, len(s.Faults))
	applied := make([]int, len(s.Faults))
	plan := make([]Req, 0, n)
	for i := 0; i < n; i++ {
		r := Req{At: time.Duration(i) * iv, Path: cycle[i%len(cycle)]}
		for fi, f := range s.Faults {
			if r.At < f.From.Std() || r.At >= f.To.Std() {
				continue
			}
			if f.Path != "" && f.Path != r.Path {
				continue
			}
			matched[fi]++
			every := f.Every
			if every < 1 {
				every = 1
			}
			if matched[fi]%every != 0 {
				continue
			}
			applied[fi]++
			switch f.Kind {
			case KindError:
				r.Fail = true
			case KindSlow:
				if f.DelayMS > r.DelayMS {
					r.DelayMS = f.DelayMS
				}
			}
		}
		plan = append(plan, r)
	}
	faults := make([]FaultSummary, len(s.Faults))
	for fi, f := range s.Faults {
		faults[fi] = FaultSummary{
			Kind: f.Kind, Path: f.Path, FromSeconds: f.From.Std().Seconds(), ToSeconds: f.To.Std().Seconds(),
			Every: f.Every, DelayMS: f.DelayMS, Matched: matched[fi], Applied: applied[fi],
		}
	}
	return plan, faults
}

// Plan returns every request of the run, in order. The same scenario always gives the same plan.
func (s *Scenario) Plan() []Req {
	plan, _ := s.build()
	return plan
}

// Summarize counts what the plan will inject. This is the ground truth.
func (s *Scenario) Summarize() Summary {
	plan, faults := s.build()
	sum := Summary{Total: len(plan), ByPath: map[string]int{}, Faults: faults}
	for _, r := range plan {
		sum.ByPath[r.Path]++
		if r.Fail {
			sum.Errors++
		} else if r.DelayMS > 0 {
			sum.Slow++
		}
	}
	return sum
}
