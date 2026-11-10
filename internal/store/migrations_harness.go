package store

func init() {
	registerMigration(Migration{ID: "0200_magpie_secrets", SQL: magpieSecretsSchema})
}

// magpieSecretsSchema stores secrets that the host magpie integration owns:
// the named gateway key of the VM and the key of the magpie admin web API.
// Values are AES-256-GCM encrypted with the master key.
const magpieSecretsSchema = `
CREATE TABLE magpie_secrets (
	tenant_id  TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	secret_enc BLOB    NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, name)
);
`
