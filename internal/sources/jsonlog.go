package sources

import (
	"encoding/json"
	"errors"
	"math"
	"time"
)

type rawJSON struct {
	TS        any      `json:"ts"`
	Service   string   `json:"service"`
	Path      string   `json:"path"`
	Status    *float64 `json:"status"`
	LatencyMS *float64 `json:"latency_ms"`
	RequestID string   `json:"request_id"`
}

type rawProbe struct {
	TS        any      `json:"ts"`
	Service   string   `json:"service"`
	Target    string   `json:"target"`
	OK        *bool    `json:"ok"`
	Status    *float64 `json:"status"`
	LatencyMS *float64 `json:"latency_ms"`
}

// parseTS accepts an RFC3339 string or unix seconds. Years outside 2000..2100 are rejected,
// which catches milliseconds or microseconds written where seconds were expected.
func parseTS(v any) (time.Time, error) {
	var t time.Time
	switch x := v.(type) {
	case nil:
		return t, errors.New("ts is required")
	case string:
		p, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			return t, errors.New("ts is not RFC3339")
		}
		t = p.UTC()
	case float64:
		if x < 946684800 || x > 4102444800 {
			return t, errors.New("ts out of range")
		}
		sec, frac := math.Modf(x)
		t = time.Unix(int64(sec), int64(frac*1e9)).UTC()
	default:
		return t, errors.New("ts must be a string or a number")
	}
	if y := t.Year(); y < 2000 || y > 2100 {
		return time.Time{}, errors.New("ts out of range")
	}
	return t, nil
}

func toStatus(f float64) (int, error) {
	if f != math.Trunc(f) || f < 100 || f > 599 {
		return 0, errors.New("status out of range")
	}
	return int(f), nil
}

func pick(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// ParseJSON parses one JSON access log line. Unknown fields (client_ip, user_id, cookie...)
// are never decoded into anything.
func ParseJSON(line, defaultService string) (Request, error) {
	var raw rawJSON
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Request{}, errors.New("invalid json")
	}
	ts, err := parseTS(raw.TS)
	if err != nil {
		return Request{}, err
	}
	if raw.Status == nil {
		return Request{}, errors.New("status is required")
	}
	status, err := toStatus(*raw.Status)
	if err != nil {
		return Request{}, err
	}
	service := pick(raw.Service, defaultService)
	if service == "" {
		return Request{}, errors.New("service is required")
	}
	r := Request{
		Time:    ts,
		Service: service,
		Path:    NormalizePath(raw.Path),
		Status:  status,
		LineKey: lineKey(service, line),
	}
	if raw.RequestID != "" {
		r.DedupKey = "id:" + service + ":" + raw.RequestID
	}
	if raw.LatencyMS != nil {
		if *raw.LatencyMS < 0 {
			return Request{}, errors.New("latency_ms must not be negative")
		}
		r.LatencyMS, r.HasLatency = *raw.LatencyMS, true
	}
	return r, nil
}

// ParseProbe parses one health probe result.
// If status is missing or 0: ok=true means 200, ok=false means no response (status 0).
// If status is present it wins over the ok flag.
func ParseProbe(line, defaultService string) (Request, error) {
	var raw rawProbe
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Request{}, errors.New("invalid json")
	}
	ts, err := parseTS(raw.TS)
	if err != nil {
		return Request{}, err
	}
	service := pick(raw.Service, defaultService)
	if service == "" {
		return Request{}, errors.New("service is required")
	}
	if raw.Target == "" {
		return Request{}, errors.New("target is required")
	}
	if raw.OK == nil {
		return Request{}, errors.New("ok is required")
	}
	status := 0
	if raw.Status != nil && *raw.Status != 0 {
		if status, err = toStatus(*raw.Status); err != nil {
			return Request{}, err
		}
	} else if *raw.OK {
		status = 200
	}
	r := Request{
		Time:    ts,
		Service: service,
		Path:    NormalizePath(raw.Target),
		Status:  status,
		LineKey: lineKey(service, line),
	}
	if raw.LatencyMS != nil {
		if *raw.LatencyMS < 0 {
			return Request{}, errors.New("latency_ms must not be negative")
		}
		r.LatencyMS, r.HasLatency = *raw.LatencyMS, true
	}
	return r, nil
}
