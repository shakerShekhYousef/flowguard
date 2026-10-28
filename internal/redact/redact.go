// Package redact removes credentials and other sensitive values from text
// before it reaches terminal output or reports.
//
// It is deliberately conservative: it keeps what helps troubleshooting
// (scheme, host, port, path, query parameter names, error kinds) and
// replaces values that commonly carry secrets with Placeholder.
package redact

import (
	"net/url"
	"regexp"
)

// Placeholder replaces every redacted value.
const Placeholder = "REDACTED"

// URL returns raw with userinfo, query values and fragment redacted. Query
// parameter names are kept. A URL that cannot be parsed is replaced whole.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[" + Placeholder + " URL]"
	}

	c := *u
	if c.User != nil {
		// Usernames can be tokens too (e.g. https://<token>@host).
		c.User = url.User(Placeholder)
	}
	if c.Opaque != "" {
		c.Opaque = Placeholder
	}
	if c.RawQuery != "" {
		q := c.Query()
		for k := range q {
			q[k] = []string{Placeholder}
		}
		c.RawQuery = q.Encode()
		c.ForceQuery = false
	}
	c.Fragment, c.RawFragment = "", ""
	return c.String()
}

// sensitiveWords are name fragments that mark a value as secret.
const sensitiveWords = `authorization|token|secret|password|passwd|api[_\-]?key|cookie|session|signature|credential`

var (
	urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"'<>]+`)

	// name=value, name: value, "name":"value" where name looks sensitive.
	// An auth scheme prefix ("Bearer x") is consumed with the value.
	sensitivePairPattern = regexp.MustCompile(
		`(?i)([\w\-]*(?:` + sensitiveWords + `)[\w\-]*)` +
			`("?\s*[=:]\s*"?)` +
			`(?:(?:bearer|basic|token)\s+)?[^\s"&,;]+`)

	sensitiveNamePattern = regexp.MustCompile(`(?i)` + sensitiveWords)

	authSchemePattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9\-._~+/]+=*`)
)

// Text redacts URLs (via URL), sensitive name/value pairs and bare
// Bearer/Basic credentials found anywhere in s.
func Text(s string) string {
	s = urlPattern.ReplaceAllStringFunc(s, URL)
	s = sensitivePairPattern.ReplaceAllString(s, "${1}${2}"+Placeholder)
	s = authSchemePattern.ReplaceAllString(s, "${1} "+Placeholder)
	return s
}

// SensitiveName reports whether a field or header name (e.g. api_key,
// X-Webhook-Signature, refresh_token) suggests its value is secret.
func SensitiveName(name string) bool {
	return sensitiveNamePattern.MatchString(name)
}

// Error returns err's message passed through Text, or "" for nil.
func Error(err error) string {
	if err == nil {
		return ""
	}
	return Text(err.Error())
}
