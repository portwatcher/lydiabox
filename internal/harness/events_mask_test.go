package harness_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/secretmask"
)

// The host copy of the events does not keep a token that a harness or a
// tool printed; the live events are not changed.
func TestEventCopyMasksTokens(t *testing.T) {
	e := newCacheEnv(t, true, 10*time.Millisecond)
	token := "sk-ant-oat01-" + strings.Repeat("x", 40)
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "cat auth.json"), ev("a1", `{"accessToken":"`+token+`"}`))
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:s1")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	c.follow(t, sub, "the events", func() bool { return len(c.order) == 2 })
	eventually(t, "the copy", func() bool {
		keys, ok := e.cache.Keys(tenant, harness.Codex, "s1")
		return ok && len(keys) == 2
	})
	got, err := e.cache.GetSessionEvents(context.Background(), tenant, harness.Codex, "s1")
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, raw := range got.Events {
		all += string(raw)
	}
	if strings.Contains(all, token) || !strings.Contains(all, secretmask.Mask) {
		t.Fatalf("stored copy keeps the token: %s", all)
	}
}
