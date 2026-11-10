package cli

import "context"

// wireVM wires Phase 2 (guest daemon and VMM drivers) into `lydiabox up`.
//
// Extension point: build internal/vmm (chv and docker drivers, auto-wake
// controller). Set d.VMStatus to the controller and return api.VMRoutes from
// internal/api/routes_vm.go.
// Returning a nil component registers nothing, which is the Phase 1 state.
func wireVM(ctx context.Context, d *deps) (*component, error) {
	return nil, nil
}
