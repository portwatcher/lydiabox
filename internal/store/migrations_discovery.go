package store

func init() {
	registerMigration(Migration{ID: "0600_session_discovery_cache", SQL: discoveryCacheSchema})
}

// discoveryCacheSchema keeps the last session discovery result per harness.
// In the provider storage mode the transcripts are on the machine disk, so
// the Studio session list reads this cache while the machine is paused or
// stopped.
const discoveryCacheSchema = `
CREATE TABLE session_discovery_cache (
	tenant_id    TEXT    NOT NULL,
	harness      TEXT    NOT NULL,
	payload_json TEXT    NOT NULL,
	updated_at   INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, harness)
);
`

func init() {
	registerMigration(Migration{ID: "0610_session_dismissed", SQL: sessionDismissedSchema})
}

// sessionDismissedSchema records the discovered harness sessions that the
// user deleted without their transcript: discovery would find them again in
// the transcript files, so the session list leaves them out until the
// transcript has newer activity.
const sessionDismissedSchema = `
CREATE TABLE session_dismissed (
	tenant_id    TEXT    NOT NULL,
	harness      TEXT    NOT NULL,
	session_id   TEXT    NOT NULL,
	dismissed_at INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, harness, session_id)
);
`

func init() {
	registerMigration(Migration{ID: "0620_session_events_cache", SQL: sessionEventsCacheSchema})
}

// sessionEventsCacheSchema keeps the last known events of a session (the
// conversation: prompts, replies, tool input and output), gzip-compressed
// JSON and bounded by MaxSessionEventsBytes. In the provider storage mode
// the transcripts are on the machine disk, so Studio shows this copy at
// once while the machine is paused or stopped. It holds no credentials.
const sessionEventsCacheSchema = `
CREATE TABLE session_events_cache (
	tenant_id   TEXT    NOT NULL,
	harness     TEXT    NOT NULL,
	session_id  TEXT    NOT NULL,
	events_gz   BLOB    NOT NULL,
	event_count INTEGER NOT NULL,
	cut_count   INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, harness, session_id)
);
`
