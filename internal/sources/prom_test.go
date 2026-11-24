package sources

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

const (
	t00 = "2026-11-24T10:00:00Z"
	t15 = "2026-11-24T10:00:15Z"
	t30 = "2026-11-24T10:00:30Z"
	t45 = "2026-11-24T10:00:45Z"
)

func mkScrape(ts string, samples ...Sample) Scrape {
	tm, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return Scrape{Time: tm.UTC(), Samples: samples}
}

func reqSample(series, handler, code string, v float64) Sample {
	return Sample{Metric: MetricRequests, Series: series, Handler: handler, Code: code, Value: v}
}

func histSample(metric, series, handler, code, le string, v float64) Sample {
	return Sample{Metric: metric, Series: series, Handler: handler, Code: code, Le: le, Value: v}
}

func sumAll(evs []event.Event, service, sli string) budget.Counts {
	return budget.Sum(evs, service, sli,
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
}

func checkoutSLOs(t *testing.T) []slo.SLO {
	t.Helper()
	var out []slo.SLO
	for _, s := range shippedSLOs(t) {
		if s.Service == "checkout" {
			out = append(out, s)
		}
	}
	return out
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func findEvent(t *testing.T, evs []event.Event, sli, label string) event.Event {
	t.Helper()
	for _, e := range evs {
		if e.SLI == sli && e.Label == label {
			return e
		}
	}
	t.Fatalf("no %s event with label %q in %+v", sli, label, evs)
	return event.Event{}
}

func TestCounterDelta(t *testing.T) {
	cases := []struct {
		prev, cur, want float64
		reset           bool
	}{
		{10, 15, 5, false},
		{15, 15, 0, false},
		{100, 3, 3, true},
		{5, 0, 0, true},
	}
	for _, c := range cases {
		got, reset := CounterDelta(c.prev, c.cur)
		if got != c.want || reset != c.reset {
			t.Errorf("CounterDelta(%v,%v) = %v,%v; want %v,%v", c.prev, c.cur, got, reset, c.want, c.reset)
		}
		if got < 0 {
			t.Errorf("CounterDelta(%v,%v) is negative", c.prev, c.cur)
		}
	}
}

func TestParseSampleLine(t *testing.T) {
	cases := []struct {
		line   string
		name   string
		labels map[string]string
		value  float64
	}{
		{`http_requests_total{handler="/pay",code="200"} 12`, "http_requests_total", map[string]string{"handler": "/pay", "code": "200"}, 12},
		{`http_requests_total{handler="/pay",code="200"} 12 1700000000000`, "http_requests_total", map[string]string{"handler": "/pay", "code": "200"}, 12},
		{`go_goroutines 12`, "go_goroutines", map[string]string{}, 12},
		{`m{} 3`, "m", map[string]string{}, 3},
		{`m{a="x\"y",b="p\\q"} 1`, "m", map[string]string{"a": `x"y`, "b": `p\q`}, 1},
		{`m{a="1",} 1e3`, "m", map[string]string{"a": "1"}, 1000},
	}
	for _, c := range cases {
		name, labels, v, err := parseSampleLine(c.line)
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.line, err)
			continue
		}
		if name != c.name || v != c.value || len(labels) != len(c.labels) {
			t.Errorf("%q: got name=%q labels=%v value=%v", c.line, name, labels, v)
			continue
		}
		for k, want := range c.labels {
			if labels[k] != want {
				t.Errorf("%q: label %s = %q, want %q", c.line, k, labels[k], want)
			}
		}
	}
	for _, line := range []string{
		`m{a="b"}`, `m{a="b} 1`, `m{a="b"`, `m 1 NaN`,
		`{a="b"} 1`, `m{a=b} 1`, `m abc`, `this is not a metric 203.0.113.7`,
	} {
		_, _, _, err := parseSampleLine(line)
		if err == nil {
			t.Errorf("%q: expected an error", line)
			continue
		}
		noSecrets(t, "error text", err.Error())
	}
}

