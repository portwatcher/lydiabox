package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	"github.com/portwatcher/lydiabox/internal/store"
)

// GuestCaller performs a unary guest daemon call without waking the VM
// (*guestclient.Client.FSCall).
type GuestCaller interface {
	FSCall(ctx context.Context, tenantID, method string, params, out any) error
}

// DiscoveryCache stores the last discovery result per harness
// (implemented by *store.Store).
type DiscoveryCache interface {
	PutDiscoveryCache(ctx context.Context, tenantID, harness string, payload json.RawMessage) error
	ListDiscoveryCache(ctx context.Context, tenantID string) ([]store.DiscoveryCacheEntry, error)
}

// DefaultDiscoverTimeout bounds one guest discovery.
const DefaultDiscoverTimeout = 30 * time.Second

// GuestDiscovery runs discovery in the guest (provider storage mode). It
// asks the guest only while the machine runs, so listing sessions never
// wakes it; otherwise, or when the call fails, it returns the result cached
// in the state database and marks it as cached.
type GuestDiscovery struct {
	Guest GuestCaller
	Cache DiscoveryCache
	// Running reports whether the machine of a tenant runs and no pause or
	// stop of it is in progress (vmm.Controller.Serving).
	Running func(tenantID string) bool
	// Begin takes a files lease and wakes the machine for a purge
	// (remotefs.FS.Begin).
	Begin   func(ctx context.Context, tenantID string) (release func(), err error)
	Timeout time.Duration
	Logger  *slog.Logger

	rootsMu sync.Mutex
	roots   map[string][]guestRootPrefix
}

func (g *GuestDiscovery) log() *slog.Logger {
	if g.Logger == nil {
		return slog.Default()
	}
	return g.Logger
}

// Discover implements Discovery.
func (g *GuestDiscovery) Discover(ctx context.Context, tenantID string, harnesses []string) (Discovered, error) {
	if g.Running == nil || g.Running(tenantID) {
		out, err := g.fresh(ctx, tenantID, harnesses)
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil {
			return Discovered{}, ctx.Err()
		}
		if g.Running != nil && !g.Running(tenantID) {
			// The machine paused or stopped during the call: expected.
			g.log().Info("guest session discovery ended by a pause or stop; using the cached result", "err", err)
		} else {
			g.log().Warn("guest session discovery failed; using the cached result", "err", err)
		}
	}
	return g.cached(ctx, tenantID, harnesses)
}

func (g *GuestDiscovery) fresh(ctx context.Context, tenantID string, harnesses []string) (Discovered, error) {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = DefaultDiscoverTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res rpc.HarnessDiscoverResult
	if err := g.Guest.FSCall(cctx, tenantID, rpc.MethodHarnessDiscover, rpc.HarnessDiscoverParams{Harnesses: harnesses}, &res); err != nil {
		return Discovered{}, err
	}
	out := Discovered{Sessions: map[string][]SessionMeta{}, Errors: map[string]error{}}
	for name, msg := range res.Errors {
		out.Errors[name] = errors.New(msg)
	}
	roots := g.guestRoots(cctx, tenantID)
	for name, raw := range res.Sessions {
		// The guest chooses the keys: keep only the harnesses asked for,
		// so it cannot add cache rows without bound.
		if !wanted(harnesses, name) {
			continue
		}
		var metas []SessionMeta
		if err := json.Unmarshal(raw, &metas); err != nil {
			out.Errors[name] = fmt.Errorf("decode guest sessions: %w", err)
			continue
		}
		if canonicalDirs(metas, roots) {
			if b, err := json.Marshal(metas); err == nil {
				raw = b
			}
		}
		out.Sessions[name] = metas
		if g.Cache != nil {
			if err := g.Cache.PutDiscoveryCache(context.WithoutCancel(ctx), tenantID, name, raw); err != nil {
				g.log().Warn("store the session discovery cache", "harness", name, "err", err)
			}
		}
	}
	return out, nil
}

