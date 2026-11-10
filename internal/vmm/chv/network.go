package chv

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lydiabox/lydiabox/internal/storage"
)

// privateRanges are the destinations the guest may not reach: the host's
// private networks, link-local (cloud metadata), CGNAT, and loopback.
var privateRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "127.0.0.0/8"}

// Firewall is the host packet filter used for NAT and isolation.
type Firewall struct {
	Runner   storage.Runner
	LookPath func(string) (string, error)
}

func tableName(nw Network) string { return "lydiabox_" + nw.Tap }

// NftScript returns the nftables ruleset: masquerade guest traffic, drop
// guest traffic to private networks and to the host itself, and forward
// the WebRTC UDP port to the guest.
func NftScript(nw Network, webrtcPort int) string {
	var b strings.Builder
	t := tableName(nw)
	fmt.Fprintf(&b, "table ip %s {\n", t)
	fmt.Fprintf(&b, "\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	fmt.Fprintf(&b, "\t\tip saddr %s oifname != %q masquerade\n\t}\n", nw.Subnet(), nw.Tap)
	if webrtcPort > 0 {
		fmt.Fprintf(&b, "\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
		fmt.Fprintf(&b, "\t\tiifname != %q udp dport %d dnat to %s\n\t}\n", nw.Tap, webrtcPort, nw.GuestIP)
	}
	fmt.Fprintf(&b, "\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	fmt.Fprintf(&b, "\t\tiifname %q ct state established,related accept\n", nw.Tap)
	fmt.Fprintf(&b, "\t\tiifname %q ip daddr { %s } drop\n", nw.Tap, strings.Join(privateRanges, ", "))
	fmt.Fprintf(&b, "\t\tiifname %q accept\n\t}\n", nw.Tap)
	fmt.Fprintf(&b, "\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n")
	fmt.Fprintf(&b, "\t\tiifname %q ct state established,related accept\n", nw.Tap)
	fmt.Fprintf(&b, "\t\tiifname %q drop\n\t}\n}\n", nw.Tap)
	return b.String()
}

// IptablesRules returns the equivalent iptables rules as argument lists
// with "-A"/"-I" replaced by op ("-A" to add, "-D" to delete).
func IptablesRules(nw Network, webrtcPort int, add bool) [][]string {
	ins, app := "-I", "-A"
	if !add {
		ins, app = "-D", "-D"
	}
	tap := nw.Tap
	rules := [][]string{
		{"-t", "nat", app, "POSTROUTING", "-s", nw.Subnet(), "!", "-o", tap, "-j", "MASQUERADE"},
		{app, "INPUT", "-i", tap, "-j", "DROP"},
		{ins, "INPUT", "-i", tap, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	for _, r := range privateRanges {
		rules = append(rules, []string{ins, "FORWARD", "-i", tap, "-d", r, "-j", "DROP"})
	}
	rules = append(rules, []string{ins, "FORWARD", "-i", tap, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"})
	if webrtcPort > 0 {
		rules = append(rules, []string{"-t", "nat", app, "PREROUTING", "!", "-i", tap, "-p", "udp", "--dport",
			fmt.Sprint(webrtcPort), "-j", "DNAT", "--to-destination", nw.GuestIP})
	}
	// Appended after the inserted private-network drops.
	return append(rules, ForwardAcceptRules(nw, webrtcPort, app)...)
}

// ForwardAcceptRules returns the iptables rules that let guest traffic
// through a filter FORWARD chain with policy DROP (Docker and ufw set it).
// Every base chain on the forward hook must accept a packet, so the accept
// in the lydiabox nftables table does not override that policy; the
// private-network drops of the lydiabox table still apply.
func ForwardAcceptRules(nw Network, webrtcPort int, op string) [][]string {
	rules := [][]string{
		{op, "FORWARD", "-i", nw.Tap, "-j", "ACCEPT"},
		{op, "FORWARD", "-o", nw.Tap, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	if webrtcPort > 0 {
		rules = append(rules, []string{op, "FORWARD", "-o", nw.Tap, "-p", "udp", "--dport", fmt.Sprint(webrtcPort), "-j", "ACCEPT"})
	}
	return rules
}

// forwardPolicyDrop reports whether the iptables filter FORWARD chain drops
// by default.
func (f Firewall) forwardPolicyDrop(ctx context.Context, ipt string) bool {
	var out strings.Builder
	if err := f.Runner.Run(ctx, storage.Command{Path: ipt, Args: []string{"-w", "-S", "FORWARD"}, Stdout: &out}); err != nil {
		return false
	}
	return strings.Contains(out.String(), "-P FORWARD DROP")
}

// Setup enables forwarding and installs the rules.
func (f Firewall) Setup(ctx context.Context, nw Network, webrtcPort int) error {
	if err := f.Runner.Run(ctx, storage.Command{Path: "sysctl", Args: []string{"-q", "-w", "net.ipv4.ip_forward=1"}}); err != nil {
		return fmt.Errorf("chv: enable IP forwarding: %w", err)
	}
	_ = f.Teardown(ctx, nw, webrtcPort)
	if nft, err := f.LookPath("nft"); err == nil {
		script := NftScript(nw, webrtcPort)
		if err := f.Runner.Run(ctx, storage.Command{Path: nft, Args: []string{"-f", "-"}, Stdin: strings.NewReader(script)}); err != nil {
			return fmt.Errorf("chv: install nftables rules: %w", err)
		}
		if ipt, err := f.LookPath("iptables"); err == nil && f.forwardPolicyDrop(ctx, ipt) {
			for _, r := range ForwardAcceptRules(nw, webrtcPort, "-I") {
				if err := f.Runner.Run(ctx, storage.Command{Path: ipt, Args: append([]string{"-w"}, r...)}); err != nil {
					return fmt.Errorf("chv: allow guest traffic in the iptables FORWARD chain: %w", err)
				}
			}
		}
		return nil
	}
	ipt, err := f.LookPath("iptables")
	if err != nil {
		return errors.New("chv: neither nft nor iptables is installed; the guest network needs one of them")
	}
	for _, r := range IptablesRules(nw, webrtcPort, true) {
		if err := f.Runner.Run(ctx, storage.Command{Path: ipt, Args: append([]string{"-w"}, r...)}); err != nil {
			return fmt.Errorf("chv: install iptables rules: %w", err)
		}
	}
	return nil
}

// Teardown removes the rules (missing rules are not an error).
func (f Firewall) Teardown(ctx context.Context, nw Network, webrtcPort int) error {
	if nft, err := f.LookPath("nft"); err == nil {
		if ipt, err := f.LookPath("iptables"); err == nil {
			for _, r := range ForwardAcceptRules(nw, webrtcPort, "-D") {
				// Missing rules are expected when the FORWARD policy is ACCEPT.
				_ = f.Runner.Run(ctx, storage.Command{Path: ipt, Args: append([]string{"-w"}, r...)})
			}
		}
		err := f.Runner.Run(ctx, storage.Command{Path: nft, Args: []string{"delete", "table", "ip", tableName(nw)}})
		if err != nil && !strings.Contains(err.Error(), "No such file") {
			return err
		}
		return nil
	}
	ipt, err := f.LookPath("iptables")
	if err != nil {
		return nil
	}
	var errs []error
	for _, r := range IptablesRules(nw, webrtcPort, false) {
		if err := f.Runner.Run(ctx, storage.Command{Path: ipt, Args: append([]string{"-w"}, r...)}); err != nil &&
			!strings.Contains(err.Error(), "does a matching rule exist") && !strings.Contains(err.Error(), "Bad rule") {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