func TestParseScrapesSample(t *testing.T) {
	scrapes, st, err := ParseScrapes(openSample(t, "prom_sample.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scrapes) != 3 || st.Scrapes != 3 {
		t.Fatalf("scrapes = %d (stats %d), want 3", len(scrapes), st.Scrapes)
	}
	if st.Malformed != 1 || len(st.Errors) != 1 || !strings.HasPrefix(st.Errors[0], "line ") {
		t.Errorf("malformed=%d errors=%v", st.Malformed, st.Errors)
	}
	for i, s := range scrapes {
		if len(s.Samples) != 9 {
			t.Errorf("scrape %d has %d kept samples, want 9 (go_goroutines must be dropped)", i, len(s.Samples))
		}
	}
	if !scrapes[1].Time.Equal(time.Date(2026, 11, 24, 10, 0, 15, 0, time.UTC)) {
		t.Errorf("scrape 1 time = %v", scrapes[1].Time)
	}
}

func TestParseScrapesBadHeaderSkipsItsLines(t *testing.T) {
	in := "# ebg-scrape ts=yesterday\n" +
		`http_requests_total{handler="/a",code="200"} 5` + "\n" +
		"# ebg-scrape ts=2026-11-24T10:00:00Z\n" +
		`http_requests_total{handler="/a",code="200"} 7` + "\n"
	scrapes, st, err := ParseScrapes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(scrapes) != 1 || st.Malformed != 1 || len(scrapes[0].Samples) != 1 {
		t.Errorf("scrapes=%d malformed=%d samples=%d", len(scrapes), st.Malformed, len(scrapes[0].Samples))
	}
}

func TestParseScrapesSampleBeforeHeader(t *testing.T) {
	_, st, err := ParseScrapes(strings.NewReader(`http_requests_total{handler="/a",code="200"} 5` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Malformed != 1 {
		t.Errorf("malformed = %d, want 1", st.Malformed)
	}
}

func TestPromEventsCounterReset(t *testing.T) {
	scrapes := []Scrape{
		mkScrape(t00, reqSample("a", "/pay", "200", 1000)),
		mkScrape(t15, reqSample("a", "/pay", "200", 1100)),
		mkScrape(t30, reqSample("a", "/pay", "200", 40)), // service restarted
		mkScrape(t45, reqSample("a", "/pay", "200", 90)),
	}
	evs, rep := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	a := sumAll(evs, "checkout", event.SLIAvailability)
	// 100 + 40 (reset, counted from zero) + 50. Never 1100, never negative.
	if a.Valid != 190 || a.Good != 190 {
		t.Errorf("availability counts = %+v, want good=190 valid=190", a)
	}
	if rep.Resets != 1 {
		t.Errorf("resets = %d, want 1", rep.Resets)
	}
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			t.Errorf("invalid event %+v: %v", e, err)
		}
		if e.Valid <= 0 {
			t.Errorf("event with no requests: %+v", e)
		}
	}
	if l := sumAll(evs, "checkout", event.SLILatency); l.Valid != 0 {
		t.Errorf("no histogram, so no latency events; got %+v", l)
	}
	if !anyContains(rep.Warnings, "latency histogram") {
		t.Errorf("expected a warning about the missing latency histogram, got %v", rep.Warnings)
	}
}

func TestPromEventsBaselineOnly(t *testing.T) {
	evs, rep := PromEvents([]Scrape{mkScrape(t00, reqSample("a", "/pay", "200", 1000))}, "checkout", checkoutSLOs(t))
	if len(evs) != 0 {
		t.Errorf("one scrape is only a baseline, got %d events", len(evs))
	}
	if !anyContains(rep.Warnings, "two scrapes") {
		t.Errorf("expected a 'two scrapes' warning, got %v", rep.Warnings)
	}
}

func TestPromEventsNoSLOForService(t *testing.T) {
	scrapes := []Scrape{
		mkScrape(t00, reqSample("a", "/pay", "200", 1)),
		mkScrape(t15, reqSample("a", "/pay", "200", 2)),
	}
	evs, rep := PromEvents(scrapes, "nobody", checkoutSLOs(t))
	if len(evs) != 0 || !anyContains(rep.Warnings, "no slo") {
		t.Errorf("events=%d warnings=%v", len(evs), rep.Warnings)
	}
}

func TestPromEventsNewSeriesStartsFromZero(t *testing.T) {
	scrapes := []Scrape{
		mkScrape(t00, reqSample("a", "/pay", "200", 100)),
		mkScrape(t15, reqSample("a", "/pay", "200", 110), reqSample("b", "/cart", "200", 7)),
	}
	evs, rep := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	if a := sumAll(evs, "checkout", event.SLIAvailability); a.Valid != 17 || a.Good != 17 {
		t.Errorf("counts = %+v, want 17/17 (10 from a, 7 from the new series)", a)
	}
	if rep.NewSeries != 1 {
		t.Errorf("new series = %d, want 1", rep.NewSeries)
	}
}

func TestPromEventsDuplicateAndOutOfOrder(t *testing.T) {
	scrapes := []Scrape{
		mkScrape(t00, reqSample("a", "/pay", "200", 1000)),
		mkScrape(t30, reqSample("a", "/pay", "200", 1200)),
		mkScrape(t15, reqSample("a", "/pay", "200", 1100)), // arrived late
		mkScrape(t15, reqSample("a", "/pay", "200", 1100)), // duplicate
	}
	evs, rep := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	if a := sumAll(evs, "checkout", event.SLIAvailability); a.Valid != 200 {
		t.Errorf("valid = %d, want 200", a.Valid)
	}
	if rep.Duplicates != 1 || rep.OutOfOrder != 1 {
		t.Errorf("duplicates=%d out_of_order=%d, want 1 and 1", rep.Duplicates, rep.OutOfOrder)
	}
	if len(evs) != 2 || !evs[0].Timestamp.Before(evs[1].Timestamp) {
		t.Errorf("events must be 2 and in time order: %+v", evs)
	}
}

func TestPromEventsMissingSeriesKeepsLastValue(t *testing.T) {
	scrapes := []Scrape{
		mkScrape(t00, reqSample("a", "/pay", "200", 1000), reqSample("b", "/cart", "200", 500)),
		mkScrape(t15, reqSample("a", "/pay", "200", 1100)), // b missing in this scrape
		mkScrape(t30, reqSample("a", "/pay", "200", 1200), reqSample("b", "/cart", "200", 560)),
	}
	evs, rep := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	if a := sumAll(evs, "checkout", event.SLIAvailability); a.Valid != 260 {
		t.Errorf("valid = %d, want 260 (100 + 100 + 60)", a.Valid)
	}
	if rep.NewSeries != 0 || rep.Resets != 0 {
		t.Errorf("a gap is not a new series or a reset: %+v", rep)
	}
}

func TestPromLatencyEvents(t *testing.T) {
	b, c := MetricBucket, MetricCount
	scrapes := []Scrape{
		mkScrape(t00,
			histSample(c, "c200", "/pay", "200", "", 100),
			histSample(b, "b200", "/pay", "200", "0.3", 90),
			histSample(b, "i200", "/pay", "200", "+Inf", 100),
			histSample(c, "c500", "/pay", "500", "", 5),
			histSample(b, "b500", "/pay", "500", "0.3", 5)),
		mkScrape(t15,
			histSample(c, "c200", "/pay", "200", "", 200),
			histSample(b, "b200", "/pay", "200", "0.3", 170),
			histSample(b, "i200", "/pay", "200", "+Inf", 200),
			histSample(c, "c500", "/pay", "500", "", 8),
			histSample(b, "b500", "/pay", "500", "0.3", 8)),
	}
	evs, _ := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	ok := findEvent(t, evs, event.SLILatency, "/pay|2xx")
	if ok.Good != 80 || ok.Valid != 100 {
		t.Errorf("/pay 2xx latency = %d/%d, want 80/100", ok.Good, ok.Valid)
	}
	bad := findEvent(t, evs, event.SLILatency, "/pay|5xx")
	if bad.Good != 0 || bad.Valid != 3 {
		t.Errorf("/pay 5xx latency = %d/%d, want 0/3 (failures are never good)", bad.Good, bad.Valid)
	}
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			t.Errorf("invalid event %+v: %v", e, err)
		}
	}
}

