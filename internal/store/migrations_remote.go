package store

func init() {
	registerMigration(Migration{ID: "0500_remote_access", SQL: remoteAccessSchema})
}

// remoteAccessSchema stores the remote access setup of each provider
// (`lydiabox cloudflare setup`): the non-secret state as JSON, and the
// secrets (tunnel token, service token secret) AES-256-GCM encrypted with
// the master key.
const remoteAccessSchema = `
CREATE TABLE remote_access (
	tenant_id   TEXT    NOT NULL,
	provider    TEXT    NOT NULL,
	state_json  TEXT    NOT NULL,
	secrets_enc BLOB,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, provider)
);
`
