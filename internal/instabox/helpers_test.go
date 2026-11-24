package instabox_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
)

// rig is a fake server with a client whose retry waits are recorded
// instead of slept, and whose debug log is captured.
type rig struct {
	fake   *instaboxtest.Server
	client *instabox.Client
	log    *bytes.Buffer

	mu    sync.Mutex
	waits []time.Duration
}

func newRig(t *testing.T, opts instaboxtest.Options, mutate ...func(*instabox.Options)) *rig {
	t.Helper()
	fake := instaboxtest.New(opts)
	t.Cleanup(fake.Close)
	r := &rig{fake: fake, log: &bytes.Buffer{}}
	co := instabox.Options{
		BaseURL: fake.URL,
		APIKey:  fake.APIKey(),
		Logger:  slog.New(slog.NewTextHandler(syncWriter{r}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sleep: func(ctx context.Context, d time.Duration) error {
			r.mu.Lock()
			r.waits = append(r.waits, d)
			r.mu.Unlock()
			return ctx.Err()
		},
	}
	for _, m := range mutate {
		m(&co)
	}
	c, err := instabox.NewClient(co)
	if err != nil {
		t.Fatal(err)
	}
	r.client = c
	return r
}

type syncWriter struct{ r *rig }

func (w syncWriter) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	return w.r.log.Write(p)
}

func (r *rig) Waits() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.waits...)
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// running creates a machine and waits until it runs.
func (r *rig) running(t *testing.T, name string) *instabox.Machine {
	t.Helper()
	ctx := ctxT(t)
	op, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: name})
	if err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	if _, err := r.client.WaitOperation(ctx, op); err != nil {
		t.Fatalf("WaitOperation: %v", err)
	}
	m, err := r.client.GetMachine(ctx, op.MachineID)
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if m.State != instabox.StateRunning {
		t.Fatalf("state = %s, want running", m.State)
	}
	return m
}

// done returns a function that takes a lifecycle call's results and
// waits for its operation: r.done(t)(r.client.StopMachine(...)).
func (r *rig) done(t *testing.T) func(*instabox.Operation, error) *instabox.Operation {
	return func(op *instabox.Operation, err error) *instabox.Operation {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		final, err := r.client.WaitOperation(ctxT(t), op)
		if err != nil {
			t.Fatal(err)
		}
		return final
	}
}
