//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/portwatcher/lydiabox/internal/api"
)

// sessionState returns the status and the live flag of one session.
func sessionState(t *testing.T, r *cellRig, id string) (string, bool) {
	t.Helper()
	var list struct {
		Sessions []struct {
			ID, Status string
			Live       bool
		}
	}
	if err := json.Unmarshal(r.must(http.MethodGet, "/api/v1/sessions", "", http.StatusOK), &list); err != nil {
		t.Fatal(err)
	}
	for _, s := range list.Sessions {
		if s.ID == id {
			return s.Status, s.Live
		}
	}
	return "", false
}

// TestInstaboxHostRestartKeepsLiveTerminals restarts the host process
// while the machine runs (a release): the terminals of before become live
// again on the same processes, a long-running process keeps running, and
// deleting a session ends its guest process.
func TestInstaboxHostRestartKeepsLiveTerminals(t *testing.T) {
	checkGoroutines(t)
	r := newCellRig(t)
	var logs lockedBuffer
	c := r.startLogged(&logs)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	shellID, shell, shellPID := liveShell(ctx, t, r)
	agentID, agent, agentPID := liveShell(ctx, t, r)
	// The "agent": a long process in the second terminal.
	if err := agent.Write(ctx, websocket.MessageBinary, []byte("while true; do echo tick; sleep 0.2; done\n")); err != nil {
		t.Fatal(err)
	}
	// The loop must run before the stop: input that is still on its way
	// when the host stops is lost.
	var started strings.Builder
	for !strings.Contains(started.String(), "tick\r\n") {
		typ, data, err := agent.Read(ctx)
		if err != nil {
			t.Fatalf("agent output before the restart %q: %v", started.String(), err)
		}
		if typ == websocket.MessageBinary {
			started.Write(data)
		}
	}
	shell.CloseNow()
	agent.CloseNow()

	c.stop(t)
	c = r.startLogged(&logs)
	deadline := time.Now().Add(20 * time.Second)
	for {
		s1, live1 := sessionState(t, r, shellID)
		s2, live2 := sessionState(t, r, agentID)
		if live1 && live2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the restart: shell %s live=%v, agent %s live=%v\n%s", s1, live1, s2, live2, logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	shell = r.dialWS(ctx, "/api/v1/sessions/"+shellID+"/ws", api.PTYSubprotocol)
	if got := terminalPID(ctx, t, shell); got != shellPID {
		t.Fatalf("shell PID %s after the restart, want %s", got, shellPID)
	}
	agent = r.dialWS(ctx, "/api/v1/sessions/"+agentID+"/ws", api.PTYSubprotocol)
	var out strings.Builder
	for strings.Count(out.String(), "tick") < 3 {
		typ, data, err := agent.Read(ctx)
		if err != nil {
			t.Fatalf("agent output %q: %v", out.String(), err)
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
		}
	}
	// Stop the loop, then check that the shell under it is the same one.
	if err := agent.Write(ctx, websocket.MessageBinary, []byte{3}); err != nil {
		t.Fatal(err)
	}
	if got := terminalPID(ctx, t, agent); got != agentPID {
		t.Fatalf("agent shell PID %s after the restart, want %s", got, agentPID)
	}
	shell.CloseNow()
	agent.CloseNow()
	if strings.Contains(logs.String(), `msg="harness session exited"`) {
		t.Fatalf("a restart ended a session:\n%s", logs.String())
	}

	// Restart again without opening the terminals, then delete: the guest
	// process must end even though no terminal was attached by a client.
	c.stop(t)
	c = r.startLogged(&logs)
	r.must(http.MethodDelete, "/api/v1/sessions/"+shellID, "", http.StatusNoContent)
	r.must(http.MethodDelete, "/api/v1/sessions/"+agentID, "", http.StatusNoContent)
	deadline = time.Now().Add(20 * time.Second)
	for {
		running := 0
		for _, p := range r.runner.PTYs(r.machine()) {
			if p.Running {
				running++
			}
		}
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d guest terminals still run after the deletes", running)
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.stop(t)
}
