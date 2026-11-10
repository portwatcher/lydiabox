package cli

import "context"

// wireHarness wires Phase 3 (magpie and harness bridge) into `lydiabox up`.
//
// Extension point: build internal/magpie and internal/harness. Return
// api.ModelsRoutes and api.SessionsRoutes; start and stop the magpie
// supervisor through Start and Stop.
// Returning a nil component registers nothing, which is the Phase 1 state.
func wireHarness(ctx context.Context, d *deps) (*component, error) {
	return nil, nil
}
