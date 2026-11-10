package config

import "path/filepath"

// HostBrokerPort is the vsock port on which the guest reaches the host
// connector broker (git-cred and the connector MCP server).
const HostBrokerPort = 9001

// HostBrokerSocket is the UNIX socket on which the host serves the connector
// broker for the docker driver and for development without a VM
// ($DATA_DIR/run/host-9001.sock).
func (c *Config) HostBrokerSocket() string { return filepath.Join(c.RunDir(), "host-9001.sock") }

// EnvOAuthClient returns the OAuth client configured for provider through
// LYDIABOX_<PROVIDER>_CLIENT_ID / _CLIENT_SECRET or lydiabox.yaml. ok is
// false when no client ID is configured.
func (c *Config) EnvOAuthClient(provider string) (client OAuthClient, ok bool) {
	client, ok = c.OAuthClients[provider]
	return client, ok && client.ClientID != ""
}
