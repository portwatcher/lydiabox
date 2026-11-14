package cloudflare

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// serviceTokenDuration is the lifetime of a new service token.
const serviceTokenDuration = "8760h"

// findServiceToken returns the service token called name, or nil.
func findServiceToken(ctx context.Context, c *Client, name string) (*ServiceToken, error) {
	tokens, err := c.ServiceTokens(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tokens {
		if tokens[i].Name == name {
			return &tokens[i], nil
		}
	}
	return nil, nil
}

// setupServiceToken creates (or keeps) the service token and its policy and
// returns the policy to attach.
func setupServiceToken(ctx context.Context, c *Client, p SetupParams, n names, policies []Policy,
	prev State, prevSecrets Secrets, st *State, sec *Secrets) (*PolicyRef, error) {
	found, err := findServiceToken(ctx, c, n.token)
	if err != nil {
		return nil, err
	}
	var tok ServiceToken
	switch {
	case found != nil && found.ID == prev.ServiceTokenID && prevSecrets.ServiceTokenSecret != "":
		tok = *found
		tok.ClientSecret = prevSecrets.ServiceTokenSecret
		p.Progress("reused service token %q", n.token)
	case found != nil:
		// The secret is shown only once; without the stored copy, rotate.
		if tok, err = c.RotateServiceToken(ctx, found.ID); err != nil {
			return nil, err
		}
		tok.ID, tok.Name = found.ID, found.Name
		if tok.ClientID == "" {
			tok.ClientID = found.ClientID
		}
		p.Progress("rotated service token %q (the stored secret was missing)", n.token)
	default:
		if tok, err = c.CreateServiceToken(ctx, n.token, serviceTokenDuration); err != nil {
			return nil, err
		}
		p.Progress("created service token %q", n.token)
	}
	if tok.ClientSecret == "" {
		return nil, errors.New("cloudflare: the service token response has no client secret")
	}
	st.ServiceTokenID, st.ServiceTokenClientID = tok.ID, tok.ClientID
	sec.ServiceTokenSecret = tok.ClientSecret
	pol, err := upsertPolicy(ctx, c, policies, Policy{
		Name: n.service, Decision: DecisionNonIdentity,
		Include: []Rule{{"service_token": {"token_id": tok.ID}}},
	}, p.Progress)
	if err != nil {
		return nil, err
	}
	return &PolicyRef{ID: pol.ID, Precedence: 2}, nil
}

// removeServiceToken deletes the service token and its policy. Setup calls
// it after the application no longer references the policy.
func removeServiceToken(ctx context.Context, c *Client, n names, policies []Policy, progress func(string, ...any)) error {
	var errs []error
	if pol := policyNamed(policies, n.service); pol != nil {
		if err := c.DeletePolicy(ctx, pol.ID); err != nil && !IsNotFound(err) {
			errs = append(errs, err)
		} else {
			progress("deleted Access policy %q", n.service)
		}
	}
	found, err := findServiceToken(ctx, c, n.token)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if found != nil {
		if err := c.DeleteServiceToken(ctx, found.ID); err != nil && !IsNotFound(err) {
			errs = append(errs, err)
		} else {
			progress("deleted service token %q", n.token)
		}
	}
	return errors.Join(errs...)
}

// setupBypass makes one Access application with a Bypass policy for each
// bypass path and removes the bypass applications that are no longer wanted.
func setupBypass(ctx context.Context, c *Client, p SetupParams, n names, apps []Application, policies []Policy) error {
	prefix := p.Hostname + "/"
	if len(p.BypassPaths) == 0 {
		var errs []error
		for _, a := range bypassApps(apps, prefix) {
			errs = append(errs, deleteApp(ctx, c, a, p.Progress))
		}
		if pol := policyNamed(policies, n.bypass); pol != nil && errors.Join(errs...) == nil {
			if err := c.DeletePolicy(ctx, pol.ID); err != nil && !IsNotFound(err) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	pol, err := upsertPolicy(ctx, c, policies, Policy{
		Name: n.bypass, Decision: DecisionBypass, Include: []Rule{{"everyone": {}}},
	}, p.Progress)
	if err != nil {
		return err
	}
	for _, path := range p.BypassPaths {
		domain := p.Hostname + path
		_, err := upsertApp(ctx, c, apps, Application{
			Name: "LydiaBox " + path + " (" + p.Hostname + ", admin token only)", Domain: domain, Type: "self_hosted",
			SessionDuration: p.SessionDuration, Policies: []PolicyRef{{ID: pol.ID, Precedence: 1}},
		}, p.Progress)
		if err != nil {
			return err
		}
	}
	var errs []error
	for _, a := range bypassApps(apps, prefix) {
		if !slices.Contains(p.BypassPaths, strings.TrimPrefix(strings.ToLower(a.Domain), p.Hostname)) {
			errs = append(errs, deleteApp(ctx, c, a, p.Progress))
		}
	}
	return errors.Join(errs...)
}

// bypassApps returns the LydiaBox applications on sub-paths of a hostname.
func bypassApps(apps []Application, prefix string) []Application {
	var out []Application
	for _, a := range apps {
		if strings.HasPrefix(strings.ToLower(a.Domain), prefix) && strings.HasPrefix(a.Name, appPrefix) {
			out = append(out, a)
		}
	}
	return out
}

func deleteApp(ctx context.Context, c *Client, a Application, progress func(string, ...any)) error {
	if err := c.DeleteApplication(ctx, a.ID); err != nil && !IsNotFound(err) {
		return err
	}
	progress("deleted Access application %s", a.Domain)
	return nil
}

func policyNamed(policies []Policy, name string) *Policy {
	for i := range policies {
		if policies[i].Name == name {
			return &policies[i]
		}
	}
	return nil
}

// Teardown deletes every resource that Setup made for hostname: the Access
// applications, the LydiaBox policies, the service token, the DNS record
// (only a CNAME to the tunnel), and the tunnel. The one-time PIN login
// method stays, because other applications can use it. It continues after
// a failure and returns every error.
func Teardown(ctx context.Context, c *Client, st State, progress func(string, ...any)) error {
	if progress == nil {
		progress = func(string, ...any) {}
	}
	if st.Hostname == "" {
		return errors.New("cloudflare: nothing to tear down: no hostname")
	}
	n := namesFor(st.Hostname, st.TunnelName)
	var errs []error
	keep := func(err error) {
		if err != nil && !IsNotFound(err) {
			errs = append(errs, err)
		}
	}

	if apps, err := c.Applications(ctx); err != nil {
		keep(err)
	} else {
		for _, a := range apps {
			ours := strings.HasPrefix(a.Name, appPrefix)
			if (ours && strings.EqualFold(a.Domain, st.Hostname)) || len(bypassApps([]Application{a}, st.Hostname+"/")) == 1 {
				keep(deleteApp(ctx, c, a, progress))
			}
		}
	}
	if policies, err := c.Policies(ctx); err != nil {
		keep(err)
	} else {
		for _, name := range []string{n.owner, n.service, n.bypass} {
			if pol := policyNamed(policies, name); pol != nil {
				if err := c.DeletePolicy(ctx, pol.ID); err != nil {
					keep(err)
				} else {
					progress("deleted Access policy %q", name)
				}
			}
		}
	}
	if tokens, err := c.ServiceTokens(ctx); err != nil {
		keep(err)
	} else {
		for _, t := range tokens {
			if t.Name == n.token {
				if err := c.DeleteServiceToken(ctx, t.ID); err != nil {
					keep(err)
				} else {
					progress("deleted service token %q", n.token)
				}
			}
		}
	}
	if err := removeTURN(ctx, c, n, progress); err != nil {
		if st.TURNKeyID != "" {
			keep(err)
		} else {
			// No stored key: the token may lack the Realtime permission.
			progress("skipped the TURN key check: %v", err)
		}
	}
	tunnel, err := c.FindTunnel(ctx, n.tunnel)
	keep(err)
	tunnelID := st.TunnelID
	if tunnel != nil {
		tunnelID = tunnel.ID
	}
	keep(teardownDNS(ctx, c, st, tunnelID, progress))
	if tunnel != nil {
		if err := c.DeleteTunnel(ctx, tunnel.ID); err != nil {
			keep(err)
		} else {
			progress("deleted tunnel %s (%s)", tunnel.Name, tunnel.ID)
		}
	}
	return errors.Join(errs...)
}

func teardownDNS(ctx context.Context, c *Client, st State, tunnelID string, progress func(string, ...any)) error {
	if tunnelID == "" {
		return nil
	}
	zoneID := st.ZoneID
	if zoneID == "" {
		z, err := c.FindZone(ctx, st.Hostname, st.ZoneName)
		if err != nil {
			return err
		}
		zoneID = z.ID
	}
	recs, err := c.DNSRecords(ctx, zoneID, st.Hostname)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if strings.EqualFold(r.Type, "CNAME") && strings.EqualFold(r.Content, TunnelTarget(tunnelID)) {
			if err := c.DeleteDNSRecord(ctx, zoneID, r.ID); err != nil {
				return err
			}
			progress("deleted DNS record %s", st.Hostname)
		}
	}
	return nil
}
