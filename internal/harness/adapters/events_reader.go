package adapters

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
)

// lineParser turns the JSONL lines of one transcript into events. It keeps
// its state between reads, so a growing file is parsed only once.
type lineParser interface {
	line(b []byte)
	log() *eventLog
}

// EventReader reads the typed events of sessions of all five harnesses.
// It keeps the parse state of the recently read sessions: an appended
// JSONL transcript is parsed from where the last read stopped, and a
// transcript that did not change is not read again.
type EventReader struct {
	mu      sync.Mutex
	entries map[string]*eventEntry
}

var _ harness.EventReader = (*EventReader)(nil)

// eventCacheSize bounds the sessions whose parse state is kept.
const eventCacheSize = 16

// headLen is the prefix of a JSONL file that tells a rewrite from an append.
const headLen = 256

type eventEntry struct {
	rel    string
	stamp  string
	offset int
	head   []byte
	parser lineParser
	list   []harness.Event
	used   time.Time
}

// NewEventReader returns an empty reader.
func NewEventReader() *EventReader { return &EventReader{entries: map[string]*eventEntry{}} }

// Events implements harness.EventReader.
func (r *EventReader) Events(home, h, id string) ([]harness.Event, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := home + "\x00" + h + "\x00" + id
	e := r.entries[key]
	if e == nil {
		e = &eventEntry{}
	}
	var err error
	switch h {
	case harness.ClaudeCode:
		err = r.jsonl(home, e, Claude{}.find, id, func() lineParser { return newClaudeEvents() })
	case harness.Codex:
		err = r.jsonl(home, e, Codex{}.find, id, func() lineParser { return newCodexEvents() })
	case harness.DSH:
		err = r.jsonl(home, e, DSH{}.find, id, func() lineParser { return newDSHEvents() })
	case harness.OpenCode:
		err = r.whole(e, ocStamp(home), func() ([]harness.Event, error) { return openCodeEvents(home, id) })
	case harness.Antigravity:
		err = r.whole(e, agyStamp(home, id), func() ([]harness.Event, error) { return antigravityEvents(home, id) })
	default:
		return nil, fmt.Errorf("%w: unknown harness %q", harness.ErrNotFound, h)
	}
	if err != nil {
		delete(r.entries, key)
		return nil, err
	}
	e.used = time.Now()
	r.entries[key] = e
	r.evict()
	return e.list, nil
}

func (r *EventReader) evict() {
	for len(r.entries) > eventCacheSize {
		var oldest string
		for k, e := range r.entries {
			if oldest == "" || e.used.Before(r.entries[oldest].used) {
				oldest = k
			}
		}
		delete(r.entries, oldest)
	}
}

func fileStamp(fi fs.FileInfo) string {
	return strconv.FormatInt(fi.Size(), 10) + "@" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

// jsonl reads a JSONL transcript from where the last read stopped.
func (r *EventReader) jsonl(home string, e *eventEntry, find func(home, id string) (string, error), id string, parser func() lineParser) error {
	var fi fs.FileInfo
	var err error
	if e.rel != "" {
		fi, err = statHome(home, e.rel)
	}
	if e.rel == "" || err != nil {
		rel, ferr := find(home, id)
		if ferr != nil {
			return ferr
		}
		if fi, err = statHome(home, rel); err != nil {
			return err
		}
		*e = eventEntry{rel: rel}
	}
	stamp := fileStamp(fi)
	if e.parser != nil && stamp == e.stamp {
		return nil
	}
	compressed := strings.HasSuffix(e.rel, ".zstd") || strings.HasSuffix(e.rel, ".zst")
	var data []byte
	if compressed {
		data, err = readDSH(home, e.rel)
	} else {
		data, err = readHomeFile(home, e.rel)
	}
	if err != nil {
		return err
	}
	if e.parser == nil || compressed || len(data) < e.offset || !bytes.HasPrefix(data, e.head) {
		e.parser, e.offset = parser(), 0
	}
	if end := bytes.LastIndexByte(data[e.offset:], '\n'); end >= 0 {
		_ = eachLine(bytes.NewReader(data[e.offset:e.offset+end+1]), func(b []byte) error {
			e.parser.line(b)
			return nil
		})
		e.offset += end + 1
	}
	e.head = append([]byte(nil), data[:min(headLen, len(data))]...)
	e.stamp = stamp
	e.list = e.parser.log().events()
	return nil
}

// whole reads a session store again when its stamp changed. An empty
// stamp (unknown) always reads it.
func (r *EventReader) whole(e *eventEntry, stamp string, read func() ([]harness.Event, error)) error {
	if stamp != "" && e.list != nil && stamp == e.stamp {
		return nil
	}
	list, err := read()
	if err != nil {
		return err
	}
	e.stamp, e.list = stamp, list
	return nil
}

// stampOf joins the stamps of the files that exist among rels; it is empty
// when none exists.
func stampOf(home string, rels ...string) string {
	var parts []string
	for _, rel := range rels {
		if fi, err := statHome(home, rel); err == nil {
			parts = append(parts, rel+"="+fileStamp(fi))
		}
	}
	return strings.Join(parts, ";")
}

func ocStamp(home string) string {
	if !strings.Contains(stampOf(home, ocDB), "=") {
		return "" // legacy JSON storage: no single file to watch
	}
	return stampOf(home, ocDB, ocDB+"-wal")
}

func agyStamp(home, id string) string {
	db := path.Join(agyRoot, "conversations", id+".db")
	return stampOf(home, db, db+"-wal", path.Join(agyRoot, "brain", id, "task.md"))
}
