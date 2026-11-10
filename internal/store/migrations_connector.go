package store

// Connector migrations (Phase 4, ID range 0300-0399).
func init() {
	registerMigration(Migration{ID: "0300_connector_status_and_clients", SQL: connectorSchema})
}

const connectorSchema = `
ALTER TABLE connector_accounts ADD COLUMN status TEXT NOT NULL DEFAULT 'ok';

CREATE TABLE connector_clients (
	tenant_id         TEXT    NOT NULL,
	provider          TEXT    NOT NULL,
	client_id         TEXT    NOT NULL,
	client_secret_enc BLOB,
	updated_at        INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, provider)
);
`
