//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/portwatcher/lydiabox/internal/agentfs"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
)

// discover runs the harness adapters on the guest home root with the agent
// identity, so only session metadata crosses the RPC link.
func (f *fsService) discover(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.HarnessDiscoverParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	home, err := f.files.Dir(agentfs.RootHome)
	if err != nil {
		return nil, fsErr(err)
	}
	res := rpc.HarnessDiscoverResult{Sessions: map[string]json.RawMessage{}, Errors: map[string]string{}}
	err = f.files.Do(func() error {
		for _, a := range adapters.All() {
			if len(p.Harnesses) > 0 && !slices.Contains(p.Harnesses, a.Name()) {
				continue
			}
			metas, err := a.DiscoverSessions(home)
			if err != nil {
				res.Errors[a.Name()] = err.Error()
				continue
			}
			b, err := json.Marshal(metas)
			if err != nil {
				return err
			}
			res.Sessions[a.Name()] = b
		}
		return nil
	})
	return res, fsErr(err)
}

// purge deletes the native transcript of one session.
func (f *fsService) purge(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.HarnessPurgeParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	home, err := f.files.Dir(agentfs.RootHome)
	if err != nil {
		return nil, fsErr(err)
	}
	for _, a := range adapters.All() {
		if a.Name() != p.Harness {
			continue
		}
		err := f.files.Do(func() error { return a.Purge(home, p.ID) })
		switch {
		case errors.Is(err, harness.ErrBadRequest):
			return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
		case err != nil:
			return nil, fsErr(err)
		}
		return nil, nil
	}
	return nil, rpc.Errorf(rpc.CodeNotFound, "unknown harness %q", p.Harness)
}

// transcript reads the native transcript of one session.
func (f *fsService) transcript(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.HarnessSessionParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	home, err := f.files.Dir(agentfs.RootHome)
	if err != nil {
		return nil, fsErr(err)
	}
	for _, a := range adapters.All() {
		if a.Name() != p.Harness {
			continue
		}
		tr, ok := a.(harness.Transcriber)
		if !ok {
			return nil, rpc.Errorf(rpc.CodeNotFound, "%s has no transcript reader", p.Harness)
		}
		var t harness.Transcript
		err := f.files.Do(func() (err error) {
			t, err = tr.Transcript(home, p.ID)
			return err
		})
		switch {
		case errors.Is(err, harness.ErrBadRequest):
			return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
		case errors.Is(err, harness.ErrNotFound):
			return nil, rpc.Errorf(rpc.CodeNotFound, "%v", err)
		case err != nil:
			return nil, fsErr(err)
		}
		return t.Fit(), nil
	}
	return nil, rpc.Errorf(rpc.CodeNotFound, "unknown harness %q", p.Harness)
}

// sessionEvents reads a page of the typed events of one session.
func (f *fsService) sessionEvents(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.HarnessEventsParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	home, err := f.files.Dir(agentfs.RootHome)
	if err != nil {
		return nil, fsErr(err)
	}
	var evs []harness.Event
	err = f.files.Do(func() (err error) {
		evs, err = f.events.Events(home, p.Harness, p.ID)
		return err
	})
	switch {
	case errors.Is(err, harness.ErrBadRequest):
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	case errors.Is(err, harness.ErrNotFound):
		return nil, rpc.Errorf(rpc.CodeNotFound, "%v", err)
	case err != nil:
		return nil, fsErr(err)
	}
	return harness.EventPageOf(evs, p.After), nil
}
