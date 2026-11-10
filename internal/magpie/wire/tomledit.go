package wire

import (
	"strconv"
	"strings"
)

// tomlDoc edits a TOML document line by line. Only the lines of keys that
// the wirer manages change; comments, blank lines, ordering, and unrelated
// keys and tables stay byte for byte.
type tomlDoc struct {
	lines []string
	// trailingNewline records whether the input ended with "\n".
	trailingNewline bool
}

type tomlEntry struct {
	table      string // "" for the root table
	key        string
	start, end int // line range, inclusive
}

type tomlHeader struct {
	table string
	array bool
	line  int
}

func parseTOML(data []byte) *tomlDoc {
	s := string(data)
	d := &tomlDoc{trailingNewline: s == "" || strings.HasSuffix(s, "\n")}
	s = strings.TrimSuffix(s, "\n")
	if s != "" {
		d.lines = strings.Split(s, "\n")
	}
	return d
}

func (d *tomlDoc) bytes() []byte {
	out := strings.Join(d.lines, "\n")
	if d.trailingNewline || out != "" {
		out += "\n"
	}
	return []byte(out)
}

// scan returns the table headers and key entries of the document.
func (d *tomlDoc) scan() ([]tomlHeader, []tomlEntry) {
	var (
		headers []tomlHeader
		entries []tomlEntry
		table   string
	)
	for i := 0; i < len(d.lines); i++ {
		line := strings.TrimSpace(d.lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, array := parseHeader(line)
			table = name
			headers = append(headers, tomlHeader{table: name, array: array, line: i})
			continue
		}
		key, value, ok := splitKeyValue(d.lines[i])
		if !ok {
			continue
		}
		end := i
		for state := valueState(value, valueScan{}); state.open() && end+1 < len(d.lines); {
			end++
			state = valueState(d.lines[end], state)
		}
		entries = append(entries, tomlEntry{table: table, key: key, start: i, end: end})
		i = end
	}
	return headers, entries
}

// parseHeader returns the normalized table name of "[a.b]" or "[[a.b]]".
func parseHeader(line string) (string, bool) {
	array := strings.HasPrefix(line, "[[")
	inner := strings.TrimPrefix(line, "[")
	if array {
		inner = strings.TrimPrefix(inner, "[")
	}
	if i := closingBracket(inner); i >= 0 {
		inner = inner[:i]
	}
	return normalizeKey(inner), array
}

func closingBracket(s string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ']':
			return i
		}
	}
	return -1
}

// normalizeKey turns a dotted key with optional quotes and spaces into
// plain dot-separated segments.
func normalizeKey(k string) string {
	var (
		parts []string
		cur   strings.Builder
		quote byte
	)
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		case c == ' ' || c == '\t':
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	return strings.Join(parts, ".")
}

// splitKeyValue splits "key = value" at the first "=" outside quotes.
func splitKeyValue(line string) (key, value string, ok bool) {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return "", "", false
		case c == '=':
			k := normalizeKey(line[:i])
			if k == "" {
				return "", "", false
			}
			return k, line[i+1:], true
		}
	}
	return "", "", false
}

// valueScan tracks multi-line strings and arrays across lines.
type valueScan struct {
	depth     int
	multiline string // `"""` or `'''` while inside a multi-line string
}

func (v valueScan) open() bool { return v.depth > 0 || v.multiline != "" }

func valueState(s string, st valueScan) valueScan {
	for i := 0; i < len(s); i++ {
		if st.multiline != "" {
			if strings.HasPrefix(s[i:], st.multiline) {
				i += 2
				st.multiline = ""
			}
			continue
		}
		switch c := s[i]; {
		case strings.HasPrefix(s[i:], `"""`) || strings.HasPrefix(s[i:], `'''`):
			st.multiline = s[i : i+3]
			i += 2
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if c == '"' && s[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case c == '#':
			return st
		case c == '[' || c == '{':
			st.depth++
		case c == ']' || c == '}':
			if st.depth > 0 {
				st.depth--
			}
		}
	}
	return st
}

// trailingComment returns the " # comment" suffix of a single-line value.
func trailingComment(value string) string {
	var quote byte
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			j := i
			for j > 0 && (value[j-1] == ' ' || value[j-1] == '\t') {
				j--
			}
			return value[j:]
		}
	}
	return ""
}

