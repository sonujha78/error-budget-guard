package sources

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

func TestDedupRequests(t *testing.T) {
	mk := func(id, line string) Request { return Request{DedupKey: id, LineKey: line} }
	reqs := []Request{mk("id:a", "l1"), mk("id:b", "l2"), mk("id:a", "l3"), mk("", "l4"), mk("", "l4"), mk("", "")}

	out, dups := DedupRequests(reqs, false)
	if dups != 1 || len(out) != 5 {
		t.Errorf("by id only: dups=%d len=%d, want 1 and 5", dups, len(out))
	}
	out, dups = DedupRequests(reqs, true)
	if dups != 2 || len(out) != 4 {
		t.Fatalf("with lines: dups=%d len=%d, want 2 and 4", dups, len(out))
	}
	if out[0].LineKey != "l1" || out[1].LineKey != "l2" || out[2].LineKey != "l4" || out[3].LineKey != "" {
		t.Errorf("first occurrence must be kept, order preserved: %+v", out)
	}
}

func TestCountOutOfOrder(t *testing.T) {
	at := func(s int) Request { return Request{Time: time.Date(2026, 11, 24, 10, 0, s, 0, time.UTC)} }
	reqs := []Request{at(1), at(3), at(2), at(4), at(3), at(4)}
	if got := CountOutOfOrder(reqs); got != 2 {
		t.Errorf("out of order = %d, want 2 (equal times are in order)", got)
	}
	if CountOutOfOrder(nil) != 0 {
		t.Error("empty input has nothing out of order")
	}
}

func jsonLine(ts, rid string) string {
	return fmt.Sprintf(`{"ts":%q,"service":"checkout","path":"/a","status":200,"request_id":%q}`, ts, rid)
}

func nginxRid(rid string) string {
	return `203.0.113.7 - - [23/Nov/2026:10:00:01 +0000] "GET /pay HTTP/1.1" 502 1 "-" "curl/8.5.0" rt=0.010 rid=` + rid
}

