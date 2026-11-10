package cli

import "context"

// wireDesktop wires Phase 2 and Phase 5 (desktop streaming) into `lydiabox up`.
//
// Extension point: build internal/desktop (WebRTC signaling relay and
// takeover coordinator). Return api.DesktopRoutes.
// Returning a nil component registers nothing, which is the Phase 1 state.
func wireDesktop(ctx context.Context, d *deps) (*component, error) {
	return nil, nil
}
