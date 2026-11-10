// Package magpietest provides an in-memory magpie admin API for tests of
// the magpie package and of the models routes.
package magpietest

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/lydiabox/lydiabox/internal/magpie"
)

// FakeAdmin is an in-memory magpie.AdminAPI.
type FakeAdmin struct {
	mu        sync.Mutex
	Down      bool
	Version   string
	Providers []magpie.RemoteProvider
	// Keys holds API keys by provider ID; they never leave the fake except
	// through Key (tests check that responses do not carry them).
	Keys        map[string]string
	Models      []magpie.RemoteModel
	Groups      []magpie.RemoteGroup
	GatewayKeys []magpie.GatewayKey
	Secrets     map[string]string // gateway key secrets by key ID
	nextID      int
	Calls       []string
}

// NewFakeAdmin returns an empty, healthy fake.
func NewFakeAdmin() *FakeAdmin {
	return &FakeAdmin{Version: "0.1.3531", Keys: map[string]string{}, Secrets: map[string]string{}}
}

func (f *FakeAdmin) call(name string) error {
	f.Calls = append(f.Calls, name)
	if f.Down {
		return fmt.Errorf("%w: connection refused", magpie.ErrUnavailable)
	}
	return nil
}

// SetDown makes every call fail with ErrUnavailable.
func (f *FakeAdmin) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Down = down
}

// Key returns the stored API key of a provider.
func (f *FakeAdmin) Key(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Keys[id]
}

// Hello implements magpie.AdminAPI.
func (f *FakeAdmin) Hello(context.Context) (magpie.Hello, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("Hello"); err != nil {
		return magpie.Hello{}, err
	}
	return magpie.Hello{Name: "magpie", Version: f.Version}, nil
}

// ListProviders implements magpie.AdminAPI.
func (f *FakeAdmin) ListProviders(context.Context) ([]magpie.RemoteProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListProviders"); err != nil {
		return nil, err
	}
	out := slices.Clone(f.Providers)
	for i := range out {
		out[i].HasKey = f.Keys[out[i].ID] != ""
	}
	return out, nil
}

// SaveProvider implements magpie.AdminAPI.
func (f *FakeAdmin) SaveProvider(_ context.Context, in magpie.ProviderInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SaveProvider"); err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(p magpie.RemoteProvider) bool { return p.ID == in.ID })
	if in.New && i >= 0 {
		return fmt.Errorf("%w: provider exists", magpie.ErrBadRequest)
	}
	if !in.New && i < 0 {
		return fmt.Errorf("%w: provider %s", magpie.ErrNotFound, in.ID)
	}
	if i < 0 {
		f.Providers = append(f.Providers, magpie.RemoteProvider{ID: in.ID, Preset: in.Preset, Ready: true,
			Models: []string{in.ID + "-large", in.ID + "-small"}})
		i = len(f.Providers) - 1
	}
	p := &f.Providers[i]
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&p.Name, in.Name)
	set(&p.Chat, in.Chat)
	set(&p.Anthropic, in.Anthropic)
	set(&p.Gemini, in.Gemini)
	if in.Key != nil {
		f.Keys[in.ID] = *in.Key
	}
	return nil
}

// SetProviderOff implements magpie.AdminAPI.
func (f *FakeAdmin) SetProviderOff(_ context.Context, id string, off bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetProviderOff"); err != nil {
		return err
	}
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i].Off = off
			return nil
		}
	}
	return fmt.Errorf("%w: provider %s", magpie.ErrNotFound, id)
}

// DeleteProvider implements magpie.AdminAPI.
func (f *FakeAdmin) DeleteProvider(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteProvider"); err != nil {
		return err
	}
	f.Providers = slices.DeleteFunc(f.Providers, func(p magpie.RemoteProvider) bool { return p.ID == id })
	delete(f.Keys, id)
	return nil
}

