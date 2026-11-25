// Package redact removes secrets and personal data from text that is stored or printed.
//
// The parsers already avoid copying IPs, user ids and cookies. This package is the second
// line of defence for free text (override reasons, titles, warnings) and for the final report.
// Known limitation: an IPv6 address that starts with "::" (like ::1) is not recognised.
package redact

import "regexp"

type rule struct {
	re   *regexp.Regexp
	repl string
}

const secretKeys = `token|access_token|refresh_token|id_token|api_key|apikey|api-key|key|secret|client_secret|password|passwd|pwd|auth|authorization|sid|session|sessionid|session_id|jwt|signature`
const userKeys = `user_id|userid|user-id|uid|user|username|email|login|account|account_id`
const hex4 = `[0-9a-fA-F]{1,4}`

// Order matters: the broad rules (cookie, bearer) run before the narrow ones.
var rules = []rule{
	{regexp.MustCompile(`(?i)\b(set-cookie|cookie)\s*[:=][^\r\n]*`), "${1}: [redacted]"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`), "Bearer [redacted]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`), "[token]"},
	{regexp.MustCompile(`(?i)\b(` + secretKeys + `)=[^\s&"'#;,]+`), "${1}=[redacted]"},
	{regexp.MustCompile(`(?i)\b(` + userKeys + `)=[^\s&"'#;,]+`), "${1}=[redacted]"},
	{regexp.MustCompile(`\b(?:tok|sk|pk|ghp|gho|ghs|xox[abp])[_-][A-Za-z0-9_-]{6,}`), "[token]"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[email]"},
	{regexp.MustCompile(`(?i)\buser[-_:]\d+\b`), "[user]"},
	{regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`), "[ip]"},
	{regexp.MustCompile(`\b(?:` + hex4 + `:){7}` + hex4 + `\b|\b` + hex4 + `(?::` + hex4 + `)*::(?:` + hex4 + `(?::` + hex4 + `)*)?`), "[ip]"},
}

// ScrubCount returns the cleaned text and how many values were replaced.
// Text that is already clean gives the same text and 0, so scrubbing twice is safe.
func ScrubCount(s string) (string, int) {
	n := 0
	for _, r := range rules {
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			out := r.re.ReplaceAllString(m, r.repl)
			if out != m {
				n++
			}
			return out
		})
	}
	return s, n
}

// Scrub returns s without secrets or personal data.
func Scrub(s string) string {
	out, _ := ScrubCount(s)
	return out
}

// Dirty reports whether Scrub would change s.
func Dirty(s string) bool { return Scrub(s) != s }
