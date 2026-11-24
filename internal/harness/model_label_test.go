package harness_test

import (
	"context"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
	"github.com/portwatcher/lydiabox/internal/harness/harnesstest"
)

// prefixedDiscovery reports the models of opencode and dsh sessions with
// the gateway provider prefix, as a guest binary or a discovery cache of an
// older version does.
type prefixedDiscovery struct {
	harness.Discovery
	ids map[string]string // harness -> native ID
}

func (p prefixedDiscovery) Discover(ctx context.Context, tenantID string, names []string) (harness.Discovered, error) {
	d, err := p.Discovery.Discover(ctx, tenantID, names)
	if err != nil {
		return d, err
	}
	for h, id := range p.ids {
		d.Sessions[h] = append(d.Sessions[h], harness.SessionMeta{Harness: h, NativeID: id, Title: h,
			WorkspaceDir: "/workspace/lydiabox", Model: "lydiabox/codex/gpt-6.1-sol", UpdatedAt: time.Now()})
	}
	return d, nil
}

func TestGatewayModel(t *testing.T) {
	for in, want := range map[string]string{
		"lydiabox/codex/gpt-6.1-sol": "codex/gpt-6.1-sol",
		"codex/gpt-6.1-sol":          "codex/gpt-6.1-sol",
		"lydiabox/":                  "lydiabox/",
		"kimi/kimi-k3":               "kimi/kimi-k3",
		"":                           "",
	} {
		if got := harness.GatewayModel(in); got != want {
			t.Errorf("GatewayModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSessionModelWithoutGatewayPrefix: the session list shows the gateway
// model ID for opencode and dsh sessions of both engines, also when the
// discovery reports the "lydiabox/" provider prefix.
func TestSessionModelWithoutGatewayPrefix(t *testing.T) {
	e := newEnv(t, &harnesstest.Runner{})
	disc := prefixedDiscovery{Discovery: localDiscovery(e.home, e.workspace), ids: map[string]string{
		harness.OpenCode: "ses_prefixed", harness.DSH: "dsh-prefixed"}}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: disc, Store: e.store, Runner: e.runner, Waker: e.waker, Wiring: e.wiring, Logger: quiet(),
		KillGrace: 200 * time.Millisecond, TermGrace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	type start struct{ harness, engine, resume string }
	var ids []string
	for _, s := range []start{
		{harness.OpenCode, harness.EnginePTY, "ses_prefixed"},
		{harness.OpenCode, harness.EngineACP, ""},
		{harness.DSH, harness.EngineACP, "dsh-prefixed"},
		{harness.DSH, harness.EnginePTY, ""},
	} {
		got, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: s.harness, Engine: s.engine, WorkspaceDir: "/workspace/lydiabox",
			Model: "codex/gpt-6.1-sol", ResumeNativeID: s.resume, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("%s %s: %v", s.harness, s.engine, err)
		}
		if got.Model != "codex/gpt-6.1-sol" {
			t.Errorf("%s %s: created with model %q", s.harness, s.engine, got.Model)
		}
		ids = append(ids, got.ID)
	}
	list, err := mgr.List(ctx, tenant, harness.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]harness.Session{}
	for _, s := range list {
		byID[s.ID] = s
	}
	for _, id := range ids {
		if s, ok := byID[id]; !ok || s.Model != "codex/gpt-6.1-sol" {
			t.Errorf("%s: listed %+v (%v)", id, s, ok)
		}
	}
}
