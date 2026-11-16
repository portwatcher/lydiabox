//go:build unix

package ptymanager

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestPTYEnvironmentIsDefined: a PTY gets the user, locale, and path
// variables of the daemon, LydiaBox's variables, and nothing else from the
// daemon's environment (the service manager's and supervisord's
// variables, the daemon's settings, and a supplier's harness settings and
// credentials).
func TestPTYEnvironmentIsDefined(t *testing.T) {
	old := daemonEnviron
	t.Cleanup(func() { daemonEnviron = old })
	daemonEnviron = func() []string {
		return []string{
			"HOME=/home/agent", "USER=alice", "LOGNAME=alice", "SHELL=/bin/bash", "PATH=" + os.Getenv("PATH"),
			"LC_ALL=C.UTF-8", "TZ=Etc/UTC", "DISPLAY=:0", "XDG_RUNTIME_DIR=/run/user/1000",
			"INVOCATION_ID=0123", "JOURNAL_STREAM=8:1", "SYSTEMD_EXEC_PID=7", "MEMORY_PRESSURE_WATCH=/sys/x",
			"SUPERVISOR_ENABLED=1", "SUPERVISOR_SERVER_URL=unix:///run/supervisor.sock", "DEBIAN_FRONTEND=noninteractive",
			"LYDIABOX_GUEST_LISTEN=ws://0.0.0.0:9000/", "LYDIABOX_LINK_SECRET=s3cret", "LYDIABOX_GUEST_HOME_ROOT=/data/x",
			"GEMINI_API_KEY=supplier-key", "ANTHROPIC_API_KEY=supplier-key", "OPENAI_API_KEY=supplier-key",
			"CLAUDE_CONFIG_DIR=/data/home/.claude", "CODEX_HOME=/data/home/.codex", "XDG_CONFIG_HOME=/data/home/.config",
			"INSTABOX_API_KEY=supplier-key", "MCP_SERVERS=x", "TMPDIR=/data/tmp", "OPENCODE_DISABLE_AUTOUPDATE=1",
		}
	}
	m := newManager(t)
	s, err := m.Start(Spec{ID: "env", Command: []string{"env"}, Env: map[string]string{"LYDIABOX_SESSION_ID": "claude-code:1"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("env did not exit")
	}
	_, replay := s.Attach()
	out := string(replay)
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "="); ok {
			got[k] = v
		}
	}
	for k, want := range map[string]string{
		"HOME": "/home/agent", "USER": "alice", "LOGNAME": "alice", "SHELL": "/bin/bash", "LC_ALL": "C.UTF-8",
		"LANG": "C.UTF-8", "TZ": "Etc/UTC", "DISPLAY": ":0", "XDG_RUNTIME_DIR": "/run/user/1000",
		"ANTHROPIC_BASE_URL": GatewayURL, "OPENAI_BASE_URL": GatewayURL, "GEMINI_API_KEY": "lydiabox-local",
		"LYDIABOX_HOST_ADDR": "unix:///run/lydiabox/host-9001.sock", "LYDIABOX_PTY_ID": "env", "TERM": "xterm-256color",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	for _, k := range []string{"INVOCATION_ID", "JOURNAL_STREAM", "SYSTEMD_EXEC_PID", "MEMORY_PRESSURE_WATCH",
		"SUPERVISOR_ENABLED", "SUPERVISOR_SERVER_URL", "DEBIAN_FRONTEND", "LYDIABOX_GUEST_LISTEN", "LYDIABOX_LINK_SECRET",
		"LYDIABOX_GUEST_HOME_ROOT", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
		"XDG_CONFIG_HOME", "INSTABOX_API_KEY", "MCP_SERVERS", "TMPDIR", "OPENCODE_DISABLE_AUTOUPDATE"} {
		if v, ok := got[k]; ok {
			t.Errorf("%s reached the PTY (length %d)", k, len(v))
		}
	}
	if strings.Contains(out, "supplier-key") || strings.Contains(out, "s3cret") {
		t.Error("a supplier credential or the link secret reached the PTY")
	}
}
