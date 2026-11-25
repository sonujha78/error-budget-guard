package alert

import (
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

// The demo policy is written in seconds for a 3 minute SLO window, so scale is exactly 1.
func TestDemoConfigs(t *testing.T) {
	f, err := slo.Load("../../configs/demo/slos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.SLOs) != 2 || f.SLOs[0].Name != "demo-availability" || f.SLOs[1].Name != "demo-latency" {
		t.Fatalf("slos = %+v", f.SLOs)
	}
	for _, s := range f.SLOs {
		if s.Window.Std() != 3*time.Minute || s.Objective != 99 {
			t.Errorf("%s: window %v objective %v", s.Name, s.Window.Std(), s.Objective)
		}
	}
	if f.SLOs[1].Good.LatencyMSLTE != 300 {
		t.Errorf("latency threshold = %v, want 300", f.SLOs[1].Good.LatencyMSLTE)
	}
	p, err := Load("../../configs/demo/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	window := f.SLOs[0].Window.Std()
	scale := p.Scale(window)
	near(t, "scale", scale, 1, 1e-12)
	long := []time.Duration{6 * time.Second, 18 * time.Second, 36 * time.Second}
	short := []time.Duration{time.Second, 3 * time.Second, 6 * time.Second}
	req := []int64{35, 84, 250}
	used := []float64{0.48, 0.60, 0.40}
	if len(p.Rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(p.Rules))
	}
	for i, r := range p.Rules {
		if l, s := ScaleDuration(r.Long.Std(), scale), ScaleDuration(r.Short.Std(), scale); l != long[i] || s != short[i] {
			t.Errorf("%s: windows %v/%v, want %v/%v", r.Name, l, s, long[i], short[i])
		}
		if got := p.RequiredValid(r, f.SLOs[0].ErrorBudgetRatio()); got != req[i] {
			t.Errorf("%s: required valid = %d, want %d", r.Name, got, req[i])
		}
		near(t, r.Name+" budget used", budget.ConsumedByBurn(r.Burn, long[i], window), used[i], budget.Tolerance)
	}
}
