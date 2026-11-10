package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// object is a JSON object that keeps its key order and the raw bytes of
// every value it does not change, so a merge touches only managed keys.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObject() *object { return &object{vals: map[string]json.RawMessage{}} }

// parseObject parses a JSON object. Empty input is an empty object.
func parseObject(data []byte) (*object, error) {
	o := newObject()
	if len(bytes.TrimSpace(data)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("top-level value is not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected token %v", tok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.setRaw(key, raw)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return o, nil
}

func (o *object) setRaw(key string, raw json.RawMessage) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = raw
}

func (o *object) get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// getString returns a string value, or "" when missing or not a string.
func (o *object) getString(key string) string {
	raw, ok := o.vals[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func (o *object) set(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	o.setRaw(key, raw)
	return nil
}

func (o *object) remove(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// child returns the object stored at key, or a new one when key is missing
// or null. A non-object value is an error: the wirer will not replace data
// it does not understand.
func (o *object) child(key string) (*object, error) {
	raw, ok := o.vals[key]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return newObject(), nil
	}
	c, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("key %q: %w", key, err)
	}
	return c, nil
}

func (o *object) setChild(key string, c *object) error {
	raw, err := c.compact()
	if err != nil {
		return err
	}
	o.setRaw(key, raw)
	return nil
}

func (o *object) compact() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		if err := json.Compact(&b, o.vals[k]); err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal renders the object indented with indent and a final newline.
func (o *object) marshal(indent string) ([]byte, error) {
	raw, err := o.compact()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", indent); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// detectIndent returns the indentation unit of a JSON document (default two
// spaces) so a rewrite keeps the user's style.
func detectIndent(data []byte) string {
	for _, line := range strings.Split(string(data), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || len(trimmed) == len(line) {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return "  "
}

// editJSON applies fn to the JSON object stored in data and returns the new
// document. Changes are limited to what fn sets.
func editJSON(data []byte, fn func(o *object) error) ([]byte, error) {
	o, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnparseable, err)
	}
	if err := fn(o); err != nil {
		return nil, err
	}
	return o.marshal(detectIndent(data))
}
