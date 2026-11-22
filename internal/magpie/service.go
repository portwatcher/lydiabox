package magpie

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// ErrConflict is returned when a resource already exists.
var ErrConflict = errors.New("conflict")

// Provider is the API view of a magpie provider (docs/api.md section 10).
// It never carries the API key, only HasKey.
type Provider struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	BaseURL   string   `json:"baseUrl"`
	Enabled   bool     `json:"enabled"`
	HasKey    bool     `json:"hasKey"`
	Models    []string `json:"models"`
	Status    string   `json:"status"`
	LastError string   `json:"lastError"`
	// Subscription is the subscription kind ("claude", "codex", "antigravity") of a
	// provider that serves signed-in accounts instead of an API key.
	Subscription string `json:"subscription,omitempty"`
}

// Model is one model of the gateway catalog.
type Model struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	ContextWindow int64  `json:"contextWindow"`
}

// Routing strategies of the API.
const (
	StrategyFallback   = "fallback"
	StrategyRoundRobin = "round-robin"
)

// RoutingGroup is a named list of model targets with a strategy.
type RoutingGroup struct {
	Name     string   `json:"name"`
	Strategy string   `json:"strategy"`
	Targets  []string `json:"targets"`
}

// GatewayInfo is the response of GET /api/v1/models/gateway.
type GatewayInfo struct {
	Status  string `json:"status"`
	Addr    string `json:"addr"`
	Version string `json:"version"`
	// Mode is ModeGuestNative when the harnesses sign in inside the VM
	// and the host gateway is off; empty otherwise.
	Mode string `json:"mode,omitempty"`
}

// CreateProviderRequest is the body of POST /api/v1/models/providers.
type CreateProviderRequest struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
}

// PatchProviderRequest is the body of PATCH /api/v1/models/providers/{id}.
type PatchProviderRequest struct {
	Name    *string `json:"name"`
	BaseURL *string `json:"baseUrl"`
	APIKey  *string `json:"apiKey"`
	Enabled *bool   `json:"enabled"`
}

// Gateway is the supervisor view the service needs (implemented by
// *Supervisor).
type Gateway interface {
	Status() Status
	Admin() (AdminAPI, error)
}

// Service implements the models routes on top of the magpie admin API.
type Service struct {
	Gateway  Gateway
	Bindings *Bindings
	// Native selects guest-native mode (native.go): no gateway calls.
	Native bool
	// NativeUsed returns the models that the harnesses used, for the
	// native model list. It may be nil.
	NativeUsed func(ctx context.Context) []NativeModel
}

func (s *Service) admin() (AdminAPI, error) {
	if s.Native {
		return nil, ErrGuestNative
	}
	if s.Gateway == nil {
		return nil, fmt.Errorf("%w: gateway not configured", ErrUnavailable)
	}
	return s.Gateway.Admin()
}

// GatewayInfo returns the gateway state.
func (s *Service) GatewayInfo() GatewayInfo {
	if s.Native {
		return GatewayInfo{Status: StateOff, Mode: ModeGuestNative}
	}
	if s.Gateway == nil {
		return GatewayInfo{Status: StateStopped, Addr: DefaultAddr}
	}
	st := s.Gateway.Status()
	return GatewayInfo{Status: st.State, Addr: st.Addr, Version: st.Version}
}

// ListProviders lists providers.
func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	if s.Native {
		return []Provider{}, nil
	}
	a, err := s.admin()
	if err != nil {
		return nil, err
	}
	rps, err := a.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Provider, 0, len(rps))
	for _, p := range rps {
		out = append(out, toProvider(p))
	}
	return out, nil
}

