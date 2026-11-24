package sources

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const nginxTimeLayout = "02/Jan/2006:15:04:05 -0700"

// Matches the nginx "combined" format, an optional trailing "$http_x_forwarded_for" field
// (the official docker image has it) and an optional " rt=$request_time".
// Only time, request line, status and rt are captured. IP, user, referer and agent are skipped.
var nginxRE = regexp.MustCompile(
	`^\S+ \S+ \S+ \[([^\]]+)\] "([^"]*)" (\d{3}) \S+ "[^"]*" "[^"]*"(?: "[^"]*")?(?: rt=(\S+))?(?: rid=(\S+))?\s*$`)

// ParseNginx parses one nginx access log line.
func ParseNginx(line, service string) (Request, error) {
	m := nginxRE.FindStringSubmatch(line)
	if m == nil {
		return Request{}, errors.New("unrecognised nginx line format")
	}
	ts, err := time.Parse(nginxTimeLayout, m[1])
	if err != nil {
		return Request{}, errors.New("bad nginx timestamp")
	}
	status, _ := strconv.Atoi(m[3])
	if status < 100 || status > 599 {
		return Request{}, errors.New("status out of range")
	}
	r := Request{
		Time:    ts.UTC(),
		Service: service,
		Path:    pathFromRequestLine(m[2]),
		Status:  status,
		LineKey: lineKey(service, line),
	}
	if m[5] != "" {
		r.DedupKey = "id:" + service + ":" + m[5]
	}
	if m[4] != "" {
		if rt, err := strconv.ParseFloat(m[4], 64); err == nil && !math.IsNaN(rt) && !math.IsInf(rt, 0) && rt >= 0 {
			r.LatencyMS = math.Round(rt*1e6) / 1e3
			r.HasLatency = true
		}
	}
	return r, nil
}

func pathFromRequestLine(req string) string {
	f := strings.Fields(req)
	if len(f) < 2 {
		return invalidPath
	}
	return NormalizePath(f[1])
}
