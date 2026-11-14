package cloudflare_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/cloudflare"
	"github.com/lydiabox/lydiabox/internal/cloudflare/cloudflaretest"
)

func params() cloudflare.SetupParams {
	return cloudflare.SetupParams{
		Hostname: "Agent.Example.com", Emails: []string{"Me@Example.com"},
		OriginURL: "http://127.0.0.1:8080", ServiceToken: true,
	}
}

func TestSetupCreatesEverything(t *testing.T) {
	f := cloudflaretest.New(t)
	var lines []string
	p := params()
	p.Progress = func(format string, args ...any) { lines = append(lines, format) }
	st, sec, err := cloudflare.Setup(context.Background(), f.Client(t), p, cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Hostname != "agent.example.com" || st.TeamDomain != "team.cloudflareaccess.com" || st.AccountID != cloudflaretest.Account {
		t.Fatalf("state = %+v", st)
	}
	if st.TunnelName != "lydiabox-agent-example-com" || st.TunnelID == "" || sec.TunnelToken != "token-"+st.TunnelID {
		t.Fatalf("tunnel state = %+v, secrets = %+v", st, sec)
	}
	cfg := f.Configs[st.TunnelID]
	if len(cfg.Ingress) != 2 || cfg.Ingress[0].Hostname != "agent.example.com" ||
		cfg.Ingress[0].Service != "http://127.0.0.1:8080" || cfg.Ingress[1].Service != "http_status:404" {
		t.Fatalf("ingress = %+v", cfg.Ingress)
	}
	rec := f.Records[st.DNSRecordID]
	if rec.Type != "CNAME" || rec.Content != st.TunnelID+".cfargotunnel.com" || !rec.Proxied || st.ZoneName != "example.com" {
		t.Fatalf("DNS record = %+v", rec)
	}
	if len(f.IDPs) != 1 || f.IDPs[0].Type != cloudflare.IdentityProviderOTP {
		t.Fatalf("idps = %+v", f.IDPs)
	}
	owner := f.Policies[st.OwnerPolicyID]
	if owner.Decision != cloudflare.DecisionAllow || len(owner.Include) != 1 || owner.Include[0]["email"]["email"] != "me@example.com" {
		t.Fatalf("owner policy = %+v", owner)
	}
	app := f.Apps[st.AppID]
	if app.Domain != "agent.example.com" || app.Type != "self_hosted" || st.AUD != "aud-"+st.AppID || app.SessionDuration != "24h" {
		t.Fatalf("app = %+v, aud %q", app, st.AUD)
	}
	if len(app.Policies) != 2 || app.Policies[0].ID != st.OwnerPolicyID || app.Policies[1].Precedence != 2 {
		t.Fatalf("app policies = %+v", app.Policies)
	}
	svcPolicy := f.Policies[app.Policies[1].ID]
	if svcPolicy.Decision != cloudflare.DecisionNonIdentity || svcPolicy.Include[0]["service_token"]["token_id"] != st.ServiceTokenID {
		t.Fatalf("service policy = %+v", svcPolicy)
	}
	if st.ServiceTokenClientID == "" || sec.ServiceTokenSecret != "secret-"+st.ServiceTokenID {
		t.Fatalf("service token = %+v / %+v", st, sec)
	}
	if len(lines) == 0 {
		t.Fatal("no progress lines")
	}
}

func TestSetupIsIdempotent(t *testing.T) {
	f := cloudflaretest.New(t)
	c := f.Client(t)
	ctx := context.Background()
	st, sec, err := cloudflare.Setup(ctx, c, params(), cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.Emails = []string{"me@example.com", "@family.example"}
	st2, sec2, err := cloudflare.Setup(ctx, c, p, st, sec)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tunnels) != 1 || len(f.Records) != 1 || len(f.Apps) != 1 || len(f.Policies) != 2 || len(f.ServiceTokens) != 1 || len(f.IDPs) != 1 {
		t.Fatalf("duplicates: tunnels %d records %d apps %d policies %d tokens %d idps %d",
			len(f.Tunnels), len(f.Records), len(f.Apps), len(f.Policies), len(f.ServiceTokens), len(f.IDPs))
	}
	if st2.TunnelID != st.TunnelID || st2.AppID != st.AppID || st2.AUD != st.AUD || st2.DNSRecordID != st.DNSRecordID {
		t.Fatalf("IDs changed: %+v -> %+v", st, st2)
	}
	if sec2.ServiceTokenSecret != sec.ServiceTokenSecret || f.Calls("POST /accounts/acct1/access/service_tokens/") != 0 {
		t.Fatal("a stored service token secret must be reused, not rotated")
	}
	owner := f.Policies[st2.OwnerPolicyID]
	if len(owner.Include) != 2 || owner.Include[1]["email_domain"]["domain"] != "family.example" {
		t.Fatalf("owner policy not updated: %+v", owner.Include)
	}
	if f.Calls("POST /accounts/acct1/cfd_tunnel") != 1 {
		t.Fatal("the tunnel was created twice")
	}
}

func TestSetupRotatesServiceTokenWithoutStoredSecret(t *testing.T) {
	f := cloudflaretest.New(t)
	c := f.Client(t)
	st, sec, err := cloudflare.Setup(context.Background(), c, params(), cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	// The local state is lost (for example a new data directory).
	st2, sec2, err := cloudflare.Setup(context.Background(), c, params(), cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if st2.ServiceTokenID != st.ServiceTokenID || sec2.ServiceTokenSecret == "" || sec2.ServiceTokenSecret == sec.ServiceTokenSecret {
		t.Fatalf("want rotated secret of the same token, got %+v %+v", st2, sec2)
	}
	if st2.ServiceTokenClientID != st.ServiceTokenClientID {
		t.Fatalf("client ID changed: %q -> %q", st.ServiceTokenClientID, st2.ServiceTokenClientID)
	}
}

func TestSetupRemovesDisabledServiceToken(t *testing.T) {
	f := cloudflaretest.New(t)
	c := f.Client(t)
	st, sec, err := cloudflare.Setup(context.Background(), c, params(), cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	p := params()
	p.ServiceToken = false
	st2, sec2, err := cloudflare.Setup(context.Background(), c, p, st, sec)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.ServiceTokens) != 0 || len(f.Policies) != 1 || st2.ServiceTokenID != "" || sec2.ServiceTokenSecret != "" {
		t.Fatalf("service token not removed: tokens %d policies %d state %+v", len(f.ServiceTokens), len(f.Policies), st2)
	}
	if refs := f.Apps[st2.AppID].Policies; len(refs) != 1 || refs[0].ID != st2.OwnerPolicyID {
		t.Fatalf("app policies = %+v", refs)
	}
}

func TestSetupBypassPaths(t *testing.T) {
	f := cloudflaretest.New(t)
	c := f.Client(t)
	ctx := context.Background()
	p := params()
	p.BypassPaths = []string{"s3", "/webdav/", "/s3"}
	st, sec, err := cloudflare.Setup(ctx, c, p, cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.BypassPaths, []string{"/s3", "/webdav"}) {
		t.Fatalf("bypass paths = %v", st.BypassPaths)
	}
	domains := func() []string {
		var out []string
		for _, a := range f.Apps {
			out = append(out, a.Domain)
		}
		slices.Sort(out)
		return out
	}
	if got := domains(); !slices.Equal(got, []string{"agent.example.com", "agent.example.com/s3", "agent.example.com/webdav"}) {
		t.Fatalf("apps = %v", got)
	}
	var bypass *cloudflare.Policy
	for _, pol := range f.Policies {
		if pol.Decision == cloudflare.DecisionBypass {
			bypass = &pol
		}
	}
	if bypass == nil || bypass.Include[0]["everyone"] == nil {
		t.Fatalf("bypass policy missing: %+v", f.Policies)
	}

	p.BypassPaths = []string{"/webdav"}
	if st, sec, err = cloudflare.Setup(ctx, c, p, st, sec); err != nil {
		t.Fatal(err)
	}
	if got := domains(); !slices.Equal(got, []string{"agent.example.com", "agent.example.com/webdav"}) {
		t.Fatalf("apps after removing /s3 = %v", got)
	}
	p.BypassPaths = nil
	if _, _, err = cloudflare.Setup(ctx, c, p, st, sec); err != nil {
		t.Fatal(err)
	}
	if got := domains(); len(got) != 1 || len(f.Policies) != 2 {
		t.Fatalf("bypass not removed: apps %v policies %d", got, len(f.Policies))
	}
}

func TestSetupFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("no Zero Trust organization", func(t *testing.T) {
		f := cloudflaretest.New(t)
		f.NoOrg = true
		_, _, err := cloudflare.Setup(ctx, f.Client(t), params(), cloudflare.State{}, cloudflare.Secrets{})
		if !cloudflare.IsNotFound(err) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("tunnel create fails", func(t *testing.T) {
		f := cloudflaretest.New(t)
		f.Fail["POST /accounts/acct1/cfd_tunnel"] = http.StatusInternalServerError
		_, _, err := cloudflare.Setup(ctx, f.Client(t), params(), cloudflare.State{}, cloudflare.Secrets{})
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "fake failure") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("hostname has a foreign record", func(t *testing.T) {
		f := cloudflaretest.New(t)
		f.Records["old"] = cloudflare.DNSRecord{ID: "old", Type: "A", Name: "agent.example.com", Content: "192.0.2.1", Proxied: true}
		_, _, err := cloudflare.Setup(ctx, f.Client(t), params(), cloudflare.State{}, cloudflare.Secrets{})
		if err == nil || !strings.Contains(err.Error(), "already has a A record") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("record not proxied", func(t *testing.T) {
		f := cloudflaretest.New(t)
		st, sec, err := cloudflare.Setup(ctx, f.Client(t), params(), cloudflare.State{}, cloudflare.Secrets{})
		if err != nil {
			t.Fatal(err)
		}
		rec := f.Records[st.DNSRecordID]
		rec.Proxied = false
		f.Records[st.DNSRecordID] = rec
		if _, _, err := cloudflare.Setup(ctx, f.Client(t), params(), st, sec); err == nil || !strings.Contains(err.Error(), "not proxied") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no zone", func(t *testing.T) {
		f := cloudflaretest.New(t)
		p := params()
		p.Hostname = "agent.other.org"
		if _, _, err := cloudflare.Setup(ctx, f.Client(t), p, cloudflare.State{}, cloudflare.Secrets{}); err == nil || !strings.Contains(err.Error(), "no zone") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("foreign Access application", func(t *testing.T) {
		f := cloudflaretest.New(t)
		f.Apps["mine"] = cloudflare.Application{ID: "mine", Name: "My app", Domain: "agent.example.com", AUD: "x"}
		_, _, err := cloudflare.Setup(ctx, f.Client(t), params(), cloudflare.State{}, cloudflare.Secrets{})
		if err == nil || !strings.Contains(err.Error(), "already protects") || f.Apps["mine"].Name != "My app" {
			t.Fatalf("err = %v, app = %+v", err, f.Apps["mine"])
		}
		if len(f.Tunnels) != 0 || len(f.Records) != 0 {
			t.Fatal("setup must stop before it creates anything")
		}
		// Teardown leaves the foreign application alone.
		if err := cloudflare.Teardown(ctx, f.Client(t), cloudflare.State{Hostname: "agent.example.com"}, nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.Apps["mine"]; !ok {
			t.Fatal("teardown deleted a foreign application")
		}
	})
	t.Run("other hostname already set up", func(t *testing.T) {
		f := cloudflaretest.New(t)
		_, _, err := cloudflare.Setup(ctx, f.Client(t), params(), cloudflare.State{Hostname: "old.example.com"}, cloudflare.Secrets{})
		if err == nil || !strings.Contains(err.Error(), "teardown") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad token", func(t *testing.T) {
		f := cloudflaretest.New(t)
		c, _ := cloudflare.NewClient(cloudflare.Options{BaseURL: f.URL(), Token: "wrong", AccountID: cloudflaretest.Account})
		if _, _, err := cloudflare.Setup(ctx, c, params(), cloudflare.State{}, cloudflare.Secrets{}); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNormalize(t *testing.T) {
	bad := []cloudflare.SetupParams{
		{Hostname: "localhost", Emails: []string{"a@b.co"}, OriginURL: "http://127.0.0.1:8080"},
		{Hostname: "a.example.com", Emails: nil, OriginURL: "http://127.0.0.1:8080"},
		{Hostname: "a.example.com", Emails: []string{"not an email"}, OriginURL: "http://127.0.0.1:8080"},
		{Hostname: "a.example.com", Emails: []string{"Me <me@example.com>"}, OriginURL: "http://127.0.0.1:8080"},
		{Hostname: "a.example.com", Emails: []string{"a@b.co"}, OriginURL: "127.0.0.1:8080"},
		{Hostname: "a.example.com", Emails: []string{"a@b.co"}, OriginURL: "http://127.0.0.1:8080", BypassPaths: []string{"/"}},
		{Hostname: "a.example.com", Emails: []string{"a@b.co"}, OriginURL: "http://127.0.0.1:8080", SessionDuration: "1 day"},
	}
	for i, p := range bad {
		if err := p.Normalize(); err == nil {
			t.Errorf("case %d: want error for %+v", i, p)
		}
	}
	emails, err := cloudflare.NormalizeEmails([]string{" A@B.co, a@b.co ", "@Example.org"})
	if err != nil || !slices.Equal(emails, []string{"a@b.co", "@example.org"}) {
		t.Fatalf("emails = %v, %v", emails, err)
	}
}

func TestTeardown(t *testing.T) {
	f := cloudflaretest.New(t)
	c := f.Client(t)
	ctx := context.Background()
	p := params()
	p.BypassPaths = []string{"/webdav"}
	st, _, err := cloudflare.Setup(ctx, c, p, cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated application and record must survive.
	f.Apps["other"] = cloudflare.Application{ID: "other", Name: "Other", Domain: "other.example.com"}
	f.Records["mx"] = cloudflare.DNSRecord{ID: "mx", Type: "MX", Name: "example.com", Content: "mail.example.com"}
	if err := cloudflare.Teardown(ctx, c, st, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.Tunnels) != 0 || len(f.Records) != 1 || len(f.Apps) != 1 || len(f.Policies) != 0 || len(f.ServiceTokens) != 0 {
		t.Fatalf("left over: tunnels %d records %d apps %d policies %d tokens %d",
			len(f.Tunnels), len(f.Records), len(f.Apps), len(f.Policies), len(f.ServiceTokens))
	}
	if len(f.IDPs) != 1 {
		t.Fatal("the one-time PIN login method must stay")
	}
	// A second teardown finds nothing and succeeds.
	if err := cloudflare.Teardown(ctx, c, st, nil); err != nil {
		t.Fatal(err)
	}
	if err := cloudflare.Teardown(ctx, c, cloudflare.State{}, nil); err == nil {
		t.Fatal("want error without a hostname")
	}
}

func TestTeardownContinuesAfterFailure(t *testing.T) {
	f := cloudflaretest.New(t)
	c := f.Client(t)
	st, _, err := cloudflare.Setup(context.Background(), c, params(), cloudflare.State{}, cloudflare.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	f.Fail["DELETE /accounts/acct1/access/apps/"] = http.StatusInternalServerError
	err = cloudflare.Teardown(context.Background(), c, st, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	if len(f.Tunnels) != 0 || len(f.Records) != 0 {
		t.Fatal("teardown must continue after one failure")
	}
}

func TestListPaginates(t *testing.T) {
	f := cloudflaretest.New(t)
	for i := 0; i < 120; i++ {
		f.Policies[f.ID("p")] = cloudflare.Policy{Name: "x"}
	}
	got, err := f.Client(t).Policies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 120 || f.Calls("GET /accounts/acct1/access/policies") != 3 {
		t.Fatalf("got %d policies in %d calls", len(got), f.Calls("GET /accounts/acct1/access/policies"))
	}
}

func TestNewClientValidates(t *testing.T) {
	if _, err := cloudflare.NewClient(cloudflare.Options{AccountID: "a"}); err == nil {
		t.Fatal("want token error")
	}
	if _, err := cloudflare.NewClient(cloudflare.Options{Token: "t"}); err == nil {
		t.Fatal("want account error")
	}
}
