package imagetest

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// launchScript waits for the shell of the newest xfce4-terminal (the one
// that desktop.launch started; the terminal also has short-lived children
// such as dbus-launch) and sends its directory, its PWD, and its window
// title to the host broker, with the directory and PWD of the Xfce panel
// (a terminal from the panel or the menu inherits them).
const launchScript = `uid=$(id -u)
pwd_of() { tr '\0' '\n' <"/proc/$1/environ" | sed -n 's/^PWD=//p'; }
term=""
shell=""
i=0
while [ -z "$shell" ] && [ "$i" -lt 90 ]; do
	term=$(pgrep -n -u "$uid" -x xfce4-terminal)
	if [ -n "$term" ]; then
		shell=$(pgrep -n -P "$term" -x 'bash|sh|dash|zsh')
		if [ -n "$shell" ] && ! readlink "/proc/$shell/cwd" >/dev/null 2>&1; then
			shell=""
		fi
	fi
	[ -n "$shell" ] || sleep 1
	i=$((i + 1))
done
title=""
i=0
while [ "$i" -lt 30 ]; do
	for w in $(xprop -root _NET_CLIENT_LIST | sed 's/^.*# //; s/,//g'); do
		case "$(xprop -id "$w" _NET_WM_PID 2>/dev/null)" in
		*" = $term") title=$(xprop -id "$w" _NET_WM_NAME | sed 's/^[^=]*= //') ;;
		esac
	done
	case "$title" in *"~"*) break ;; esac
	sleep 1
	i=$((i + 1))
done
panel=$(pgrep -u "$uid" -x xfce4-panel | head -n 1)
curl -s -G --unix-socket /run/lydiabox/host-9001.sock http://broker/launch \
	--data-urlencode "shell=$shell" --data-urlencode "title=$title" \
	--data-urlencode "cwd=$(readlink "/proc/$shell/cwd")" --data-urlencode "pwd=$(pwd_of "$shell")" \
	--data-urlencode "panel=$panel" \
	--data-urlencode "panelcwd=$(readlink "/proc/$panel/cwd")" --data-urlencode "panelpwd=$(pwd_of "$panel")"`

// checkLaunchDir starts a terminal on the desktop the way the Desktop
// page's Terminal button does (desktop.launch) and checks that its shell
// starts in the agent home by its logical path: the directory is LydiaBox's
// home and PWD is /home/agent, so a prompt and the window title show "~".
// The Xfce panel, which starts the terminals of the panel and the menu,
// has the same directory and PWD.
func checkLaunchDir(ctx context.Context, t *testing.T, sess *rpc.Session, report <-chan url.Values, wantHome string) {
	t.Helper()
	if err := sess.Call(ctx, rpc.MethodDesktopLaunch, rpc.LaunchParams{App: "terminal"}, nil); err != nil {
		if strings.Contains(err.Error(), "is installed") {
			t.Logf("SKIP terminal launch: %v", err)
			return
		}
		t.Fatalf("desktop.launch terminal: %v", err)
	}
	var info rpc.PTYInfo
	if err := sess.Call(ctx, rpc.MethodPTYStart, rpc.PTYStartParams{Command: []string{"sh", "-c", launchScript}}, &info); err != nil {
		t.Fatalf("pty.start (launch): %v", err)
	}
	var q url.Values
	select {
	case q = <-report:
	case <-ctx.Done():
		t.Fatal("the PTY did not report the terminal")
	}
	t.Logf("launched terminal: %v", q)
	if q.Get("shell") == "" {
		t.Fatal("no shell of a launched xfce4-terminal")
	}
	if q.Get("cwd") != wantHome || q.Get("pwd") != "/home/agent" {
		t.Errorf("terminal shell directory %q with PWD %q, want %s with PWD /home/agent", q.Get("cwd"), q.Get("pwd"), wantHome)
	}
	if title := q.Get("title"); !strings.Contains(title, "~") || strings.Contains(title, wantHome) {
		t.Errorf("terminal title %q, want the home as ~", title)
	}
	if q.Get("panel") == "" {
		t.Log("SKIP panel directory: no xfce4-panel")
		return
	}
	if q.Get("panelcwd") != wantHome || q.Get("panelpwd") != "/home/agent" {
		t.Errorf("xfce4-panel directory %q with PWD %q, want %s with PWD /home/agent", q.Get("panelcwd"), q.Get("panelpwd"), wantHome)
	}
}
