package magpie

import (
	"context"
	"errors"
)

// Error classes returned by the admin client and the models service. The
// API layer maps them to HTTP status codes with errors.Is.
var (
	ErrUnavailable = errors.New("magpie is not available")
	ErrNotFound    = errors.New("not found")
	ErrBadRequest  = errors.New("bad request")
)

// Hello is the identity magpie reports on GET /api/hello.
type Hello struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RemoteProvider is a provider as the magpie admin API lists it.
type RemoteProvider struct {
	ID        string
	Name      string
	Preset    string
	Chat      string
	Responses string
	Anthropic string
	Gemini    string
	Off       bool
	HasKey    bool
	Ready     bool
	Models    []string
	// Error is the last listing or request error magpie reports.
	Error string
}

// ProviderInput creates or updates a provider. Nil fields are left
// unchanged on update.
type ProviderInput struct {
	ID string
	// New creates a provider instead of replacing the one with this ID.
	New       bool
	Name      *string
	Preset    string
	Chat      *string
	Anthropic *string
	Gemini    *string
	Key       *string
}

// RemoteModel is one model of the gateway catalog (GET /v1/models).
type RemoteModel struct {
	ID            string
	OwnedBy       string
	ContextWindow int64
}

// RemoteGroup is a magpie routing group. Routing is "order" (fallback),
// "rotate" (round robin), or another magpie strategy.
type RemoteGroup struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Members  []string `json:"members"`
	Routing  string   `json:"routing,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
}

// GatewayKey is a magpie caller key ("gateway key"). Secret is only set in
// the response that creates the key.
type GatewayKey struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Off    bool   `json:"off,omitempty"`
	Secret string `json:"secret,omitempty"`
}

// AdminAPI is the part of the magpie HTTP API that LydiaBox uses. The HTTP
// implementation is HTTPAdmin; tests use fakes.
type AdminAPI interface {
	Hello(ctx context.Context) (Hello, error)
	ListProviders(ctx context.Context) ([]RemoteProvider, error)
	SaveProvider(ctx context.Context, in ProviderInput) error
	SetProviderOff(ctx context.Context, id string, off bool) error
	DeleteProvider(ctx context.Context, id string) error
	ListModels(ctx context.Context) ([]RemoteModel, error)
	ListGroups(ctx context.Context) ([]RemoteGroup, error)
	SaveGroup(ctx context.Context, g RemoteGroup) error
	DeleteGroups(ctx context.Context, ids []string) error
	ListGatewayKeys(ctx context.Context) ([]GatewayKey, error)
	AddGatewayKey(ctx context.Context, name, secret string) (GatewayKey, error)
	SetGatewayKeyOff(ctx context.Context, id string, off bool) error
	RemoveGatewayKey(ctx context.Context, id string) error
}
