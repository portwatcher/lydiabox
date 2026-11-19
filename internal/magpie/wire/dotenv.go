package wire

import (
	"regexp"
	"strconv"
	"strings"
)

// envDoc edits a .env file line by line, keeping comments, blank lines,
// "export" prefixes, and unrelated variables.
type envDoc struct {
	lines           []string
	trailingNewline bool
}

var envLine = regexp.MustCompile(`^(\s*(?:export\s+)?)([A-Za-z_][A-Za-z0-9_]*)(\s*=\s*)(.*)$`)

func parseEnv(data []byte) *envDoc {
	s := string(data)
	d := &envDoc{trailingNewline: s == "" || strings.HasSuffix(s, "\n")}
	s = strings.TrimSuffix(s, "\n")
	if s != "" {
		d.lines = strings.Split(s, "\n")
	}
	return d
}

func (d *envDoc) bytes() []byte {
	out := strings.Join(d.lines, "\n")
	if d.trailingNewline || out != "" {
		out += "\n"
	}
	return []byte(out)
}

// get returns the unquoted value of the last assignment of key.
func (d *envDoc) get(key string) (string, bool) {
	val, found := "", false
	for _, l := range d.lines {
		m := envLine.FindStringSubmatch(l)
		if m == nil || m[2] != key {
			continue
		}
		val, found = unquoteEnv(m[4]), true
	}
	return val, found
}

// del removes every assignment of key and reports whether there was one.
func (d *envDoc) del(key string) bool {
	kept := d.lines[:0]
	found := false
	for _, l := range d.lines {
		if m := envLine.FindStringSubmatch(l); m != nil && m[2] == key {
			found = true
			continue
		}
		kept = append(kept, l)
	}
	d.lines = kept
	return found
}

// set assigns key, rewriting every existing assignment line (so the last
// one wins with the managed value) or appending a new line.
func (d *envDoc) set(key, value string) {
	encoded := quoteEnv(value)
	done := false
	for i, l := range d.lines {
		m := envLine.FindStringSubmatch(l)
		if m == nil || m[2] != key {
			continue
		}
		d.lines[i] = m[1] + m[2] + m[3] + encoded + envComment(m[4])
		done = true
	}
	if !done {
		d.lines = append(d.lines, key+"="+encoded)
	}
}

func unquoteEnv(v string) string {
	v = strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(v, `"`):
		if end := closingQuote(v, '"'); end > 0 {
			if s, err := strconv.Unquote(v[:end+1]); err == nil {
				return s
			}
			return v[1:end]
		}
	case strings.HasPrefix(v, `'`):
		if end := closingQuote(v, '\''); end > 0 {
			return v[1:end]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func closingQuote(v string, q byte) int {
	for i := 1; i < len(v); i++ {
		if v[i] == '\\' && q == '"' {
			i++
			continue
		}
		if v[i] == q {
			return i
		}
	}
	return -1
}

// envComment returns the trailing " # comment" of an existing value.
func envComment(v string) string {
	t := strings.TrimSpace(v)
	rest := t
	switch {
	case strings.HasPrefix(t, `"`):
		if end := closingQuote(t, '"'); end > 0 {
			rest = t[end+1:]
		}
	case strings.HasPrefix(t, `'`):
		if end := closingQuote(t, '\''); end > 0 {
			rest = t[end+1:]
		}
	}
	if i := strings.Index(rest, " #"); i >= 0 {
		return rest[i:]
	}
	if strings.HasPrefix(rest, "#") && rest != t {
		return " " + rest
	}
	return ""
}

func quoteEnv(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t#\"'$`\\\n") {
		return v
	}
	return strconv.Quote(v)
}
