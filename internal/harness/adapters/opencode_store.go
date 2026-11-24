package adapters

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/portwatcher/lydiabox/internal/harness/safefile"
)

// opencode keeps sessions in SQLite (~/.local/share/opencode/opencode.db,
// opencode 1.x) and, in older releases, as JSON files under
// ~/.local/share/opencode/storage/{session,message,part}. Both are read
// into the same in-memory shape.
const (
	ocDataDir = ".local/share/opencode"
	ocDB      = ocDataDir + "/opencode.db"
	ocStorage = ocDataDir + "/storage"
)

type ocSession struct {
	ID        string
	ParentID  string
	Directory string
	Title     string
	Created   int64
	Updated   int64
	Archived  bool
	// Rel are the legacy JSON files below the home directory.
	Rel    []string
	FromDB bool
}

type ocTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

type ocMessage struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	ModelID    string    `json:"modelID"`
	ProviderID string    `json:"providerID"`
	Cost       float64   `json:"cost"`
	Tokens     *ocTokens `json:"tokens"`
	Time       struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	// Finish and Error are set on assistant messages.
	Finish string          `json:"finish"`
	Error  json.RawMessage `json:"error"`
	Model  *struct {
		ProviderID string `json:"providerID"`
		ModelID    string `json:"modelID"`
	} `json:"model"`
	Parts []ocPart `json:"-"`
}

type ocPart struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	// Tool, CallID, and State are set on parts of type "tool".
	Tool   string `json:"tool"`
	CallID string `json:"callID"`
	State  struct {
		Status      string          `json:"status"`
		Input       json.RawMessage `json:"input"`
		Output      json.RawMessage `json:"output"`
		Error       string          `json:"error"`
		Attachments json.RawMessage `json:"attachments"`
		Time        struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
	} `json:"state"`
	// Tokens and Cost are set on parts of type "step-finish".
	Tokens *ocTokens `json:"tokens"`
	Cost   float64   `json:"cost"`
}

// openOCDB opens the opencode database read-only, or returns nil when it
// does not exist.
func openOCDB(home string, write bool) (*sql.DB, error) {
	p, err := safefile.Resolve(home, ocDB)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if _, err := statHome(home, ocDB); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	if !write {
		q.Set("mode", "ro")
	}
	db, err := sql.Open("sqlite", sqliteDSN(p, q))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// rowsAsMaps scans rows into maps keyed by column name, so the reader works
// across opencode schema versions that add columns.
func rowsAsMaps(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func colStr(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

func colInt(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func dbSessions(db *sql.DB) ([]ocSession, error) {
	rows, err := db.Query(`SELECT * FROM session`)
	if err != nil {
		return nil, err
	}
	ms, err := rowsAsMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]ocSession, 0, len(ms))
	for _, m := range ms {
		out = append(out, ocSession{ID: colStr(m, "id"), ParentID: colStr(m, "parent_id"), Directory: colStr(m, "directory"),
			Title: colStr(m, "title"), Created: colInt(m, "time_created"), Updated: colInt(m, "time_updated"),
			Archived: colInt(m, "time_archived") > 0, FromDB: true})
	}
	return out, nil
}

func dbMessages(db *sql.DB, sessionID string) ([]ocMessage, error) {
	rows, err := db.Query(`SELECT * FROM message WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	ms, err := rowsAsMaps(rows)
	if err != nil {
		return nil, err
	}
	msgs := make([]ocMessage, 0, len(ms))
	index := map[string]int{}
	for _, m := range ms {
		var msg ocMessage
		if err := json.Unmarshal([]byte(colStr(m, "data")), &msg); err != nil {
			continue
		}
		msg.ID = colStr(m, "id")
		if msg.Time.Created == 0 {
			msg.Time.Created = colInt(m, "time_created")
		}
		index[msg.ID] = len(msgs)
		msgs = append(msgs, msg)
	}
	rows, err = db.Query(`SELECT * FROM part WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	ps, err := rowsAsMaps(rows)
	if err != nil {
		return nil, err
	}
	sort.Slice(ps, func(i, j int) bool { return colStr(ps[i], "id") < colStr(ps[j], "id") })
	for _, m := range ps {
		var p ocPart
		if err := json.Unmarshal([]byte(colStr(m, "data")), &p); err != nil {
			continue
		}
		p.ID, p.MessageID = colStr(m, "id"), colStr(m, "message_id")
		if i, ok := index[p.MessageID]; ok {
			msgs[i].Parts = append(msgs[i].Parts, p)
		}
	}
	sortMessages(msgs)
	return msgs, nil
}

func sortMessages(msgs []ocMessage) {
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].Time.Created != msgs[j].Time.Created {
			return msgs[i].Time.Created < msgs[j].Time.Created
		}
		return msgs[i].ID < msgs[j].ID
	})
}

