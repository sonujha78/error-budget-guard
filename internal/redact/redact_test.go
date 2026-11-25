package redact

import "testing"

var cases = []struct{ in, want string }{
	{"GET /pay?token=tok_SECRET123&x=1", "GET /pay?token=[redacted]&x=1"},
	{"from 203.0.113.7 to 198.51.100.9", "from [ip] to [ip]"},
	{"client 203.0.113.7:9100 ok", "client [ip]:9100 ok"},
	{"Cookie: session=SESS999; a=b", "Cookie: [redacted]"},
	{"user_id=4242 and uid=77", "user_id=[redacted] and uid=[redacted]"},
	{"user-4242 logged in", "[user] logged in"},
	{"Authorization: Bearer abc.def-ghi", "Authorization: Bearer [redacted]"},
	{"mail alice@example.com now", "mail [email] now"},
	{"key tok_SECRET123 here", "key [token] here"},
	{"x-api-key=abc123", "x-api-key=[redacted]"},
	{"fe80::1 and 2001:db8::8a2e:370:7334", "[ip] and [ip]"},
	{"full 2001:0db8:0000:0000:0000:ff00:0042:8329", "full [ip]"},
	{"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc_DEF-1 end", "jwt [token] end"},
}

func TestScrub(t *testing.T) {
	for _, c := range cases {
		if got := Scrub(c.in); got != c.want {
			t.Errorf("Scrub(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestScrubIsIdempotent(t *testing.T) {
	for _, c := range cases {
		once := Scrub(c.in)
		if twice := Scrub(once); twice != once {
			t.Errorf("Scrub is not idempotent for %q: %q then %q", c.in, once, twice)
		}
	}
}

func TestScrubLeavesNormalReportTextAlone(t *testing.T) {
	for _, s := range []string{
		"2026-11-25T05:51:29.950999975Z",
		"burn 14.4x over 1h0m0s and 0.0x over 5s (fires when both >= 14.4x)",
		"slo demo-availability (objective 99.9%, window 12h0m0s, scale=0.0166667)",
		"| /orders/:id | 12 | 40.0% |",
		"history covers 30.4% of the 6m0s window, need at least 80%",
		"malformed=0 requests=931 events=931 no_latency=0",
		"line 6: unrecognised nginx line format",
		"at 10:02:29 and go1.22.5",
		"[block] checkout-availability: 3.0% of the error budget remains (97.0 of 100.0 events used)",
	} {
		if got := Scrub(s); got != s {
			t.Errorf("normal text was changed: %q -> %q", s, got)
		}
	}
}

func TestScrubCountAndDirty(t *testing.T) {
	out, n := ScrubCount("a token=x b 1.2.3.4 c 5.6.7.8")
	if n != 3 || out != "a token=[redacted] b [ip] c [ip]" {
		t.Errorf("out=%q n=%d, want 3 replacements", out, n)
	}
	if _, n2 := ScrubCount(out); n2 != 0 {
		t.Errorf("already scrubbed text must count 0, got %d", n2)
	}
	if !Dirty("ip 1.2.3.4") || Dirty("nothing secret here") || Dirty(out) {
		t.Error("Dirty gave a wrong answer")
	}
}
