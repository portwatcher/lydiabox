package wire

import "strings"

// remove deletes the entry of key in table ("" is the root table). Other
// lines stay as they are.
func (d *tomlDoc) remove(table, key string) {
	e, ok := d.find(table, key)
	if !ok {
		return
	}
	d.lines = append(d.lines[:e.start], d.lines[e.end+1:]...)
}

// removeTable deletes the table header "[table]" and its lines up to the
// next header. A blank line before the header goes with it. Sub-tables
// ("[table.x]") stay.
func (d *tomlDoc) removeTable(table string) {
	headers, _ := d.scan()
	for i, h := range headers {
		if h.table != table || h.array {
			continue
		}
		end := len(d.lines)
		if i+1 < len(headers) {
			end = headers[i+1].line
		}
		// Comments and blank lines just before the next header belong to it.
		for end > h.line+1 && isTOMLTrivia(d.lines[end-1]) && i+1 < len(headers) {
			end--
		}
		start := h.line
		if start > 0 && strings.TrimSpace(d.lines[start-1]) == "" {
			start--
		}
		d.lines = append(d.lines[:start], d.lines[end:]...)
		return
	}
}

func isTOMLTrivia(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}