func TestCollectDropsDuplicateRequestIDsAndCountsOutOfOrder(t *testing.T) {
	in := strings.Join([]string{
		jsonLine("2026-11-23T10:00:03Z", "r-1"),
		jsonLine("2026-11-23T10:00:01Z", "r-2"), // arrived late
		jsonLine("2026-11-23T10:00:03Z", "r-1"), // duplicate
		"",
		jsonLine("2026-11-23T10:00:02Z", "r-3"), // arrived late
		"not json",
	}, "\n")
	res, err := Collect(strings.NewReader(in), FormatJSON, "", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.Lines != 6 || st.Blank != 1 || st.Malformed != 1 || st.Duplicates != 1 || st.Requests != 3 || st.OutOfOrder != 2 {
		t.Errorf("stats = %+v", st)
	}
	if a := sums(res.Events, "checkout", event.SLIAvailability); a.Good != 3 || a.Valid != 3 {
		t.Errorf("availability = %+v, want 3/3", a)
	}
}

func TestIdenticalNginxLinesAreKeptByDefault(t *testing.T) {
	line := `203.0.113.7 - - [23/Nov/2026:10:00:01 +0000] "GET /boom HTTP/1.1" 502 150 "-" "curl/8.5.0" rt=0.010`
	in := strings.Repeat(line+"\n", 20)

	res, err := Collect(strings.NewReader(in), FormatNginx, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Requests != 20 || res.Stats.Duplicates != 0 {
		t.Errorf("default: requests=%d duplicates=%d, want 20 and 0 (identical lines can be real requests)", res.Stats.Requests, res.Stats.Duplicates)
	}
	if a := sums(res.Events, "checkout", event.SLIAvailability); a.Good != 0 || a.Valid != 20 {
		t.Errorf("all 20 failures must be counted, got %+v", a)
	}

	res, err = CollectOpts(strings.NewReader(in), FormatNginx, "checkout", shippedSLOs(t), Options{DedupeLines: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Requests != 1 || res.Stats.Duplicates != 19 {
		t.Errorf("opt in: requests=%d duplicates=%d, want 1 and 19", res.Stats.Requests, res.Stats.Duplicates)
	}
}

func TestNginxRequestIDDedupe(t *testing.T) {
	in := nginxRid("aaa") + "\n" + nginxRid("bbb") + "\n" + nginxRid("aaa") + "\n"
	res, err := Collect(strings.NewReader(in), FormatNginx, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Requests != 2 || res.Stats.Duplicates != 1 {
		t.Errorf("requests=%d duplicates=%d, want 2 and 1", res.Stats.Requests, res.Stats.Duplicates)
	}

	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString(nginxRid(fmt.Sprintf("%032x", i)) + "\n")
	}
	res, err = Collect(strings.NewReader(b.String()), FormatNginx, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Requests != 20 || res.Stats.Duplicates != 0 {
		t.Errorf("20 identical lines with different ids are 20 requests: %+v", res.Stats)
	}
}

func TestParseNginxRequestID(t *testing.T) {
	r, err := ParseNginx(nginxRid("abc123"), "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if r.DedupKey != "id:checkout:abc123" || r.LineKey == "" || !r.HasLatency || r.LatencyMS != 10 {
		t.Errorf("request = %+v", r)
	}
	r, err = ParseNginx(nginxLine, "checkout")
	if err != nil || r.DedupKey != "" || r.LineKey == "" {
		t.Errorf("a line without rid has no explicit key: %+v err=%v", r, err)
	}
}

func TestDuplicateLinesShareKeys(t *testing.T) {
	reqs, _, err := ReadRequests(openSample(t, "nginx_sample.log"), FormatNginx, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].LineKey != reqs[5].LineKey {
		t.Error("identical lines must share a line key")
	}
	if reqs[0].LineKey == reqs[1].LineKey {
		t.Error("different lines must not share a line key")
	}
	reqs, _, err = ReadRequests(openSample(t, "json_sample.log"), FormatJSON, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].DedupKey == "" || reqs[0].DedupKey != reqs[3].DedupKey {
		t.Errorf("same request_id must share a key: %q vs %q", reqs[0].DedupKey, reqs[3].DedupKey)
	}
}

func TestNginxSampleWithLineDedupe(t *testing.T) {
	res, err := CollectOpts(openSample(t, "nginx_sample.log"), FormatNginx, "checkout", shippedSLOs(t), Options{DedupeLines: true})
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.Lines != 7 || st.Malformed != 1 || st.Duplicates != 1 || st.Requests != 5 || st.Events != 8 {
		t.Errorf("stats = %+v", st)
	}
	if a := sums(res.Events, "checkout", event.SLIAvailability); a.Good != 3 || a.Valid != 4 {
		t.Errorf("availability = %+v, want 3/4", a)
	}
	if l := sums(res.Events, "checkout", event.SLILatency); l.Good != 2 || l.Valid != 4 {
		t.Errorf("latency = %+v, want 2/4", l)
	}
}

func TestEveryLineIsAccountedFor(t *testing.T) {
	for _, c := range []struct{ file, format string }{
		{"nginx_sample.log", FormatNginx},
		{"json_sample.log", FormatJSON},
		{"probe_sample.log", FormatProbe},
	} {
		res, err := CollectOpts(openSample(t, c.file), c.format, "checkout", shippedSLOs(t), Options{DedupeLines: true})
		if err != nil {
			t.Fatal(err)
		}
		st := res.Stats
		if st.Lines != st.Blank+st.Malformed+st.Requests+st.Duplicates {
			t.Errorf("%s: lines=%d but blank+malformed+requests+duplicates=%d", c.file, st.Lines, st.Blank+st.Malformed+st.Requests+st.Duplicates)
		}
	}
}

func TestResultTimes(t *testing.T) {
	res, err := Collect(openSample(t, "nginx_sample.log"), FormatNginx, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Times) != 6 {
		t.Errorf("nginx times = %d, want one per request (6)", len(res.Times))
	}
	res, err = Collect(openSample(t, "prom_sample.txt"), FormatProm, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Times) != 3 {
		t.Errorf("prom times = %d, want one per scrape (3), even when a scrape had no new requests", len(res.Times))
	}
}
