package scenario

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const base = `
name: t
rps: 10
duration: 180s
paths:
  - {path: /api/pay, weight: 5}
  - {path: /api/cart, weight: 3}
  - {path: /api/search, weight: 2}
`

func withFault(f string) string { return base + "faults:\n  - " + f + "\n" }

func mustParse(t *testing.T, y string) *Scenario {
	t.Helper()
	s, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"empty file", "", "empty"},
		{"unknown field", base + "extra: 1\n", "extra"},
		{"no name", strings.Replace(base, "name: t\n", "", 1), "name is required"},
		{"zero rps", strings.Replace(base, "rps: 10", "rps: 0", 1), "rps must be"},
		{"huge rps", strings.Replace(base, "rps: 10", "rps: 5000", 1), "rps must be"},
		{"zero duration", strings.Replace(base, "duration: 180s", "duration: 0s", 1), "duration must be positive"},
		{"bad duration", strings.Replace(base, "duration: 180s", "duration: soon", 1), "invalid duration"},
		{"no paths", "name: t\nrps: 10\nduration: 180s\n", "at least one path"},
		{"zero weight", strings.Replace(base, "weight: 5", "weight: 0", 1), "weight must be"},
		{"relative path", strings.Replace(base, "path: /api/pay", "path: api/pay", 1), "must start with /"},
		{"unknown kind", withFault("{kind: boom, from: 1s, to: 10s}"), "kind must be"},
		{"to before from", withFault("{kind: error, from: 10s, to: 5s}"), "to must be after from"},
		{"to after duration", withFault("{kind: error, from: 10s, to: 200s}"), "within the duration"},
		{"slow without delay", withFault("{kind: slow, from: 1s, to: 10s}"), "delay_ms"},
		{"fault path not listed", withFault("{kind: error, path: /nope, from: 1s, to: 10s}"), "not one of the scenario paths"},
		{"negative every", withFault("{kind: error, from: 1s, to: 10s, every: -1}"), "every must"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v, want substring %q", err, c.want)
			}
		})
	}
}

func TestPlanCountsPathsAndTiming(t *testing.T) {
	s := mustParse(t, base)
	plan := s.Plan()
	if len(plan) != 1800 || plan[0].At != 0 || plan[1].At != 100*time.Millisecond || plan[1799].At != 179900*time.Millisecond {
		t.Fatalf("len=%d first=%v second=%v last=%v", len(plan), plan[0].At, plan[1].At, plan[len(plan)-1].At)
	}
	sum := s.Summarize()
	want := map[string]int{"/api/pay": 900, "/api/cart": 540, "/api/search": 360}
	if sum.Total != 1800 || sum.Errors != 0 || sum.Slow != 0 || !reflect.DeepEqual(sum.ByPath, want) {
		t.Errorf("summary = %+v", sum)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	s := mustParse(t, withFault("{kind: error, from: 1s, to: 180s, every: 7}"))
	if !reflect.DeepEqual(s.Plan(), s.Plan()) {
		t.Error("two plans of the same scenario differ")
	}
}

func TestErrorEveryNthRequest(t *testing.T) {
	s := mustParse(t, withFault("{kind: error, from: 1s, to: 180s, every: 500}"))
	var idx []int
	for i, r := range s.Plan() {
		if r.Fail {
			idx = append(idx, i)
		}
	}
	if !reflect.DeepEqual(idx, []int{509, 1009, 1509}) {
		t.Errorf("failing indexes = %v, want [509 1009 1509]", idx)
	}
	f := s.Summarize().Faults[0]
	if f.Matched != 1790 || f.Applied != 3 {
		t.Errorf("fault summary = %+v, want matched 1790 applied 3", f)
	}
}

func TestPathOutageHasExactBoundaries(t *testing.T) {
	s := mustParse(t, withFault("{kind: error, path: /api/pay, from: 100s, to: 120s}"))
	plan := s.Plan()
	n, first, last := 0, -1, -1
	for i, r := range plan {
		if r.Fail {
			if r.Path != "/api/pay" {
				t.Fatalf("request %d fails on %s", i, r.Path)
			}
			n++
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if n != 100 || first != 1000 || last != 1194 {
		t.Errorf("errors=%d first=%d last=%d, want 100, 1000, 1194 (start inclusive, end exclusive)", n, first, last)
	}
}

func TestSlowOnlyOnMatchingPath(t *testing.T) {
	s := mustParse(t, withFault("{kind: slow, path: /api/search, from: 150s, to: 180s, every: 5, delay_ms: 800}"))
	n := 0
	for _, r := range s.Plan() {
		if r.DelayMS == 0 {
			continue
		}
		n++
		if r.Path != "/api/search" || r.DelayMS != 800 || r.Fail {
			t.Errorf("unexpected slow request %+v", r)
		}
	}
	sum := s.Summarize()
	if n != 12 || sum.Slow != 12 || sum.Errors != 0 {
		t.Errorf("slow=%d summary=%+v, want 12 slow, 0 errors", n, sum)
	}
}

func TestFractionalRPS(t *testing.T) {
	s := mustParse(t, strings.Replace(base, "rps: 10", "rps: 0.5", 1))
	plan := s.Plan()
	if len(plan) != 90 || plan[1].At != 2*time.Second {
		t.Errorf("len=%d second at %v, want 90 and 2s", len(plan), plan[1].At)
	}
}

// The ground truth of the five shipped scenarios, worked out by hand before anything was recorded.
func TestShippedScenarios(t *testing.T) {
	want := map[string][3]int{ // total, errors, slow
		"healthy":             {1800, 3, 0},
		"slow-burn":           {1800, 12, 0},
		"fast-burn":           {1800, 100, 0},
		"latency-degradation": {1800, 0, 12},
		"low-traffic":         {90, 10, 0},
	}
	files, err := filepath.Glob("../../../scenarios/*/scenario.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(want) {
		t.Fatalf("found %d scenario files, want %d", len(files), len(want))
	}
	for _, f := range files {
		s, err := Load(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		dir := filepath.Base(filepath.Dir(f))
		w, ok := want[dir]
		if !ok || s.Name != dir {
			t.Errorf("%s: name %q does not match its directory", f, s.Name)
			continue
		}
		sum := s.Summarize()
		if got := [3]int{sum.Total, sum.Errors, sum.Slow}; got != w {
			t.Errorf("%s: total/errors/slow = %v, want %v", dir, got, w)
		}
		if s.Description == "" {
			t.Errorf("%s: the expected outcome must be written in the description", dir)
		}
	}
	if _, err := os.Stat("../../../scenarios/healthy/scenario.yaml"); err != nil {
		t.Fatal(err)
	}
}
