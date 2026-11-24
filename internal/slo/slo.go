// Package slo loads SLO definitions from YAML and decides what counts as a good or valid event.
package slo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

var knownClasses = map[string]bool{"1xx": true, "2xx": true, "3xx": true, "4xx": true, "5xx": true}

// Duration is a time.Duration that also understands "30d" in YAML.
type Duration time.Duration

// Std returns the standard library duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// UnmarshalYAML parses values like 30d, 6h, 90m.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ParseDuration is time.ParseDuration plus a "d" (24h) unit. The result must be positive.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("invalid duration: empty")
	}
	var d time.Duration
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid duration %q: must be positive", s)
	}
	return d, nil
}

// GoodSpec says what makes an event good.
type GoodSpec struct {
	StatusClasses []string `yaml:"status_classes"`
	LatencyMSLTE  float64  `yaml:"latency_ms_lte"`
}

// ValidSpec says which events are not counted at all.
type ValidSpec struct {
	ExcludeStatusClasses []string `yaml:"exclude_status_classes"`
}

// SLO is one objective for one service and one SLI type.
type SLO struct {
	Name      string    `yaml:"name"`
	Service   string    `yaml:"service"`
	SLI       string    `yaml:"sli"`
	Objective float64   `yaml:"objective"` // percent, e.g. 99.5
	Window    Duration  `yaml:"window"`
	Good      GoodSpec  `yaml:"good"`
	Valid     ValidSpec `yaml:"valid"`
}

// File is the whole SLO YAML document.
type File struct {
	SLOs []SLO `yaml:"slos"`
}

// ErrorBudgetRatio is the allowed bad fraction, e.g. 0.001 for 99.9%.
func (s *SLO) ErrorBudgetRatio() float64 { return (100 - s.Objective) / 100 }

// StatusClass maps an HTTP status code to "2xx", "5xx", ... or "other".
func StatusClass(code int) string {
	if code < 100 || code > 599 {
		return "other"
	}
	return fmt.Sprintf("%dxx", code/100)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// IsValid reports whether a request is counted at all for this SLO.
func (s *SLO) IsValid(status int) bool {
	return !contains(s.Valid.ExcludeStatusClasses, StatusClass(status))
}

// IsGood reports whether a request is good for this SLO.
func (s *SLO) IsGood(status int, latencyMS float64) bool {
	if !s.IsValid(status) {
		return false
	}
	if !contains(s.Good.StatusClasses, StatusClass(status)) {
		return false
	}
	if s.SLI == event.SLILatency && latencyMS > s.Good.LatencyMSLTE {
		return false
	}
	return true
}

func (s *SLO) applyDefaults() {
	if s.SLI == event.SLILatency && len(s.Good.StatusClasses) == 0 {
		s.Good.StatusClasses = []string{"2xx", "3xx"}
	}
}

// Validate checks one SLO and returns all problems at once.
func (s *SLO) Validate() error {
	var errs []error
	add := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("slo %q: "+format, append([]any{s.Name}, a...)...))
	}
	if s.Name == "" {
		add("name is required")
	}
	if s.Service == "" {
		add("service is required")
	}
	if s.Objective <= 0 || s.Objective >= 100 {
		add("objective must be between 0 and 100 (exclusive), got %v", s.Objective)
	}
	if s.Window <= 0 {
		add("window must be positive")
	}
	switch s.SLI {
	case event.SLIAvailability:
		if len(s.Good.StatusClasses) == 0 {
			add("good.status_classes is required for availability")
		}
	case event.SLILatency:
		if s.Good.LatencyMSLTE <= 0 {
			add("good.latency_ms_lte must be > 0 for latency")
		}
	default:
		add("sli must be %q or %q, got %q", event.SLIAvailability, event.SLILatency, s.SLI)
	}
	for _, c := range append(append([]string{}, s.Good.StatusClasses...), s.Valid.ExcludeStatusClasses...) {
		if !knownClasses[c] {
			add("unknown status class %q", c)
		}
	}
	for _, c := range s.Good.StatusClasses {
		if contains(s.Valid.ExcludeStatusClasses, c) {
			add("status class %q is both good and excluded", c)
		}
	}
	return errors.Join(errs...)
}

// Validate checks every SLO and that names are unique.
func (f *File) Validate() error {
	if len(f.SLOs) == 0 {
		return errors.New("no slos defined")
	}
	var errs []error
	seen := map[string]bool{}
	pair := map[string]string{}
	for i := range f.SLOs {
		if err := f.SLOs[i].Validate(); err != nil {
			errs = append(errs, err)
		}
		if seen[f.SLOs[i].Name] {
			errs = append(errs, fmt.Errorf("duplicate slo name %q", f.SLOs[i].Name))
		}
		seen[f.SLOs[i].Name] = true
		key := f.SLOs[i].Service + "/" + f.SLOs[i].SLI
		if other, ok := pair[key]; ok && other != f.SLOs[i].Name {
			errs = append(errs, fmt.Errorf("service %q already has a %s slo (%q): events carry only service and sli, so use one slo per service and sli", f.SLOs[i].Service, f.SLOs[i].SLI, other))
		}
		pair[key] = f.SLOs[i].Name
	}
	return errors.Join(errs...)
}

// Parse reads SLO YAML strictly: unknown fields are errors.
func Parse(data []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("slo file is empty")
		}
		return nil, fmt.Errorf("parse slo file: %w", err)
	}
	for i := range f.SLOs {
		f.SLOs[i].applyDefaults()
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// Load reads and parses an SLO file from disk.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}
