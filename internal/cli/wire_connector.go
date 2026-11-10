package cli

import "context"

// wireConnector wires Phase 4 (connectors) into `lydiabox up`.
//
// Extension point: build internal/connector (OAuth2/PAT broker, git-cred and
// MCP over vsock). Return api.ConnectorRoutes.
// Returning a nil component registers nothing, which is the Phase 1 state.
func wireConnector(ctx context.Context, d *deps) (*component, error) {
	return nil, nil
}