func TestPromLatencyThresholdNotABucket(t *testing.T) {
	b, c := MetricBucket, MetricCount
	scrapes := []Scrape{
		mkScrape(t00,
			histSample(c, "c", "/pay", "200", "", 100),
			histSample(b, "b1", "/pay", "200", "0.25", 80),
			histSample(b, "b2", "/pay", "200", "0.5", 95)),
		mkScrape(t15,
			histSample(c, "c", "/pay", "200", "", 200),
			histSample(b, "b1", "/pay", "200", "0.25", 160),
			histSample(b, "b2", "/pay", "200", "0.5", 190)),
	}
	evs, rep := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	if len(evs) != 0 {
		t.Errorf("300ms is not a bucket boundary: no latency events expected, got %+v", evs)
	}
	if !anyContains(rep.Warnings, "bucket boundary") {
		t.Errorf("expected a 'bucket boundary' warning, got %v", rep.Warnings)
	}
}

func TestPromLatencyClampsInconsistentHistogram(t *testing.T) {
	b, c := MetricBucket, MetricCount
	scrapes := []Scrape{
		mkScrape(t00, histSample(c, "c", "/pay", "200", "", 100), histSample(b, "b", "/pay", "200", "0.3", 90)),
		mkScrape(t15, histSample(c, "c", "/pay", "200", "", 110), histSample(b, "b", "/pay", "200", "0.3", 102)),
	}
	evs, rep := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	e := findEvent(t, evs, event.SLILatency, "/pay|2xx")
	if e.Good != 10 || e.Valid != 10 || e.Validate() != nil {
		t.Errorf("event = %+v, want good=10 valid=10 and valid", e)
	}
	if rep.Clamped != 1 {
		t.Errorf("clamped = %d, want 1", rep.Clamped)
	}
}

