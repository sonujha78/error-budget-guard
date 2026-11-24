package slo

import (
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

const validYAML = `
slos:
  - name: checkout-availability
    service: checkout
    sli: availability
    objective: 99.5
    window: 30d
    good:
      status_classes: [2xx, 3xx]
    valid:
      exclude_status_classes: [4xx]
  - name: checkout-latency
    service: checkout
    sli: latency
    objective: 99
    window: 30d
    good:
      latency_ms_lte: 300
    valid:
      exclude_status_classes: [4xx]
`

func TestParseValid(t *testing.T) {
	f, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.SLOs) != 2 {
		t.Fatalf("want 2 slos, got %d", len(f.SLOs))
	}
	a := f.SLOs[0]
	if a.Window.Std() != 720*time.Hour {
		t.Errorf("window = %v, want 720h", a.Window.Std())
	}
	if d := a.ErrorBudgetRatio() - 0.005; d > 1e-12 || d < -1e-12 {
		t.Errorf("budget ratio = %v, want 0.005", a.ErrorBudgetRatio())
	}
	l := f.SLOs[1]
	if len(l.Good.StatusClasses) != 2 || l.Good.StatusClasses[0] != "2xx" {
		t.Errorf("latency default status classes = %v, want [2xx 3xx]", l.Good.StatusClasses)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"empty file", "", "empty"},
		{"no slos", "slos: []", "no slos"},
		{"unknown field", "slos:\n  - name: a\n    objectiv: 99\n", "objectiv"},
		{"bad window", strings.Replace(validYAML, "window: 30d", "window: 30x", 1), "invalid duration"},
		{"duplicate name", validYAML + `
  - name: checkout-latency
    service: checkout
    sli: latency
    objective: 99
    window: 30d
    good:
      latency_ms_lte: 300
`, "duplicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

func goodAvail() SLO {
	return SLO{
		Name:      "a",
		Service:   "svc",
		SLI:       event.SLIAvailability,
		Objective: 99.5,
		Window:    Duration(30 * 24 * time.Hour),
		Good:      GoodSpec{StatusClasses: []string{"2xx", "3xx"}},
	}
}

func TestSLOValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*SLO)
		want string // empty = must be valid
	}{
		{"ok", func(s *SLO) {}, ""},
		{"no name", func(s *SLO) { s.Name = "" }, "name is required"},
		{"no service", func(s *SLO) { s.Service = "" }, "service is required"},
		{"unknown sli", func(s *SLO) { s.SLI = "throughput" }, "sli must be"},
		{"objective 100", func(s *SLO) { s.Objective = 100 }, "objective must be"},
		{"objective 0", func(s *SLO) { s.Objective = 0 }, "objective must be"},
		{"zero window", func(s *SLO) { s.Window = 0 }, "window must be"},
		{"availability no good classes", func(s *SLO) { s.Good.StatusClasses = nil }, "good.status_classes"},
		{"unknown class", func(s *SLO) { s.Good.StatusClasses = []string{"6xx"} }, "unknown status class"},
		{"class both good and excluded", func(s *SLO) { s.Valid.ExcludeStatusClasses = []string{"2xx"} }, "both good and excluded"},
		{"latency no threshold", func(s *SLO) { s.SLI = event.SLILatency; s.Good = GoodSpec{} }, "latency_ms_lte"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := goodAvail()
			c.mut(&s)
			err := s.Validate()
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v, want substring %q", err, c.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"30d":   720 * time.Hour,
		"1h":    time.Hour,
		"90m":   90 * time.Minute,
		"1h30m": 90 * time.Minute,
		"45s":   45 * time.Second,
	}
	for in, want := range ok {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0d", "-1h", "-2d", "abc", "3600"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) expected error", in)
		}
	}
}

func TestErrorBudgetRatio(t *testing.T) {
	s := goodAvail()
	s.Objective = 99.9
	if d := s.ErrorBudgetRatio() - 0.001; d > 1e-12 || d < -1e-12 {
		t.Errorf("99.9%% -> %v, want 0.001", s.ErrorBudgetRatio())
	}
}

func TestStatusClass(t *testing.T) {
	cases := map[int]string{200: "2xx", 301: "3xx", 404: "4xx", 503: "5xx", 99: "other", 600: "other", 0: "other"}
	for code, want := range cases {
		if got := StatusClass(code); got != want {
			t.Errorf("StatusClass(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestIsValidIsGood(t *testing.T) {
	f, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	avail, lat := f.SLOs[0], f.SLOs[1]

	type row struct {
		status    int
		latencyMS float64
		valid     bool
		good      bool
	}
	availRows := []row{
		{200, 5000, true, true}, // availability ignores latency
		{301, 10, true, true},
		{404, 10, false, false}, // client error: not valid, so not counted
		{500, 10, true, false},
		{503, 10, true, false},
	}
	for _, r := range availRows {
		if got := avail.IsValid(r.status); got != r.valid {
			t.Errorf("avail IsValid(%d) = %v, want %v", r.status, got, r.valid)
		}
		if got := avail.IsGood(r.status, r.latencyMS); got != r.good {
			t.Errorf("avail IsGood(%d,%v) = %v, want %v", r.status, r.latencyMS, got, r.good)
		}
	}
	latRows := []row{
		{200, 250, true, true},
		{200, 300, true, true}, // boundary is inclusive
		{200, 350, true, false},
		{500, 10, true, false}, // fast failure is still not good
		{404, 10, false, false},
	}
	for _, r := range latRows {
		if got := lat.IsGood(r.status, r.latencyMS); got != r.good {
			t.Errorf("latency IsGood(%d,%v) = %v, want %v", r.status, r.latencyMS, got, r.good)
		}
	}
}

func TestShippedConfigLoads(t *testing.T) {
	f, err := Load("../../configs/slos.yaml")
	if err != nil {
		t.Fatalf("configs/slos.yaml: %v", err)
	}
	if len(f.SLOs) < 3 {
		t.Fatalf("want at least 3 slos, got %d", len(f.SLOs))
	}
}
