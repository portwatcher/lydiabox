package config

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// Values of LYDIABOX_CLOUDFLARE_TUNNEL.
const (
	// TunnelAuto runs cloudflared when `lydiabox cloudflare setup` saved a
	// tunnel.
	TunnelAuto = "auto"
	// TunnelOff never runs cloudflared (for example when a sidecar container
	// runs it); Access login still works.
	TunnelOff = "off"
)

// RemoteAccessConfig configures remote access through Cloudflare Zero Trust.
// `lydiabox cloudflare setup` stores the tunnel and Access settings in the
// state database; the Access* fields override the stored values one by one,
// or replace them for an Access application made by hand.
type RemoteAccessConfig struct {
	Tunnel           string
	AccessTeamDomain string
	AccessAUD        string
	AccessEmails     []string
	// CloudflareAPIToken and CloudflareAccountID are read only by
	// `lydiabox cloudflare` and only from the environment, so the API token
	// never lands in lydiabox.yaml.
	CloudflareAPIToken  string
	CloudflareAccountID string
	// ListenAddrSet reports that LYDIABOX_LISTEN_ADDR or listen_addr set the
	// listen address. Without it, an active tunnel moves the default
	// listener to loopback so that nobody can bypass Access.
	ListenAddrSet bool
}

// AccessConfigured reports whether any Access override is set.
func (r RemoteAccessConfig) AccessConfigured() bool {
	return r.AccessTeamDomain != "" || r.AccessAUD != "" || len(r.AccessEmails) > 0
}

type remoteFile struct {
	Tunnel           *string  `yaml:"tunnel"`
	AccessTeamDomain *string  `yaml:"access_team_domain"`
	AccessAUD        *string  `yaml:"access_aud"`
	AccessEmails     []string `yaml:"access_emails"`
}

func applyRemoteFile(cfg *Config, fc *fileConfig) {
	r := &cfg.Remote
	if fc.ListenAddr != nil {
		r.ListenAddrSet = true
	}
	setStr(&r.Tunnel, fc.RemoteAccess.Tunnel)
	setStr(&r.AccessTeamDomain, fc.RemoteAccess.AccessTeamDomain)
	setStr(&r.AccessAUD, fc.RemoteAccess.AccessAUD)
	if fc.RemoteAccess.AccessEmails != nil {
		r.AccessEmails = splitList(strings.Join(fc.RemoteAccess.AccessEmails, ","))
	}
}

func applyRemoteEnv(cfg *Config, env map[string]string) {
	r := &cfg.Remote
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(env[k]); v != "" {
				return v
			}
		}
		return ""
	}
	if get("LYDIABOX_LISTEN_ADDR") != "" {
		r.ListenAddrSet = true
	}
	if v := get("LYDIABOX_CLOUDFLARE_TUNNEL"); v != "" {
		r.Tunnel = strings.ToLower(v)
	}
	if v := get("LYDIABOX_ACCESS_TEAM_DOMAIN"); v != "" {
		r.AccessTeamDomain = v
	}
	if v := get("LYDIABOX_ACCESS_AUD"); v != "" {
		r.AccessAUD = v
	}
	if v := get("LYDIABOX_ACCESS_EMAILS"); v != "" {
		r.AccessEmails = splitList(v)
	}
	r.CloudflareAPIToken = get("LYDIABOX_CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_TOKEN")
	r.CloudflareAccountID = get("LYDIABOX_CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_ACCOUNT_ID")
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (r RemoteAccessConfig) validate() []error {
	var errs []error
	if r.Tunnel != TunnelAuto && r.Tunnel != TunnelOff {
		errs = append(errs, fmt.Errorf("LYDIABOX_CLOUDFLARE_TUNNEL %q must be auto or off", r.Tunnel))
	}
	for _, e := range r.AccessEmails {
		if at := strings.LastIndexByte(e, '@'); at < 0 || at == len(e)-1 || strings.ContainsAny(e, " <>") {
			errs = append(errs, fmt.Errorf("LYDIABOX_ACCESS_EMAILS: %q is not an email address or @domain", e))
		}
	}
	if strings.ContainsAny(r.AccessTeamDomain, " @") {
		errs = append(errs, fmt.Errorf("LYDIABOX_ACCESS_TEAM_DOMAIN %q must look like myteam.cloudflareaccess.com", r.AccessTeamDomain))
	}
	return errs
}

// LoopbackListenAddr returns addr with its host replaced by 127.0.0.1.
func LoopbackListenAddr(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// IsLoopbackAddr reports whether a listen address binds only loopback.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CloudflaredDir is the data directory of the cloudflared connector.
func (c *Config) CloudflaredDir() string { return filepath.Join(c.StateDir(), "cloudflared") }