func TestPromIgnoresSamplesWithoutCode(t *testing.T) {
	scrapes := []Scrape{
		mkScrape(t00, reqSample("a", "/pay", "", 100)),
		mkScrape(t15, reqSample("a", "/pay", "", 200)),
	}
	evs, rep := PromEvents(scrapes, "checkout", checkoutSLOs(t))
	if len(evs) != 0 {
		t.Errorf("no code label means status unknown: no events expected, got %+v", evs)
	}
	if !anyContains(rep.Warnings, "code label") {
		t.Errorf("expected a 'code label' warning, got %v", rep.Warnings)
	}
}

func TestCollectPromSampleEndToEnd(t *testing.T) {
	res, err := Collect(openSample(t, "prom_sample.txt"), FormatProm, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.Scrapes != 3 || st.Malformed != 1 || st.Events != 10 || st.Resets != 9 || st.NewSeries != 0 {
		t.Errorf("stats = %+v", st)
	}
	if a := sumAll(res.Events, "checkout", event.SLIAvailability); a.Good != 210 || a.Valid != 217 {
		t.Errorf("availability = %+v, want good=210 valid=217", a)
	}
	if l := sumAll(res.Events, "checkout", event.SLILatency); l.Good != 110 || l.Valid != 147 {
		t.Errorf("latency = %+v, want good=110 valid=147", l)
	}
	for _, e := range res.Events {
		if e.Source != "prometheus" || e.Validate() != nil {
			t.Errorf("bad event %+v", e)
		}
	}
	b, err := json.Marshal(res.Events)
	if err != nil {
		t.Fatal(err)
	}
	noSecrets(t, "events", string(b))
	noSecrets(t, "stats errors", strings.Join(res.Stats.Errors, "\n"))
	noSecrets(t, "warnings", strings.Join(res.Stats.Warnings, "\n"))
}

func TestCollectPromNeedsService(t *testing.T) {
	if _, err := Collect(strings.NewReader(""), FormatProm, "", shippedSLOs(t)); err == nil {
		t.Error("prometheus input without a service name must be an error")
	}
}

func TestParseSampleLineAcceptsNonFiniteValues(t *testing.T) {
	for _, line := range []string{
		`m NaN`, `m +Inf`, `m -Inf`,
		`go_gc_duration_seconds{quantile="0.5"} NaN`,
	} {
		if _, _, _, err := parseSampleLine(line); err != nil {
			t.Errorf("%q is valid exposition format, got error: %v", line, err)
		}
	}
}

func TestParseScrapesIgnoresNonFiniteOfUntrackedMetrics(t *testing.T) {
	in := "# ebg-scrape ts=2026-11-24T10:00:00Z\n" +
		`go_gc_duration_seconds{quantile="0.5"} NaN` + "\n" +
		`some_summary{quantile="0.99"} NaN` + "\n" +
		`some_gauge +Inf` + "\n" +
		`http_requests_total{handler="/a",code="200"} 7` + "\n"
	scrapes, st, err := ParseScrapes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if st.Malformed != 0 || len(st.Errors) != 0 {
		t.Errorf("untracked NaN/Inf must not count as malformed: malformed=%d errors=%v", st.Malformed, st.Errors)
	}
	if len(scrapes) != 1 || len(scrapes[0].Samples) != 1 {
		t.Fatalf("want 1 scrape with 1 kept sample, got %+v", scrapes)
	}
}

func TestParseScrapesRejectsNonFiniteTrackedMetric(t *testing.T) {
	in := "# ebg-scrape ts=2026-11-24T10:00:00Z\n" +
		`http_requests_total{handler="/a",code="200"} NaN` + "\n" +
		`http_requests_total{handler="/b",code="200"} +Inf` + "\n" +
		`http_requests_total{handler="/c",code="200"} 5` + "\n"
	scrapes, st, err := ParseScrapes(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if st.Malformed != 2 || len(scrapes[0].Samples) != 1 {
		t.Errorf("malformed=%d samples=%d, want 2 and 1", st.Malformed, len(scrapes[0].Samples))
	}
	if !anyContains(st.Errors, "not finite") {
		t.Errorf("expected a 'not finite' error, got %v", st.Errors)
	}
}