func toProvider(p RemoteProvider) Provider {
	out := Provider{ID: p.ID, Name: p.Name, Enabled: !p.Off, HasKey: p.HasKey, Models: p.Models, LastError: p.Error}
	if out.Models == nil {
		out.Models = []string{}
	}
	if p.Account != nil {
		out.Subscription = p.Account.Agent
	}
	switch {
	case p.Preset != "":
		out.Kind = p.Preset
	case p.Anthropic != "" && p.Chat == "":
		out.Kind = "anthropic"
	case p.Gemini != "" && p.Chat == "":
		out.Kind = "gemini"
	default:
		out.Kind = "openai-compatible"
	}
	if p.Preset == "" {
		out.BaseURL = firstNonEmpty(p.Chat, p.Anthropic, p.Gemini, p.Responses)
	}
	switch {
	case p.Off:
		out.Status = "disabled"
	case p.Error != "":
		out.Status = "error"
	case p.Ready:
		out.Status = "ok"
	default:
		out.Status = "pending"
	}
	return out
}

var slugRe = regexp.MustCompile(`[^a-z0-9.-]+`)

// slug turns a display name into a magpie ID.
func slug(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(s, "-.")
}

func validBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: baseUrl must be an absolute http(s) URL", ErrBadRequest)
	}
	return nil
}

// urlField picks the magpie URL field for a provider kind.
func urlField(kind string, in *ProviderInput, base *string) {
	switch kind {
	case "anthropic":
		in.Anthropic = base
	case "gemini", "google":
		in.Gemini = base
	default:
		in.Chat = base
	}
}

// CreateProvider adds a provider. Without a base URL, kind names a magpie
// preset ("openai", "anthropic", "deepseek", ...); with one, the provider
// is a custom endpoint speaking the kind's protocol.
func (s *Service) CreateProvider(ctx context.Context, req CreateProviderRequest) (Provider, error) {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	if req.Kind == "" {
		return Provider{}, fmt.Errorf("%w: kind is required", ErrBadRequest)
	}
	if err := validBaseURL(req.BaseURL); err != nil {
		return Provider{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Kind
	}
	id := slug(name)
	if id == "" {
		return Provider{}, fmt.Errorf("%w: name must contain letters or digits", ErrBadRequest)
	}
	a, err := s.admin()
	if err != nil {
		return Provider{}, err
	}
	existing, err := a.ListProviders(ctx)
	if err != nil {
		return Provider{}, err
	}
	if slices.ContainsFunc(existing, func(p RemoteProvider) bool { return p.ID == id }) {
		return Provider{}, fmt.Errorf("%w: provider %q already exists", ErrConflict, id)
	}
	in := ProviderInput{ID: id, New: true, Name: &name}
	if req.BaseURL == "" {
		in.Preset = req.Kind
	} else {
		base := req.BaseURL
		urlField(req.Kind, &in, &base)
	}
	if req.APIKey != "" {
		key := req.APIKey
		in.Key = &key
	}
	if err := a.SaveProvider(ctx, in); err != nil {
		return Provider{}, err
	}
	return s.findProvider(ctx, a, id)
}

func (s *Service) findProvider(ctx context.Context, a AdminAPI, id string) (Provider, error) {
	rps, err := a.ListProviders(ctx)
	if err != nil {
		return Provider{}, err
	}
	for _, p := range rps {
		if p.ID == id {
			return toProvider(p), nil
		}
	}
	return Provider{}, fmt.Errorf("%w: provider %q", ErrNotFound, id)
}

// UpdateProvider changes the given fields of a provider.
func (s *Service) UpdateProvider(ctx context.Context, id string, req PatchProviderRequest) (Provider, error) {
	a, err := s.admin()
	if err != nil {
		return Provider{}, err
	}
	cur, err := s.findProvider(ctx, a, id)
	if err != nil {
		return Provider{}, err
	}
	if req.BaseURL != nil {
		if err := validBaseURL(*req.BaseURL); err != nil {
			return Provider{}, err
		}
	}
	if req.Name != nil || req.BaseURL != nil || req.APIKey != nil {
		if cur.Subscription != "" {
			return Provider{}, fmt.Errorf("%w: a subscription provider only takes enabled", ErrBadRequest)
		}
		in := ProviderInput{ID: id, Name: req.Name, Key: req.APIKey}
		if req.BaseURL != nil {
			urlField(cur.Kind, &in, req.BaseURL)
		}
		if err := a.SaveProvider(ctx, in); err != nil {
			return Provider{}, err
		}
	}
	if req.Enabled != nil && *req.Enabled != cur.Enabled {
		if err := a.SetProviderOff(ctx, id, !*req.Enabled); err != nil {
			return Provider{}, err
		}
	}
	return s.findProvider(ctx, a, id)
}

// DeleteProvider removes a provider.
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	a, err := s.admin()
	if err != nil {
		return err
	}
	cur, err := s.findProvider(ctx, a, id)
	if err != nil {
		return err
	}
	if cur.Subscription != "" {
		return fmt.Errorf("%w: remove the accounts of a subscription provider instead", ErrBadRequest)
	}
	return a.DeleteProvider(ctx, id)
}

