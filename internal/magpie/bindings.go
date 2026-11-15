package magpie

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/magpie/wire"
	"github.com/lydiabox/lydiabox/internal/store"
)

// Binding is the default model of one harness (docs/api.md section 10).
type Binding struct {
	Harness    string    `json:"harness"`
	Model      string    `json:"model"`
	ConfigPath string    `json:"configPath"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// BindingStore persists bindings (implemented by *store.Store).
type BindingStore interface {
	SetHarnessBinding(ctx context.Context, tenantID, harness, model string) error
	GetHarnessBinding(ctx context.Context, tenantID, harness string) (*store.HarnessBinding, error)
	ListHarnessBindings(ctx context.Context, tenantID string) ([]store.HarnessBinding, error)
	DeleteHarnessBinding(ctx context.Context, tenantID, harness string) error
}

// Bindings binds harnesses to models: it stores the choice and rewrites the
// harness config through its wirer.
type Bindings struct {
	Store BindingStore
	// Files reaches /home/agent (agentfs.Local in juicefs mode, remotefs in
	// provider mode).
	Files  agentfs.FS
	Wirers []wire.Wirer
	// GatewayURL is the gateway as seen from the guest (default
	// wire.DefaultGatewayURL).
	GatewayURL string
	MCPServers []wire.MCPServer
}

func (b *Bindings) wirer(harness string) (wire.Wirer, error) {
	ws := b.Wirers
	if ws == nil {
		ws = wire.All()
	}
	for _, w := range ws {
		if w.Harness() == harness {
			return w, nil
		}
	}
	return nil, fmt.Errorf("%w: unknown harness %q", ErrBadRequest, harness)
}

func (b *Bindings) binding(model string) wire.Binding {
	return wire.Binding{GatewayURL: b.GatewayURL, Model: model, MCPServers: b.MCPServers}
}

// List returns one binding per harness; Model is empty when unbound.
func (b *Bindings) List(ctx context.Context, tenantID string) ([]Binding, error) {
	stored, err := b.Store.ListHarnessBindings(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	byHarness := map[string]store.HarnessBinding{}
	for _, s := range stored {
		byHarness[s.Harness] = s
	}
	ws := b.Wirers
	if ws == nil {
		ws = wire.All()
	}
	out := make([]Binding, 0, len(ws))
	for _, w := range ws {
		s := byHarness[w.Harness()]
		out = append(out, Binding{Harness: w.Harness(), Model: s.Model, ConfigPath: w.ConfigPath(), UpdatedAt: s.UpdatedAt})
	}
	return out, nil
}

// Bind sets the model of harness and rewrites its config atomically. An
// empty model removes the stored binding and leaves the config model as is.
func (b *Bindings) Bind(ctx context.Context, tenantID, harness, model string) (Binding, error) {
	w, err := b.wirer(harness)
	if err != nil {
		return Binding{}, err
	}
	if model != "" && !ValidModelID(model) {
		return Binding{}, fmt.Errorf("%w: model %q is not a provider/model ID", ErrBadRequest, model)
	}
	if err := w.Apply(wire.FSHome(ctx, b.Files, tenantID), b.binding(model)); err != nil {
		if errors.Is(err, wire.ErrUnparseable) {
			return Binding{}, fmt.Errorf("%w: %v", ErrConflict, err)
		}
		if errors.Is(err, agentfs.ErrUnavailable) {
			return Binding{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return Binding{}, err
	}
	if model == "" {
		if err := b.Store.DeleteHarnessBinding(ctx, tenantID, harness); err != nil && !errors.Is(err, store.ErrNotFound) {
			return Binding{}, err
		}
		return Binding{Harness: harness, ConfigPath: w.ConfigPath()}, nil
	}
	if err := b.Store.SetHarnessBinding(ctx, tenantID, harness, model); err != nil {
		return Binding{}, err
	}
	got, err := b.Store.GetHarnessBinding(ctx, tenantID, harness)
	if err != nil {
		return Binding{}, err
	}
	return Binding{Harness: harness, Model: got.Model, ConfigPath: w.ConfigPath(), UpdatedAt: got.UpdatedAt}, nil
}

// EnsureWired points harness at the gateway with its stored model. It is
// called before every harness start, so a config the user edited inside
// the VM is repaired without losing their other settings.
func (b *Bindings) EnsureWired(ctx context.Context, tenantID, harness string) error {
	w, err := b.wirer(harness)
	if err != nil {
		return err
	}
	model := ""
	if s, err := b.Store.GetHarnessBinding(ctx, tenantID, harness); err == nil {
		model = s.Model
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return w.Apply(wire.FSHome(ctx, b.Files, tenantID), b.binding(model))
}

// WireAll runs EnsureWired for every harness and joins the errors.
func (b *Bindings) WireAll(ctx context.Context, tenantID string) error {
	ws := b.Wirers
	if ws == nil {
		ws = wire.All()
	}
	var errs []error
	for _, w := range ws {
		if err := b.EnsureWired(ctx, tenantID, w.Harness()); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", w.Harness(), err))
		}
	}
	return errors.Join(errs...)
}

// Env returns the environment a harness process needs to reach the gateway.
func (b *Bindings) Env(harness string) map[string]string {
	w, err := b.wirer(harness)
	if err != nil {
		return nil
	}
	return maps.Clone(w.Env(b.binding("")))
}
