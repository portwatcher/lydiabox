package imagetest

import (
	"context"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/guest/ptymanager"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// ptyEnvScript sends the names (never the values) of a PTY's variables,
// its HOME, and the directory that HOME resolves to, to the host broker.
const ptyEnvScript = `names=$(env | cut -d= -f1 | sort | paste -sd, -)
curl -s -G --unix-socket /run/lydiabox/host-9001.sock http://broker/env \
	--data-urlencode "names=$names" --data-urlencode "home=$HOME" \
	--data-urlencode "realhome=$(readlink -f "$HOME")" --data-urlencode "pwd=$(pwd)"`

// shellNames are set by the shell that runs ptyEnvScript, not inherited.
var shellNames = map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}

// agentHome is the directory of /home/agent in the image
// (LYDIABOX_IMAGE_AGENT_HOME, default /data/lydiabox/home).
func agentHome() string {
	if h := os.Getenv("LYDIABOX_IMAGE_AGENT_HOME"); h != "" {
		return h
	}
	return "/data/lydiabox/home"
}

// checkPTYEnv checks the environment of a PTY in the image: HOME is
// /home/agent in LydiaBox's home (LYDIABOX_IMAGE_AGENT_HOME, default
// /data/lydiabox/home), and every variable is either inherited by rule
// (ptymanager.Inherited) or set by LydiaBox (ptymanager.DefaultEnv,
// DISPLAY, LYDIABOX_PTY_ID). Nothing else of the daemon's environment
// (systemd, supervisord, the daemon's settings, a supplier's variables)
// reaches it.
func checkPTYEnv(ctx context.Context, t *testing.T, sess *rpc.Session, report <-chan url.Values) {
	t.Helper()
	var info rpc.PTYInfo
	if err := sess.Call(ctx, rpc.MethodPTYStart, rpc.PTYStartParams{Command: []string{"sh", "-c", ptyEnvScript}}, &info); err != nil {
		t.Fatalf("pty.start (env): %v", err)
	}
	var q url.Values
	select {
	case q = <-report:
	case <-ctx.Done():
		t.Fatal("the PTY did not report its environment")
	}
	wantHome := agentHome()
	if q.Get("home") != "/home/agent" || q.Get("realhome") != wantHome {
		t.Errorf("PTY HOME = %q (resolves to %q), want /home/agent in %s", q.Get("home"), q.Get("realhome"), wantHome)
	}
	if q.Get("pwd") != "/data/workspace" {
		t.Errorf("PTY directory = %q, want /data/workspace", q.Get("pwd"))
	}
	allowed := map[string]bool{"DISPLAY": true, "LYDIABOX_PTY_ID": true}
	for k := range ptymanager.DefaultEnv("") {
		allowed[k] = true
	}
	names := strings.Split(q.Get("names"), ",")
	var extra []string
	for _, n := range names {
		if n != "" && !allowed[n] && !shellNames[n] && !ptymanager.Inherited(n) {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("variables outside the defined PTY environment: %s", strings.Join(extra, ", "))
	}
	for _, need := range []string{"HOME", "USER", "PATH", "LANG", "ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "LYDIABOX_HOST_ADDR"} {
		if !strings.Contains(","+q.Get("names")+",", ","+need+",") {
			t.Errorf("PTY lacks %s", need)
		}
	}
	t.Logf("PTY variables: %s", q.Get("names"))
}