// ListModels implements magpie.AdminAPI. Without explicit Models it lists
// the models of the enabled providers.
func (f *FakeAdmin) ListModels(context.Context) ([]magpie.RemoteModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListModels"); err != nil {
		return nil, err
	}
	if f.Models != nil {
		return slices.Clone(f.Models), nil
	}
	var out []magpie.RemoteModel
	for _, p := range f.Providers {
		if p.Off {
			continue
		}
		for _, m := range p.Models {
			out = append(out, magpie.RemoteModel{ID: p.ID + "/" + m, OwnedBy: p.ID, ContextWindow: 128000})
		}
	}
	return out, nil
}

// ListGroups implements magpie.AdminAPI.
func (f *FakeAdmin) ListGroups(context.Context) ([]magpie.RemoteGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroups"); err != nil {
		return nil, err
	}
	return slices.Clone(f.Groups), nil
}

// SaveGroup implements magpie.AdminAPI.
func (f *FakeAdmin) SaveGroup(_ context.Context, g magpie.RemoteGroup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SaveGroup"); err != nil {
		return err
	}
	if i := slices.IndexFunc(f.Groups, func(x magpie.RemoteGroup) bool { return x.ID == g.ID }); i >= 0 {
		f.Groups[i] = g
		return nil
	}
	f.Groups = append(f.Groups, g)
	return nil
}

// DeleteGroups implements magpie.AdminAPI.
func (f *FakeAdmin) DeleteGroups(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteGroups"); err != nil {
		return err
	}
	f.Groups = slices.DeleteFunc(f.Groups, func(g magpie.RemoteGroup) bool { return slices.Contains(ids, g.ID) })
	return nil
}

// ListGatewayKeys implements magpie.AdminAPI.
func (f *FakeAdmin) ListGatewayKeys(context.Context) ([]magpie.GatewayKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGatewayKeys"); err != nil {
		return nil, err
	}
	return slices.Clone(f.GatewayKeys), nil
}

// AddGatewayKey implements magpie.AdminAPI.
func (f *FakeAdmin) AddGatewayKey(_ context.Context, name, secret string) (magpie.GatewayKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AddGatewayKey"); err != nil {
		return magpie.GatewayKey{}, err
	}
	f.nextID++
	k := magpie.GatewayKey{ID: "key" + strconv.Itoa(f.nextID), Name: name}
	if secret == "" {
		secret = "sk-magpie-key-generated" + strconv.Itoa(f.nextID)
	}
	f.GatewayKeys = append(f.GatewayKeys, k)
	f.Secrets[k.ID] = secret
	k.Secret = secret
	return k, nil
}

// SetGatewayKeyOff implements magpie.AdminAPI.
func (f *FakeAdmin) SetGatewayKeyOff(_ context.Context, id string, off bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SetGatewayKeyOff"); err != nil {
		return err
	}
	for i := range f.GatewayKeys {
		if f.GatewayKeys[i].ID == id {
			f.GatewayKeys[i].Off = off
			return nil
		}
	}
	return fmt.Errorf("%w: key %s", magpie.ErrNotFound, id)
}

// RemoveGatewayKey implements magpie.AdminAPI.
func (f *FakeAdmin) RemoveGatewayKey(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RemoveGatewayKey"); err != nil {
		return err
	}
	f.GatewayKeys = slices.DeleteFunc(f.GatewayKeys, func(k magpie.GatewayKey) bool { return k.ID == id })
	delete(f.Secrets, id)
	return nil
}

// SecretOf returns the secret of the gateway key called name.
func (f *FakeAdmin) SecretOf(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.GatewayKeys {
		if k.Name == name {
			return f.Secrets[k.ID]
		}
	}
	return ""
}

// Gateway is a magpie.Gateway backed by a FakeAdmin.
type Gateway struct {
	Fake  *FakeAdmin
	State string
}

// Status implements magpie.Gateway.
func (g Gateway) Status() magpie.Status {
	st := g.State
	if st == "" {
		st = magpie.StateRunning
	}
	return magpie.Status{State: st, Addr: magpie.DefaultAddr, Version: g.Fake.Version}
}

// Admin implements magpie.Gateway.
func (g Gateway) Admin() (magpie.AdminAPI, error) { return g.Fake, nil }