func (d *tomlDoc) find(table, key string) (tomlEntry, bool) {
	_, entries := d.scan()
	for _, e := range entries {
		if e.table == table && e.key == key {
			return e, true
		}
	}
	return tomlEntry{}, false
}

// getString returns the string value of key in table.
func (d *tomlDoc) getString(table, key string) (string, bool) {
	e, ok := d.find(table, key)
	if !ok || e.start != e.end {
		return "", false
	}
	_, value, _ := splitKeyValue(d.lines[e.start])
	value = strings.TrimSpace(value)
	value = strings.TrimSpace(strings.TrimSuffix(value, trailingComment(value)))
	switch {
	case strings.HasPrefix(value, `"`):
		s, err := strconv.Unquote(value)
		return s, err == nil
	case strings.HasPrefix(value, `'`) && strings.HasSuffix(value, `'`) && len(value) >= 2:
		return value[1 : len(value)-1], true
	}
	return "", false
}

// set writes `key = value` (value already TOML encoded) into table ("" is
// the root table), replacing an existing entry or adding one.
func (d *tomlDoc) set(table, key, value string) {
	headers, entries := d.scan()
	line := quoteKey(key) + " = " + value
	for _, e := range entries {
		if e.table != table || e.key != key {
			continue
		}
		orig := d.lines[e.start]
		indent := orig[:len(orig)-len(strings.TrimLeft(orig, " \t"))]
		if e.start == e.end {
			_, v, _ := splitKeyValue(orig)
			line += trailingComment(v)
		}
		d.replace(e.start, e.end, indent+line)
		return
	}
	last := -1
	for _, e := range entries {
		if e.table == table {
			last = e.end
		}
	}
	if last >= 0 {
		d.insert(last+1, line)
		return
	}
	if table == "" {
		d.insertRoot(headers, line)
		return
	}
	for _, h := range headers {
		if h.table == table && !h.array {
			d.insert(h.line+1, line)
			return
		}
	}
	if n := len(d.lines); n > 0 && strings.TrimSpace(d.lines[n-1]) != "" {
		d.lines = append(d.lines, "")
	}
	d.lines = append(d.lines, "["+quoteTable(table)+"]", line)
}

// insertRoot adds a root key before the first table, after any leading
// comment block of the file.
func (d *tomlDoc) insertRoot(headers []tomlHeader, line string) {
	if len(headers) == 0 {
		d.lines = append(d.lines, line)
		return
	}
	at := 0
	for at < len(d.lines) && strings.HasPrefix(strings.TrimSpace(d.lines[at]), "#") {
		at++
	}
	if at > 0 && at < len(d.lines) && strings.TrimSpace(d.lines[at]) == "" {
		at++
	} else if at >= headers[0].line {
		at = 0
	}
	d.insert(at, line)
	if strings.HasPrefix(strings.TrimSpace(d.lines[at+1]), "[") {
		d.insert(at+1, "")
	}
}

func (d *tomlDoc) insert(at int, line string) {
	d.lines = append(d.lines, "")
	copy(d.lines[at+1:], d.lines[at:])
	d.lines[at] = line
}

func (d *tomlDoc) replace(start, end int, line string) {
	out := append([]string{}, d.lines[:start]...)
	out = append(out, line)
	d.lines = append(out, d.lines[end+1:]...)
}

func isBareKey(k string) bool {
	if k == "" {
		return false
	}
	for _, c := range k {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func quoteKey(k string) string {
	if isBareKey(k) {
		return k
	}
	return tomlString(k)
}

func quoteTable(t string) string {
	parts := strings.Split(t, ".")
	for i, p := range parts {
		parts[i] = quoteKey(p)
	}
	return strings.Join(parts, ".")
}

// tomlString encodes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(`\u` + strings.ToUpper(strconv.FormatInt(int64(r)|0x10000, 16)[1:]))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