// ListModels lists the gateway catalog.
func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
	if s.Native {
		return s.nativeModels(ctx), nil
	}
	a, err := s.admin()
	if err != nil {
		return nil, err
	}
	rms, err := a.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(rms))
	for _, m := range rms {
		prov, name, ok := strings.Cut(m.ID, "/")
		if !ok {
			prov, name = m.OwnedBy, m.ID
		}
		out = append(out, Model{ID: m.ID, Provider: prov, Name: name, ContextWindow: m.ContextWindow})
	}
	return out, nil
}

// ListRouting lists routing groups.
func (s *Service) ListRouting(ctx context.Context) ([]RoutingGroup, error) {
	if s.Native {
		return []RoutingGroup{}, nil
	}
	a, err := s.admin()
	if err != nil {
		return nil, err
	}
	gs, err := a.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoutingGroup, 0, len(gs))
	for _, g := range gs {
		strategy := StrategyFallback
		if g.Routing == "rotate" {
			strategy = StrategyRoundRobin
		}
		targets := g.Members
		if targets == nil {
			targets = []string{}
		}
		out = append(out, RoutingGroup{Name: firstNonEmpty(g.Name, g.ID), Strategy: strategy, Targets: targets})
	}
	return out, nil
}

var modelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+-]*/[A-Za-z0-9._:@+/-]+$`)

// ValidModelID reports whether id looks like "provider/model".
func ValidModelID(id string) bool { return len(id) <= 256 && modelIDRe.MatchString(id) }

// ReplaceRouting makes the given groups the complete set of routing groups.
func (s *Service) ReplaceRouting(ctx context.Context, groups []RoutingGroup) ([]RoutingGroup, error) {
	want := make([]RemoteGroup, 0, len(groups))
	seen := map[string]bool{}
	for _, g := range groups {
		id := slug(g.Name)
		if id == "" {
			return nil, fmt.Errorf("%w: group name %q is invalid", ErrBadRequest, g.Name)
		}
		if seen[id] {
			return nil, fmt.Errorf("%w: duplicate group %q", ErrBadRequest, g.Name)
		}
		seen[id] = true
		routing := ""
		switch g.Strategy {
		case StrategyFallback:
			routing = "order"
		case StrategyRoundRobin:
			routing = "rotate"
		default:
			return nil, fmt.Errorf("%w: strategy must be %q or %q", ErrBadRequest, StrategyFallback, StrategyRoundRobin)
		}
		if len(g.Targets) == 0 {
			return nil, fmt.Errorf("%w: group %q has no targets", ErrBadRequest, g.Name)
		}
		for _, t := range g.Targets {
			if !ValidModelID(t) {
				return nil, fmt.Errorf("%w: target %q is not a provider/model ID", ErrBadRequest, t)
			}
		}
		want = append(want, RemoteGroup{ID: id, Name: g.Name, Members: g.Targets, Routing: routing})
	}
	a, err := s.admin()
	if err != nil {
		return nil, err
	}
	have, err := a.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, g := range have {
		if !seen[g.ID] {
			stale = append(stale, g.ID)
		}
	}
	for _, g := range want {
		if err := a.SaveGroup(ctx, g); err != nil {
			return nil, err
		}
	}
	if err := a.DeleteGroups(ctx, stale); err != nil {
		return nil, err
	}
	return s.ListRouting(ctx)
}
