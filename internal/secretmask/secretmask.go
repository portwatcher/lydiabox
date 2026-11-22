// Package secretmask replaces well-known credential formats in text that
// the host keeps or logs (session event copies, child process output), so
// that a token that a harness or a tool printed does not stay on the host.
// It recognizes formats by their fixed prefixes; it is a second line of
// defense, not a guarantee.
package secretmask

import "regexp"

// Mask is the replacement text.
const Mask = "[redacted]"

var patterns = regexp.MustCompile(alternatives(
	// Anthropic API keys and OAuth tokens (sk-ant-api03-, sk-ant-oat01-, sk-ant-ort01-).
	`sk-ant-[A-Za-z0-9_-]{16,}`,
	// OpenAI and compatible API keys (sk-..., sk-proj-...).
	`sk-[A-Za-z0-9_-]{20,}`,
	// Google OAuth access and refresh tokens, Google API keys.
	`ya29\.[A-Za-z0-9_-]{20,}`,
	`1//0[A-Za-z0-9_-]{20,}`,
	`AIza[0-9A-Za-z_-]{35}`,
	// GitHub tokens.
	`gh[pousr]_[A-Za-z0-9]{30,}`,
	`github_pat_[A-Za-z0-9_]{30,}`,
	// JSON Web Tokens (OAuth ID and access tokens).
	`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`,
	// The LydiaBox gateway key.
	`lbx-gw-[0-9a-f]{16,}`,
))

func alternatives(alts ...string) string {
	out := ""
	for i, a := range alts {
		if i > 0 {
			out += "|"
		}
		out += `\b(?:` + a + ")"
	}
	return out
}

// Bytes returns b with every recognized credential replaced by Mask. It
// returns b itself when nothing matches.
func Bytes(b []byte) []byte {
	if !patterns.Match(b) {
		return b
	}
	return patterns.ReplaceAll(b, []byte(Mask))
}

// String is Bytes for a string.
func String(s string) string {
	if !patterns.MatchString(s) {
		return s
	}
	return patterns.ReplaceAllString(s, Mask)
}
