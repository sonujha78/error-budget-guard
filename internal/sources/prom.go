package sources

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FormatProm is a recorded file of Prometheus text-format scrapes.
// Every scrape starts with a header line:  # ebg-scrape ts=2026-11-24T10:00:00Z
// A live /metrics response has no timestamp, so the collector writes this header.
const FormatProm = "prom"

const sourceProm = "prometheus"

// The only metrics the guard reads. Counters of other names are ignored.
const (
	MetricRequests = "http_requests_total"
	MetricBucket   = "http_request_duration_seconds_bucket"
	MetricCount    = "http_request_duration_seconds_count"
)

// Sample is one counter value. Only handler, code and le are kept as text.
// Series is a hash of the full label set (so instance IPs or tokens are never stored as text);
// it is used only to match the same series between scrapes.
type Sample struct {
	Metric  string
	Series  string
	Handler string
	Code    string
	Le      string
	Value   float64
}

// Scrape is all kept samples read at one moment.
type Scrape struct {
	Time    time.Time
	Samples []Sample
}

var scrapeHeaderRE = regexp.MustCompile(`^# ebg-scrape ts=(\S+)$`)

func isNameChar(c byte) bool {
	return c == '_' || c == ':' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// parseLabels reads {a="b",c="d"} from the start of s and returns the labels and the rest.
func parseLabels(s string) (map[string]string, string, error) {
	labels := map[string]string{}
	i := 1 // after '{'
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			return nil, "", errors.New("unterminated label set")
		}
		if s[i] == '}' {
			return labels, s[i+1:], nil
		}
		j := i
		for j < len(s) && isNameChar(s[j]) {
			j++
		}
		if j == i || j+1 >= len(s) || s[j] != '=' || s[j+1] != '"' {
			return nil, "", errors.New("bad label")
		}
		var val strings.Builder
		k := j + 2
		closed := false
		for k < len(s) {
			c := s[k]
			if c == '\\' && k+1 < len(s) {
				if s[k+1] == 'n' {
					val.WriteByte('\n')
				} else {
					val.WriteByte(s[k+1])
				}
				k += 2
				continue
			}
			if c == '"' {
				closed = true
				k++
				break
			}
			val.WriteByte(c)
			k++
		}
		if !closed {
			return nil, "", errors.New("unterminated label value")
		}
		labels[s[i:j]] = val.String()
		i = k
	}
}

// parseSampleLine parses  name{labels} value [timestamp_ms].
// Errors are fixed texts: they never contain the line.
func parseSampleLine(line string) (string, map[string]string, float64, error) {
	i := 0
	for i < len(line) && isNameChar(line[i]) {
		i++
	}
	if i == 0 {
		return "", nil, 0, errors.New("bad metric name")
	}
	name := line[:i]
	labels := map[string]string{}
	rest := line[i:]
	if strings.HasPrefix(rest, "{") {
		var err error
		labels, rest, err = parseLabels(rest)
		if err != nil {
			return "", nil, 0, err
		}
	}
	fields := strings.Fields(rest)
	if len(fields) < 1 || len(fields) > 2 {
		return "", nil, 0, errors.New("bad sample line")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", nil, 0, errors.New("bad sample value")
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", nil, 0, errors.New("sample value is not finite")
	}
	if len(fields) == 2 {
		if _, err := strconv.ParseInt(fields[1], 10, 64); err != nil {
			return "", nil, 0, errors.New("bad sample timestamp")
		}
	}
	return name, labels, v, nil
}

func buildSample(name string, labels map[string]string, v float64) (Sample, bool) {
	switch name {
	case MetricRequests, MetricBucket, MetricCount:
	default:
		return Sample{}, false
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(name))
	for _, k := range keys {
		h.Write([]byte{0})
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(labels[k]))
	}
	return Sample{
		Metric:  name,
		Series:  hex.EncodeToString(h.Sum(nil)[:8]),
		Handler: NormalizePath(labels["handler"]),
		Code:    labels["code"],
		Le:      labels["le"],
		Value:   v,
	}, true
}

// ParseScrapes reads a recorded scrape file. Bad lines are counted and never echoed.
// If a scrape header is bad, that whole scrape is skipped and counted once.
func ParseScrapes(r io.Reader) ([]Scrape, Stats, error) {
	var st Stats
	bad := func(msg string) {
		st.Malformed++
		if len(st.Errors) < maxErrors {
			st.Errors = append(st.Errors, fmt.Sprintf("line %d: %s", st.Lines, msg))
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var scrapes []Scrape
	cur := -1
	skipping := false
	for sc.Scan() {
		st.Lines++
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			st.Blank++
		case strings.HasPrefix(line, "# ebg-scrape"):
			m := scrapeHeaderRE.FindStringSubmatch(line)
			var ts time.Time
			var err error
			if m != nil {
				ts, err = time.Parse(time.RFC3339Nano, m[1])
			}
			if m == nil || err != nil {
				bad("bad scrape header")
				cur, skipping = -1, true
				break
			}
			scrapes = append(scrapes, Scrape{Time: ts.UTC()})
			cur, skipping = len(scrapes)-1, false
		case strings.HasPrefix(line, "#"):
			// HELP, TYPE and other comments carry no values.
		default:
			if skipping {
				break
			}
			if cur < 0 {
				bad("sample before any scrape header")
				break
			}
			name, labels, v, err := parseSampleLine(line)
			if err != nil {
				bad(err.Error())
				break
			}
			s, ok := buildSample(name, labels, v)
			if !ok {
				break
			}
			if s.Value < 0 {
				bad("negative counter value")
				break
			}
			scrapes[cur].Samples = append(scrapes[cur].Samples, s)
		}
	}
	st.Scrapes = len(scrapes)
	if err := sc.Err(); err != nil {
		return scrapes, st, fmt.Errorf("read input near line %d: %w", st.Lines+1, err)
	}
	return scrapes, st, nil
}
