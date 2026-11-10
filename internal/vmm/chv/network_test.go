package chv

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/storage"
)

// policyRunner answers `iptables -S FORWARD` with a FORWARD policy and
// records every command.
type policyRunner struct {
	policy string
	runs   []string
}

func (p *policyRunner) Run(_ context.Context, c storage.Command) error {
	line := filepath.Base(c.Path) + " " + strings.Join(c.Args, " ")
	p.runs = append(p.runs, line)
	if strings.HasSuffix(line, "-S FORWARD") && c.Stdout != nil {
		fmt.Fprintf(c.Stdout, "-P FORWARD %s\n", p.policy)
	}
	return nil
}

func (p *policyRunner) Start(storage.Command) (storage.Process, error) {
	return nil, errors.New("unexpected start")
}

// TestFirewallForwardPolicyDrop: on a host whose iptables FORWARD policy is
// DROP (Docker, ufw), Setup also accepts the guest traffic there.
func TestFirewallForwardPolicyDrop(t *testing.T) {
	nw := DefaultNetwork("default")
	look := func(name string) (string, error) { return "/usr/sbin/" + name, nil }
	accept := "iptables -w -I FORWARD -i " + nw.Tap + " -j ACCEPT"
	for _, tc := range []struct {
		policy string
		want   bool
	}{{"DROP", true}, {"ACCEPT", false}} {
		r := &policyRunner{policy: tc.policy}
		if err := (Firewall{Runner: r, LookPath: look}).Setup(context.Background(), nw, 50000); err != nil {
			t.Fatal(err)
		}
		runs := strings.Join(r.runs, "\n")
		if strings.Contains(runs, accept) != tc.want {
			t.Fatalf("policy %s: accept rule present = %v, want %v:\n%s", tc.policy, !tc.want, tc.want, runs)
		}
		if tc.want && !strings.Contains(runs, "-I FORWARD -o "+nw.Tap+" -p udp --dport 50000 -j ACCEPT") {
			t.Fatalf("WebRTC forward rule missing:\n%s", runs)
		}
		if !strings.Contains(runs, "iptables -w -D FORWARD -i "+nw.Tap+" -j ACCEPT") {
			t.Fatalf("Setup does not remove stale forward rules first:\n%s", runs)
		}
	}
	rules := IptablesRules(nw, 0, true)
	if last := strings.Join(rules[len(rules)-1], " "); !strings.HasPrefix(last, "-A FORWARD -o "+nw.Tap) {
		t.Fatalf("iptables fallback does not end with the forward accept rules: %s", last)
	}
}
