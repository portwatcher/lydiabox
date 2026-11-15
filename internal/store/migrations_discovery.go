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
