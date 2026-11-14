package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// SetupParams is the desired remote access configuration.
type SetupParams struct {
	// Hostname is the public name of the web UI, for example agent.example.com.
	Hostname string
	// Emails is the allow list. An entry "@example.com" allows a domain.
	Emails []string
	// OriginURL is where cloudflared sends requests, for example
	// http://127.0.0.1:8080.
	OriginURL string
	// Zone is the DNS zone name; empty finds it from Hostname.
	Zone string
	// TunnelName defaults to TunnelNameFor(Hostname).
	TunnelName string
	// ServiceToken creates an Access service token for non-browser clients.
	ServiceToken bool
	// BypassPaths get their own Access application with a Bypass policy, so
	// clients that cannot send service token headers (WebDAV, S3) reach
	// them with the admin token alone. Example: /webdav, /s3.
	BypassPaths []string
	// TURN creates a Cloudflare Realtime TURN key for the desktop stream.
	TURN bool
	// SessionDuration of an Access login (default 24h).
	SessionDuration string
	// Progress receives one line per change; nil discards them.
	Progress func(format string, args ...any)
}

// State is the non-secret result of Setup that LydiaBox stores.
type State struct {
	AccountID      string   `json:"accountId"`
	Hostname       string   `json:"hostname"`
	Emails         []string `json:"emails"`
	OriginURL      string   `json:"originUrl"`
	TeamDomain     string   `json:"teamDomain"`
	AUD            string   `json:"aud"`
	TunnelID       string   `json:"tunnelId"`
	TunnelName     string   `json:"tunnelName"`
	ZoneID         string   `json:"zoneId"`
	ZoneName       string   `json:"zoneName"`
	DNSRecordID    string   `json:"dnsRecordId"`
	AppID          string   `json:"appId"`
	OwnerPolicyID  string   `json:"ownerPolicyId"`
	ServiceTokenID string   `json:"serviceTokenId,omitempty"`
	// ServiceTokenClientID is the CF-Access-Client-Id header value.
	ServiceTokenClientID string   `json:"serviceTokenClientId,omitempty"`
	BypassPaths          []string `json:"bypassPaths,omitempty"`
	// TURNKeyID is the Cloudflare Realtime TURN key of the desktop stream.
	TURNKeyID string    `json:"turnKeyId,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Secrets are the secret results of Setup. LydiaBox stores them encrypted.
type Secrets struct {
	TunnelToken string `json:"tunnelToken"`
	// ServiceTokenSecret is the CF-Access-Client-Secret header value.
	ServiceTokenSecret string `json:"serviceTokenSecret,omitempty"`
	// TURNKeySecret is the bearer token of the TURN key.
	TURNKeySecret string `json:"turnKeySecret,omitempty"`
}

// Resource names derived from the hostname, so a repeated Setup finds the
// resources of the previous run even when the local state is lost.
type names struct {
	tunnel, app, owner, service, token, bypass, turn string
}

// TunnelNameFor is the default tunnel name of a hostname.
func TunnelNameFor(hostname string) string {
	return "lydiabox-" + strings.ReplaceAll(hostname, ".", "-")
}

func namesFor(hostname, tunnel string) names {
	if tunnel == "" {
		tunnel = TunnelNameFor(hostname)
	}
	return names{
		tunnel:  tunnel,
		app:     "LydiaBox (" + hostname + ")",
		owner:   "LydiaBox owner (" + hostname + ")",
		service: "LydiaBox service token (" + hostname + ")",
		token:   "LydiaBox (" + hostname + ")",
		bypass:  "LydiaBox bypass (" + hostname + ")",
		turn:    "LydiaBox (" + hostname + ")",
	}
}

var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// Normalize validates p and fills defaults.
func (p *SetupParams) Normalize() error {
	p.Hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p.Hostname)), ".")
	if !hostnameRE.MatchString(p.Hostname) {
		return fmt.Errorf("cloudflare: %q is not a valid hostname (example: agent.example.com)", p.Hostname)
	}
	emails, err := NormalizeEmails(p.Emails)
	if err != nil {
		return err
	}
	if len(emails) == 0 {
		return errors.New("cloudflare: at least one email is required")
	}
	p.Emails = emails
	u, err := url.Parse(p.OriginURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("cloudflare: origin %q must be an http:// or https:// URL", p.OriginURL)
	}
	if p.SessionDuration == "" {
		p.SessionDuration = "24h"
	}
	if _, err := time.ParseDuration(p.SessionDuration); err != nil {
		return fmt.Errorf("cloudflare: session duration: %w", err)
	}
	paths := make([]string, 0, len(p.BypassPaths))
	for _, raw := range p.BypassPaths {
		bp := "/" + strings.Trim(strings.TrimSpace(raw), "/")
		if bp == "/" || strings.ContainsAny(bp, "*?#") {
			return fmt.Errorf("cloudflare: bypass path %q must name one sub-path such as /webdav", raw)
		}
		if !slices.Contains(paths, bp) {
			paths = append(paths, bp)
		}
	}
	slices.Sort(paths)
	p.BypassPaths = paths
	if p.Progress == nil {
		p.Progress = func(string, ...any) {}
	}
	return nil
}

// NormalizeEmails lower-cases, validates, and de-duplicates an allow list.
func NormalizeEmails(in []string) ([]string, error) {
	var out []string
	for _, raw := range in {
		for _, e := range strings.Split(raw, ",") {
			e = strings.ToLower(strings.TrimSpace(e))
			if e == "" {
				continue
			}
			if domain, ok := strings.CutPrefix(e, "@"); ok {
				if !hostnameRE.MatchString(domain) {
					return nil, fmt.Errorf("cloudflare: %q is not a valid email domain", e)
				}
			} else if a, err := mail.ParseAddress(e); err != nil || a.Address != e {
				return nil, fmt.Errorf("cloudflare: %q is not a valid email address", e)
			}
			if !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// Setup creates or updates every Cloudflare resource of remote access and
// returns what LydiaBox must store. It finds existing resources by name, so
// running it again converges instead of duplicating. prev and prevSecrets
// are the stored result of an earlier run (zero values when none).
func Setup(ctx context.Context, c *Client, p SetupParams, prev State, prevSecrets Secrets) (State, Secrets, error) {
	if err := p.Normalize(); err != nil {
		return State{}, Secrets{}, err
	}
	if prev.Hostname != "" && prev.Hostname != p.Hostname {
		return State{}, Secrets{}, fmt.Errorf("cloudflare: remote access is set up for %s; run 'lydiabox cloudflare teardown' first", prev.Hostname)
	}
	n := namesFor(p.Hostname, p.TunnelName)
	st := State{AccountID: c.AccountID(), Hostname: p.Hostname, Emails: p.Emails, OriginURL: p.OriginURL,
		TunnelName: n.tunnel, BypassPaths: p.BypassPaths}
	var sec Secrets

	org, err := c.AccessOrganization(ctx)
	if err != nil {
		return st, sec, err
	}
	st.TeamDomain = org.AuthDomain
	// Check for a foreign application before anything is created.
	apps, err := c.Applications(ctx)
	if err != nil {
		return st, sec, err
	}
	for _, domain := range append([]string{p.Hostname}, bypassDomains(p)...) {
		if err := checkForeignApp(apps, domain); err != nil {
			return st, sec, err
		}
	}

	if err := setupTunnel(ctx, c, p, &st, &sec); err != nil {
		return st, sec, err
	}
	if err := setupDNS(ctx, c, p, &st); err != nil {
		return st, sec, err
	}
	if err := ensureOTP(ctx, c, p.Progress); err != nil {
		return st, sec, err
	}
	policies, err := c.Policies(ctx)
	if err != nil {
		return st, sec, err
	}
	owner, err := upsertPolicy(ctx, c, policies, Policy{Name: n.owner, Decision: DecisionAllow, Include: EmailRules(p.Emails)}, p.Progress)
	if err != nil {
		return st, sec, err
	}
	st.OwnerPolicyID = owner.ID
	refs := []PolicyRef{{ID: owner.ID, Precedence: 1}}
	if p.ServiceToken {
		svcRef, err := setupServiceToken(ctx, c, p, n, policies, prev, prevSecrets, &st, &sec)
		if err != nil {
			return st, sec, err
		}
		refs = append(refs, *svcRef)
	}
	app, err := upsertApp(ctx, c, apps, Application{
		Name: n.app, Domain: p.Hostname, Type: "self_hosted", SessionDuration: p.SessionDuration, Policies: refs,
	}, p.Progress)
	if err != nil {
		return st, sec, err
	}
	st.AppID, st.AUD = app.ID, app.AUD
	if st.AUD == "" {
		return st, sec, fmt.Errorf("cloudflare: Access application %s has no audience tag", app.ID)
	}
	if !p.ServiceToken {
		if err := removeServiceToken(ctx, c, n, policies, p.Progress); err != nil {
			return st, sec, err
		}
	}
	if err := setupBypass(ctx, c, p, n, apps, policies); err != nil {
		return st, sec, err
	}
	// Without -turn, only a key that the stored setup records is removed,
	// so a token without the Realtime permission still works.
	if p.TURN {
		err = setupTURN(ctx, c, p, n, prev, prevSecrets, &st, &sec)
	} else if prev.TURNKeyID != "" {
		err = removeTURN(ctx, c, n, p.Progress)
	}
	if err != nil {
		return st, sec, err
	}
	st.UpdatedAt = time.Now().UTC()
	return st, sec, nil
}

func setupTunnel(ctx context.Context, c *Client, p SetupParams, st *State, sec *Secrets) error {
	t, err := c.FindTunnel(ctx, st.TunnelName)
	if err != nil {
		return err
	}
	if t == nil {
		created, err := c.CreateTunnel(ctx, st.TunnelName)
		if err != nil {
			return err
		}
		t = &created
		p.Progress("created tunnel %s (%s)", t.Name, t.ID)
	} else {
		p.Progress("reused tunnel %s (%s)", t.Name, t.ID)
	}
	st.TunnelID = t.ID
	if sec.TunnelToken, err = c.TunnelToken(ctx, t.ID); err != nil {
		return err
	}
	if sec.TunnelToken == "" {
		return fmt.Errorf("cloudflare: tunnel %s returned an empty token", t.ID)
	}
	if err := c.PutTunnelConfig(ctx, t.ID, p.Hostname, p.OriginURL); err != nil {
		return err
	}
	p.Progress("routed %s to %s", p.Hostname, p.OriginURL)
	return nil
}

func setupDNS(ctx context.Context, c *Client, p SetupParams, st *State) error {
	zone, err := c.FindZone(ctx, p.Hostname, p.Zone)
	if err != nil {
		return err
	}
	st.ZoneID, st.ZoneName = zone.ID, zone.Name
	recs, err := c.DNSRecords(ctx, zone.ID, p.Hostname)
	if err != nil {
		return err
	}
	target := TunnelTarget(st.TunnelID)
	for _, r := range recs {
		if strings.EqualFold(r.Type, "CNAME") && strings.EqualFold(r.Content, target) {
			if !r.Proxied {
				return fmt.Errorf("cloudflare: the DNS record of %s is not proxied; turn on the proxy (orange cloud) or delete the record", p.Hostname)
			}
			st.DNSRecordID = r.ID
			p.Progress("reused DNS record %s CNAME %s", p.Hostname, target)
			return nil
		}
	}
	if len(recs) > 0 {
		return fmt.Errorf("cloudflare: %s already has a %s record (%s); delete it or choose another hostname",
			p.Hostname, recs[0].Type, recs[0].Content)
	}
	rec, err := c.CreateDNSRecord(ctx, zone.ID, DNSRecord{
		Type: "CNAME", Name: p.Hostname, Content: target, Proxied: true, TTL: 1, Comment: "LydiaBox remote access",
	})
	if err != nil {
		return err
	}
	st.DNSRecordID = rec.ID
	p.Progress("created DNS record %s CNAME %s", p.Hostname, target)
	return nil
}

func ensureOTP(ctx context.Context, c *Client, progress func(string, ...any)) error {
	idps, err := c.IdentityProviders(ctx)
	if err != nil {
		return err
	}
	for _, idp := range idps {
		if idp.Type == IdentityProviderOTP {
			return nil
		}
	}
	if _, err := c.CreateIdentityProvider(ctx, IdentityProvider{Name: "One-time PIN", Type: IdentityProviderOTP}); err != nil {
		return err
	}
	progress("enabled the one-time PIN login method")
	return nil
}

func upsertPolicy(ctx context.Context, c *Client, existing []Policy, want Policy, progress func(string, ...any)) (Policy, error) {
	for _, p := range existing {
		if p.Name == want.Name {
			out, err := c.UpdatePolicy(ctx, p.ID, want)
			if err != nil {
				return out, err
			}
			if out.ID == "" {
				out.ID = p.ID
			}
			progress("updated Access policy %q", want.Name)
			return out, nil
		}
	}
	out, err := c.CreatePolicy(ctx, want)
	if err == nil {
		progress("created Access policy %q", want.Name)
	}
	return out, err
}

// appPrefix starts the name of every Access application that Setup makes.
// Setup and Teardown never change or delete an application without it.
const appPrefix = "LydiaBox "

func bypassDomains(p SetupParams) []string {
	out := make([]string, 0, len(p.BypassPaths))
	for _, bp := range p.BypassPaths {
		out = append(out, p.Hostname+bp)
	}
	return out
}

// checkForeignApp refuses a domain that an application not made by Setup
// already protects.
func checkForeignApp(apps []Application, domain string) error {
	for _, a := range apps {
		if strings.EqualFold(a.Domain, domain) && !strings.HasPrefix(a.Name, appPrefix) {
			return fmt.Errorf("cloudflare: the Access application %q already protects %s; delete it or choose another hostname",
				a.Name, a.Domain)
		}
	}
	return nil
}

func upsertApp(ctx context.Context, c *Client, existing []Application, want Application, progress func(string, ...any)) (Application, error) {
	if err := checkForeignApp(existing, want.Domain); err != nil {
		return Application{}, err
	}
	for _, a := range existing {
		if strings.EqualFold(a.Domain, want.Domain) {
			out, err := c.UpdateApplication(ctx, a.ID, want)
			if err != nil {
				return out, err
			}
			if out.ID == "" {
				out.ID = a.ID
			}
			if out.AUD == "" {
				out.AUD = a.AUD
			}
			progress("updated Access application %s", want.Domain)
			return out, nil
		}
	}
	out, err := c.CreateApplication(ctx, want)
	if err == nil {
		progress("created Access application %s", want.Domain)
	}
	return out, err
}