func readJSONFile[T any](home, rel string) (T, error) {
	var v T
	b, err := readHomeFile(home, rel)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(b, &v)
}

func fileSessions(home string) ([]ocSession, error) {
	files, err := globHome(home, ocStorage+"/session/*/*.json")
	if err != nil {
		return nil, err
	}
	var out []ocSession
	for _, rel := range files {
		v, err := readJSONFile[struct {
			ID        string `json:"id"`
			ParentID  string `json:"parentID"`
			Directory string `json:"directory"`
			Title     string `json:"title"`
			Time      struct {
				Created  int64 `json:"created"`
				Updated  int64 `json:"updated"`
				Archived int64 `json:"archived"`
			} `json:"time"`
		}](home, rel)
		if err != nil || v.ID == "" {
			continue
		}
		out = append(out, ocSession{ID: v.ID, ParentID: v.ParentID, Directory: v.Directory, Title: v.Title,
			Created: v.Time.Created, Updated: v.Time.Updated, Archived: v.Time.Archived > 0,
			Rel: []string{rel}})
	}
	return out, nil
}

func fileMessages(home, sessionID string) ([]ocMessage, error) {
	files, err := globHome(home, ocStorage+"/message/"+sessionID+"/*.json")
	if err != nil {
		return nil, err
	}
	var msgs []ocMessage
	for _, rel := range files {
		m, err := readJSONFile[ocMessage](home, rel)
		if err != nil || m.ID == "" {
			continue
		}
		parts, _ := globHome(home, ocStorage+"/part/"+m.ID+"/*.json")
		sort.Strings(parts)
		for _, prel := range parts {
			if p, err := readJSONFile[ocPart](home, prel); err == nil {
				m.Parts = append(m.Parts, p)
			}
		}
		msgs = append(msgs, m)
	}
	sortMessages(msgs)
	return msgs, nil
}

// ocAll returns the top-level sessions of both stores (SQLite wins).
func ocAll(home string) ([]ocSession, *sql.DB, error) {
	db, err := openOCDB(home, false)
	if err != nil {
		return nil, nil, err
	}
	var all []ocSession
	seen := map[string]bool{}
	if db != nil {
		ss, err := dbSessions(db)
		if err != nil {
			db.Close()
			return nil, nil, fmt.Errorf("read %s: %w", ocDB, err)
		}
		for _, s := range ss {
			seen[s.ID] = true
			all = append(all, s)
		}
	}
	fs, err := fileSessions(home)
	if err != nil {
		if db != nil {
			db.Close()
		}
		return nil, nil, err
	}
	for _, s := range fs {
		if !seen[s.ID] {
			all = append(all, s)
		}
	}
	return all, db, nil
}

func ocMessagesOf(home string, db *sql.DB, s ocSession) ([]ocMessage, error) {
	if s.FromDB {
		return dbMessages(db, s.ID)
	}
	return fileMessages(home, s.ID)
}

// purgeOC deletes a session from the store that holds it.
func purgeOC(home string, s ocSession, msgs []ocMessage) error {
	if !s.FromDB {
		var errs []error
		for _, rel := range s.Rel {
			errs = append(errs, safefile.RemoveAll(home, rel))
		}
		errs = append(errs, safefile.RemoveAll(home, path.Join(ocStorage, "message", s.ID)))
		for _, m := range msgs {
			errs = append(errs, safefile.RemoveAll(home, path.Join(ocStorage, "part", m.ID)))
		}
		errs = append(errs, safefile.RemoveAll(home, path.Join(ocStorage, "session_diff", s.ID+".json")))
		return errors.Join(errs...)
	}
	db, err := openOCDB(home, true)
	if err != nil || db == nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM part WHERE session_id = ?`, `DELETE FROM message WHERE session_id = ?`, `DELETE FROM session WHERE id = ?`} {
		if _, err := tx.Exec(q, s.ID); err != nil && !strings.Contains(err.Error(), "no such table") {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return safefile.RemoveAll(home, path.Join(ocStorage, "session_diff", s.ID+".json"))
}
