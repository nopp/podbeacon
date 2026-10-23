// Package redact masks credential-shaped substrings before text leaves the cluster.
package redact

import "regexp"

var rules = []struct {
	re *regexp.Regexp
	to string
}{
	{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]+`), "[redigido]"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9\-._~+/]+=*`), "Bearer [redigido]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), "[redigido]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[redigido]"},
	{regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|access[_-]?key|authorization)\b(\s*[=:]\s*)("?)[^\s"]+`), "$1$2$3[redigido]"},
}

// String returns s with credential-shaped values replaced.
func String(s string) string {
	for _, rule := range rules {
		s = rule.re.ReplaceAllString(s, rule.to)
	}
	return s
}
