package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Migration is one forward-only schema change.
//
// IDs are applied in lexical order and recorded by ID, so subsystems can add
// migrations in their own files without editing a shared list. Each phase
// owns an ID range to keep the order deterministic:
//
//	0001-0099  core (Phase 1: this file)
//	0100-0199  vmm (Phase 2)
//	0200-0299  magpie / harness (Phase 3)
//	0300-0399  connector (Phase 4)
//	0400-0499  web / desktop (Phase 5)
//	0500-0599  remote access (Cloudflare Zero Trust)
//
// To add one, create internal/store/migrations_<subsystem>.go with an init
// function that calls registerMigration, plus tenant-scoped accessor methods
// in internal/store/<subsystem>.go.
type Migration struct {
	ID  string
	SQL string
}

var (
	migrationsMu sync.Mutex
	migrations   = map[string]Migration{}
)

func registerMigration(m Migration) {
	migrationsMu.Lock()
	defer migrationsMu.Unlock()
	if _, dup := migrations[m.ID]; dup {
		panic("store: duplicate migration " + m.ID)
	}
	migrations[m.ID] = m
}

func sortedMigrations() []Migration {
	migrationsMu.Lock()
	defer migrationsMu.Unlock()
	out := make([]Migration, 0, len(migrations))
	for _, m := range migrations {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func init() {
	registerMigration(Migration{ID: "0001_core_schema", SQL: coreSchema})
}

const coreSchema = `
CREATE TABLE connector_accounts (
	tenant_id         TEXT    NOT NULL,
	provider          TEXT    NOT NULL,
	account_label     TEXT    NOT NULL,
	auth_kind         TEXT    NOT NULL DEFAULT 'oauth2',
	access_token_enc  BLOB,
	refresh_token_enc BLOB,
	token_type        TEXT    NOT NULL DEFAULT '',
	expires_at        INTEGER NOT NULL DEFAULT 0,
	scopes            TEXT    NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, provider, account_label)
);

CREATE TABLE harness_bindings (
	tenant_id  TEXT    NOT NULL,
	harness    TEXT    NOT NULL,
	model      TEXT    NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, harness)
);

CREATE TABLE audit_log (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	tenant_id  TEXT    NOT NULL,
	kind       TEXT    NOT NULL,
	detail     TEXT    NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX audit_log_tenant_created ON audit_log (tenant_id, created_at DESC);

CREATE TABLE sessions_meta (
	tenant_id     TEXT    NOT NULL,
	harness       TEXT    NOT NULL,
	session_id    TEXT    NOT NULL,
	workspace_dir TEXT    NOT NULL DEFAULT '',
	title         TEXT    NOT NULL DEFAULT '',
	model         TEXT    NOT NULL DEFAULT '',
	status        TEXT    NOT NULL DEFAULT '',
	extra_json    TEXT    NOT NULL DEFAULT '{}',
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, harness, session_id)
);
CREATE INDEX sessions_meta_tenant_updated ON sessions_meta (tenant_id, updated_at DESC);
`

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		id TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("store: read schema_migrations: %w", err)
		}
		applied[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read schema_migrations: %w", err)
	}

	for _, m := range sortedMigrations() {
		if applied[m.ID] {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration %s: %w", m.ID, err)
		}
		for _, stmt := range splitStatements(m.SQL) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("store: migration %s: %w", m.ID, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (id, applied_at) VALUES (?, ?)`, m.ID, s.timestamp()); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record migration %s: %w", m.ID, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %s: %w", m.ID, err)
		}
	}
	return nil
}

// splitStatements splits a migration on semicolons that end a line. Migration
// SQL must not contain triggers or string literals with ";\n".
func splitStatements(sqlText string) []string {
	var out []string
	for _, part := range strings.Split(sqlText, ";\n") {
		if p := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ";")); p != "" {
			out = append(out, p)
		}
	}
	return out
}
