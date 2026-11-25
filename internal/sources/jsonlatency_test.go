package sources

import (
	"strings"
	"testing"
)

func TestParseJSONLatencySeconds(t *testing.T) {
	head := `{"ts":1795428002.5,"service":"demo","path":"/pay?token=tok_SECRET123","status":200,`
	cases := []struct {
		name, tail string
		has        bool
		ms         float64
	}{
		{"seconds", `"latency_s":0.123}`, true, 123},
		{"threshold boundary is exact", `"latency_s":0.3}`, true, 300},
		{"zero", `"latency_s":0}`, true, 0},
		{"milliseconds win over seconds", `"latency_ms":50,"latency_s":9}`, true, 50},
		{"none", `"x":1}`, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := ParseJSON(head+c.tail, "")
			if err != nil {
				t.Fatal(err)
			}
			if r.HasLatency != c.has || r.LatencyMS != c.ms {
				t.Errorf("has=%v ms=%v, want %v and %v", r.HasLatency, r.LatencyMS, c.has, c.ms)
			}
		})
	}
	if _, err := ParseJSON(head+`"latency_s":-1}`, ""); err == nil || !strings.Contains(err.Error(), "latency_s") {
		t.Errorf("negative latency_s must be refused, got %v", err)
	}
}
