package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Cell mode: several `lydiabox up` processes ("cells", one tenant each) on
// one host. Each cell needs its own tenant ID, data directory, and the
// host-wide names and addresses below. The defaults are the values of the
// single-user mode.

// Default magpie addresses on the host.
const (
	DefaultMagpieAddr    = "127.0.0.1:3425"
	DefaultMagpieWebAddr = "127.0.0.1:3426"
)

// SubnetAuto selects the guest subnet index from the state database: 0
// for the "default" tenant, else the lowest free index from 1.
const SubnetAuto = -1

// MaxSubnetIndex is the highest guest subnet index (172.30.254.0/24).
const MaxSubnetIndex = 254

// MaxTapNameLength is the longest Linux interface name.
const MaxTapNameLength = 15

var (
	tapPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,15}$`)
	tablePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

// MagpieConfig holds the listen addresses of the host magpie gateway.
type MagpieConfig struct {
	// Addr is the OpenAI-compatible gateway (LYDIABOX_MAGPIE_ADDR).
	Addr string
	// WebAddr is the magpie admin web API (LYDIABOX_MAGPIE_WEB_ADDR).
	WebAddr string
}

// cellFile mirrors the cell-mode keys of lydiabox.yaml that are not in
// the vm section.
type cellFile struct {
	TenantID *string `yaml:"tenant_id"`
	CellMode *bool   `yaml:"cell_mode"`
	// GuestNative is guest_native (Config.GuestNative).
	GuestNative *bool `yaml:"guest_native"`
	Magpie   struct {
		Addr    *string `yaml:"addr"`
		WebAddr *string `yaml:"web_addr"`
	} `yaml:"magpie"`
}

// vmCellFile mirrors the cell-mode keys of the vm section.
type vmCellFile struct {
	WebRTCPort  *int    `yaml:"webrtc_udp_port"`
	SubnetIndex *int    `yaml:"subnet_index"`
	Tap         *string `yaml:"tap"`
	NftTable    *string `yaml:"nft_table"`
}

func applyCellFile(cfg *Config, fc *fileConfig) {
	setStr(&cfg.TenantID, fc.Cell.TenantID)
	if fc.Cell.CellMode != nil {
		cfg.CellMode = *fc.Cell.CellMode
	}
	if fc.Cell.GuestNative != nil {
		v := *fc.Cell.GuestNative
		cfg.GuestNativeSet = &v
	}
	setStr(&cfg.Magpie.Addr, fc.Cell.Magpie.Addr)
	setStr(&cfg.Magpie.WebAddr, fc.Cell.Magpie.WebAddr)
	if fc.VM.WebRTCPort != nil {
		cfg.VM.WebRTCPort, cfg.VM.WebRTCPortSet = *fc.VM.WebRTCPort, true
	}
	if fc.VM.SubnetIndex != nil {
		cfg.VM.SubnetIndex = *fc.VM.SubnetIndex
	}
	setStr(&cfg.VM.Tap, fc.VM.Tap)
	setStr(&cfg.VM.NftTable, fc.VM.NftTable)
}

func applyCellEnv(cfg *Config, env map[string]string) []error {
	str := func(key string, dst *string) {
		if v := strings.TrimSpace(env[key]); v != "" {
			*dst = v
		}
	}
	str("LYDIABOX_TENANT_ID", &cfg.TenantID)
	str("LYDIABOX_MAGPIE_ADDR", &cfg.Magpie.Addr)
	str("LYDIABOX_MAGPIE_WEB_ADDR", &cfg.Magpie.WebAddr)
	str("LYDIABOX_VM_TAP", &cfg.VM.Tap)
	str("LYDIABOX_VM_NFT_TABLE", &cfg.VM.NftTable)
	var errs []error
	if v := strings.TrimSpace(env["LYDIABOX_CELL_MODE"]); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_CELL_MODE: %q is not a boolean", v))
		} else {
			cfg.CellMode = b
		}
	}
	if v := strings.TrimSpace(env["LYDIABOX_GUEST_NATIVE"]); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_GUEST_NATIVE: %q is not a boolean", v))
		} else {
			cfg.GuestNativeSet = &b
		}
	}
	if cfg.CellMode {
		// The control plane runs the tunnel; a cell never starts cloudflared.
		cfg.Remote.Tunnel = TunnelOff
	}
	if v := strings.TrimSpace(env["LYDIABOX_WEBRTC_UDP_PORT"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_WEBRTC_UDP_PORT: %q is not an integer", v))
		} else {
			cfg.VM.WebRTCPort, cfg.VM.WebRTCPortSet = n, true
		}
	}
	if v := strings.TrimSpace(env["LYDIABOX_VM_SUBNET_INDEX"]); v != "" {
		if strings.EqualFold(v, "auto") {
			cfg.VM.SubnetIndex = SubnetAuto
		} else {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("LYDIABOX_VM_SUBNET_INDEX: %q is not an integer or \"auto\"", v))
			} else {
				cfg.VM.SubnetIndex = n
			}
		}
	}
	return errs
}

// GuestNative reports guest-native mode (docs/design.md section 4.4.2):
// the harnesses sign in inside the VM and send model traffic directly to
// the provider; the host runs no model gateway and keeps no subscription
// or API key. It is LYDIABOX_GUEST_NATIVE when set, else on in cell mode
// and off in the self-hosted mode.
func (c *Config) GuestNative() bool {
	if c.GuestNativeSet != nil {
		return *c.GuestNativeSet
	}
	return c.CellMode
}

// validateCell checks the cell-mode settings and that the host addresses
// of this process differ from each other.
func (c *Config) validateCell() []error {
	var errs []error
	for _, a := range []struct{ name, val string }{
		{"LYDIABOX_MAGPIE_ADDR", c.Magpie.Addr},
		{"LYDIABOX_MAGPIE_WEB_ADDR", c.Magpie.WebAddr},
	} {
		if _, port, err := net.SplitHostPort(a.val); err != nil || port == "" || port == "0" {
			errs = append(errs, fmt.Errorf("%s %q must be host:port with a fixed port", a.name, a.val))
		}
	}
	addrs := []struct{ name, val string }{
		{"LYDIABOX_LISTEN_ADDR", c.ListenAddr},
		{"LYDIABOX_S3_GATEWAY_ADDR", c.S3.GatewayAddr},
		{"LYDIABOX_MAGPIE_ADDR", c.Magpie.Addr},
		{"LYDIABOX_MAGPIE_WEB_ADDR", c.Magpie.WebAddr},
	}
	for i, a := range addrs {
		for _, b := range addrs[i+1:] {
			if AddrsOverlap(a.val, b.val) {
				errs = append(errs, fmt.Errorf("%s %q and %s %q use the same port", a.name, a.val, b.name, b.val))
			}
		}
	}
	if c.VM.WebRTCPort < 0 || c.VM.WebRTCPort > 65535 {
		errs = append(errs, fmt.Errorf("LYDIABOX_WEBRTC_UDP_PORT %d must be a UDP port (0 disables the forward)", c.VM.WebRTCPort))
	}
	if c.VM.SubnetIndex != SubnetAuto && (c.VM.SubnetIndex < 0 || c.VM.SubnetIndex > MaxSubnetIndex) {
		errs = append(errs, fmt.Errorf("LYDIABOX_VM_SUBNET_INDEX %d must be \"auto\" or between 0 and %d", c.VM.SubnetIndex, MaxSubnetIndex))
	}
	if c.VM.Tap != "" && !tapPattern.MatchString(c.VM.Tap) {
		errs = append(errs, fmt.Errorf("LYDIABOX_VM_TAP %q must be 1 to %d letters, digits, '-', or '_'", c.VM.Tap, MaxTapNameLength))
	}
	if c.VM.NftTable != "" && !tablePattern.MatchString(c.VM.NftTable) {
		errs = append(errs, fmt.Errorf("LYDIABOX_VM_NFT_TABLE %q must match %s", c.VM.NftTable, tablePattern))
	}
	return errs
}

// validateCellServe checks the settings that a cell of a control plane
// must get from it: the cell does not generate secrets of its own, and it
// serves one named tenant.
func (c *Config) validateCellServe() []error {
	if !c.CellMode {
		return nil
	}
	var errs []error
	if c.TenantID == DefaultTenantID {
		errs = append(errs, errors.New("LYDIABOX_CELL_MODE needs LYDIABOX_TENANT_ID: a cell serves one named tenant"))
	}
	if c.AuthToken == "" {
		errs = append(errs, errors.New("LYDIABOX_CELL_MODE needs LYDIABOX_AUTH_TOKEN or LYDIABOX_AUTH_TOKEN_FILE from the control plane"))
	}
	if c.MasterKey == "" {
		errs = append(errs, errors.New("LYDIABOX_CELL_MODE needs LYDIABOX_MASTER_KEY or LYDIABOX_MASTER_KEY_FILE from the control plane"))
	}
	return errs
}

// AddrsOverlap reports whether two TCP listen addresses conflict: the same
// fixed port on the same host, or on a wildcard host.
func AddrsOverlap(a, b string) bool {
	ha, pa, err := net.SplitHostPort(a)
	if err != nil || pa == "" || pa == "0" {
		return false
	}
	hb, pb, err := net.SplitHostPort(b)
	if err != nil || pb != pa {
		return false
	}
	wild := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	return ha == hb || wild(ha) || wild(hb)
}
