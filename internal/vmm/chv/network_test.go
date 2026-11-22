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

// The guest network is IPv4 only: all IPv6 from the TAP is dropped, so
// the guest cannot reach host services over link-local addresses.
func TestFirewallDropsGuestIPv6(t *testing.T) {
	nw := DefaultNetwork("default")
	script := NftScript(nw, 0)
	i := strings.Index(script, "table ip6 "+TableName(nw)+" {")
	if i < 0 {
		t.Fatalf("no ip6 table:\n%s", script)
	}
	ip6 := script[i:]
	for _, w := range []string{"hook forward", "hook input"} {
		if !strings.Contains(ip6, w) {
			t.Fatalf("ip6 table has no %s chain:\n%s", w, ip6)
		}
	}
	if strings.Count(ip6, "iifname \""+nw.Tap+"\" drop") != 2 {
		t.Fatalf("ip6 table does not drop TAP input and forward:\n%s", ip6)
	}
	// iptables fallback: ip6tables drops are installed too.
	look := func(name string) (string, error) {
		if name == "nft" {
			return "", errors.New("not found")
		}
		return "/usr/sbin/" + name, nil
	}
	r := &policyRunner{policy: "ACCEPT"}
	if err := (Firewall{Runner: r, LookPath: look}).Setup(context.Background(), nw, 0); err != nil {
		t.Fatal(err)
	}
	runs := strings.Join(r.runs, "\n")
	for _, w := range []string{"ip6tables -w -I INPUT -i " + nw.Tap + " -j DROP", "ip6tables -w -I FORWARD -i " + nw.Tap + " -j DROP", "iptables -w -I INPUT -i " + nw.Tap + " -j DROP"} {
		if !strings.Contains(runs, w) {
			t.Fatalf("missing %q:\n%s", w, runs)
		}
	}
}

// A host without IPv6 (ipv6.disable=1) needs no IPv6 rules: a missing
// ip6tables does not stop the VM.
func TestFirewallWithoutHostIPv6(t *testing.T) {
	old := ipv6Enabled
	ipv6Enabled = func() bool { return false }
	defer func() { ipv6Enabled = old }()
	nw := DefaultNetwork("default")
	look := func(name string) (string, error) {
		if name == "iptables" {
			return "/usr/sbin/iptables", nil
		}
		return "", errors.New("not found")
	}
	if err := (Firewall{Runner: &policyRunner{policy: "ACCEPT"}, LookPath: look}).Setup(context.Background(), nw, 0); err != nil {
		t.Fatalf("Setup without host IPv6: %v", err)
	}
	ipv6Enabled = func() bool { return true }
	if err := (Firewall{Runner: &policyRunner{policy: "ACCEPT"}, LookPath: look}).Setup(context.Background(), nw, 0); err == nil {
		t.Fatal("Setup with host IPv6 and no ip6tables must fail")
	}
}