func (g *GuestDiscovery) cached(ctx context.Context, tenantID string, harnesses []string) (Discovered, error) {
	out := Discovered{Sessions: map[string][]SessionMeta{}, Errors: map[string]error{}, Cached: true}
	if g.Cache == nil {
		return out, nil
	}
	entries, err := g.Cache.ListDiscoveryCache(ctx, tenantID)
	if err != nil {
		return Discovered{}, err
	}
	// A cache of an older host version can hold guest directories; the
	// roots are known when the guest answered once since this host started.
	roots, _ := g.knownRoots(tenantID)
	for _, e := range entries {
		if !wanted(harnesses, e.Harness) {
			continue
		}
		var metas []SessionMeta
		if err := json.Unmarshal(e.Payload, &metas); err != nil {
			out.Errors[e.Harness] = fmt.Errorf("decode cached sessions: %w", err)
			continue
		}
		canonicalDirs(metas, roots)
		out.Sessions[e.Harness] = metas
		if out.CachedAt.IsZero() || e.UpdatedAt.Before(out.CachedAt) {
			out.CachedAt = e.UpdatedAt
		}
	}
	return out, nil
}

// Purge implements Discovery. It wakes the machine, deletes the transcript
// in the guest, and drops the session from the cache.
func (g *GuestDiscovery) Purge(ctx context.Context, tenantID, harness, nativeID string) error {
	if g.Begin != nil {
		release, err := g.Begin(ctx, tenantID)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		defer release()
	}
	err := g.Guest.FSCall(ctx, tenantID, rpc.MethodHarnessPurge, rpc.HarnessPurgeParams{Harness: harness, ID: nativeID}, nil)
	switch {
	case rpc.IsCode(err, rpc.CodeBadRequest):
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	case rpc.IsCode(err, rpc.CodeNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case err != nil:
		var re *rpc.Error
		if errors.As(err, &re) {
			return err
		}
		return fmt.Errorf("%w: purge in the guest: %v", ErrUnavailable, err)
	}
	g.dropCached(ctx, tenantID, harness, nativeID)
	return nil
}

// Transcript implements Discovery. The files are on the machine disk, so
// it starts or resumes the machine (an action of the user: the view of one
// session).
func (g *GuestDiscovery) Transcript(ctx context.Context, tenantID, harness, nativeID string) (Transcript, error) {
	if g.Begin != nil {
		release, err := g.Begin(ctx, tenantID)
		if err != nil {
			return Transcript{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		defer release()
	}
	var t Transcript
	err := g.Guest.FSCall(ctx, tenantID, rpc.MethodHarnessTranscript, rpc.HarnessSessionParams{Harness: harness, ID: nativeID}, &t)
	switch {
	case err == nil:
		return t, nil
	case rpc.IsCode(err, rpc.CodeBadRequest):
		return Transcript{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	case rpc.IsCode(err, rpc.CodeNotFound):
		return Transcript{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	case rpc.IsCode(err, rpc.CodeNoMethod):
		return Transcript{}, fmt.Errorf("%w: the guest program of the machine is too old to read transcripts; update the machine image", ErrUnavailable)
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return Transcript{}, err
	}
	return Transcript{}, fmt.Errorf("%w: read the transcript in the guest: %v", ErrUnavailable, err)
}

func (g *GuestDiscovery) dropCached(ctx context.Context, tenantID, harness, nativeID string) {
	if g.Cache == nil {
		return
	}
	entries, err := g.Cache.ListDiscoveryCache(ctx, tenantID)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Harness != harness {
			continue
		}
		var metas []SessionMeta
		if json.Unmarshal(e.Payload, &metas) != nil {
			return
		}
		kept := metas[:0]
		for _, m := range metas {
			if m.NativeID != nativeID {
				kept = append(kept, m)
			}
		}
		if b, err := json.Marshal(kept); err == nil {
			_ = g.Cache.PutDiscoveryCache(ctx, tenantID, harness, b)
		}
	}
}
