package accessjwt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/cloudflare/accessjwt"
	"github.com/lydiabox/lydiabox/internal/cloudflare/accessjwt/accessjwttest"
)

func newVerifier(t *testing.T, j *accessjwttest.Issuer, now *time.Time) *accessjwt.Verifier {
	t.Helper()
	v, err := accessjwt.New(accessjwt.Config{
		TeamDomain: "https://" + accessjwttest.TeamDomain + "/", AUD: accessjwttest.AUD,
		Emails: []string{"Me@Example.com", "@family.example"}, CertsURL: j.CertsURL(),
		Now: func() time.Time { return *now }, MinRefresh: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerify(t *testing.T) {
	j := accessjwttest.NewIssuer(t)
	now := time.Now()
	v := newVerifier(t, j, &now)
	ctx := context.Background()

	cases := []struct {
		name   string
		claims func(c map[string]any)
		want   error
	}{
		{"valid", nil, nil},
		{"domain allow list", func(c map[string]any) { c["email"] = "kid@family.example" }, nil},
		{"aud array", func(c map[string]any) { c["aud"] = []string{"other", accessjwttest.AUD} }, nil},
		{"expired", func(c map[string]any) { c["exp"] = now.Add(-2 * time.Minute).Unix() }, accessjwt.ErrInvalid},
		{"no expiry", func(c map[string]any) { delete(c, "exp") }, accessjwt.ErrInvalid},
		{"not yet valid", func(c map[string]any) { c["nbf"] = now.Add(5 * time.Minute).Unix() }, accessjwt.ErrInvalid},
		{"wrong audience", func(c map[string]any) { c["aud"] = []string{"other-app"} }, accessjwt.ErrInvalid},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://evil.cloudflareaccess.com" }, accessjwt.ErrInvalid},
		{"email not allowed", func(c map[string]any) { c["email"] = "eve@example.com" }, accessjwt.ErrNotAllowed},
		{"look-alike domain", func(c map[string]any) { c["email"] = "eve@evilfamily.example" }, accessjwt.ErrNotAllowed},
		{"service token", func(c map[string]any) { delete(c, "email"); c["common_name"] = "id.access" }, accessjwt.ErrNoEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := j.Claims(now)
			if tc.claims != nil {
				tc.claims(claims)
			}
			email, err := v.VerifyAccess(ctx, j.Sign(t, claims))
			if tc.want == nil {
				if err != nil || email == "" {
					t.Fatalf("want success, got %q, %v", email, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			// Only a valid token of a not-allowed email reports its email.
			if errors.Is(err, accessjwt.ErrNotAllowed) != (email != "") {
				t.Fatalf("email %q with %v", email, err)
			}
		})
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	j := accessjwttest.NewIssuer(t)
	now := time.Now()
	v := newVerifier(t, j, &now)
	ctx := context.Background()
	good := j.Sign(t, j.Claims(now))

	other := j.Claims(now)
	other["email"] = "eve@example.com"
	forged := j.Sign(t, other)
	// Keep the payload of an allowed token but the signature of another.
	tampered := accessjwttest.Part(good, 0) + "." + accessjwttest.Part(good, 1) + "." + accessjwttest.Part(forged, 2)
	if _, err := v.Verify(ctx, tampered); !errors.Is(err, accessjwt.ErrInvalid) {
		t.Fatalf("tampered signature: %v", err)
	}
	swapped := accessjwttest.Part(good, 0) + "." + accessjwttest.Part(forged, 1) + "." + accessjwttest.Part(good, 2)
	if _, err := v.Verify(ctx, swapped); !errors.Is(err, accessjwt.ErrInvalid) {
		t.Fatalf("tampered payload: %v", err)
	}
	for _, bad := range []string{"", "a.b", "a.b.c", j.SignWith(t, "HS256", j.Claims(now)), j.SignWith(t, "none", j.Claims(now))} {
		if _, err := v.Verify(ctx, bad); !errors.Is(err, accessjwt.ErrInvalid) {
			t.Fatalf("token %q: %v", bad, err)
		}
	}
	// A key that is not in the JWKS fails even with a valid structure.
	if _, err := v.Verify(ctx, accessjwttest.NewIssuer(t).Sign(t, j.Claims(now))); !errors.Is(err, accessjwt.ErrInvalid) {
		t.Fatalf("foreign key: %v", err)
	}
}

func TestUnknownKeyRefreshesJWKS(t *testing.T) {
	j := accessjwttest.NewIssuer(t)
	now := time.Now()
	v := newVerifier(t, j, &now)
	ctx := context.Background()
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); err != nil {
		t.Fatal(err)
	}
	if j.Fetches() != 1 {
		t.Fatalf("fetches = %d", j.Fetches())
	}
	// Known keys come from the cache.
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); err != nil || j.Fetches() != 1 {
		t.Fatalf("cached verify: %v, fetches %d", err, j.Fetches())
	}

	// Cloudflare rotates the key; inside MinRefresh the new kid is refused.
	j.Rotate(t)
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); !errors.Is(err, accessjwt.ErrInvalid) || j.Fetches() != 1 {
		t.Fatalf("want rate-limited refresh, got %v after %d fetches", err, j.Fetches())
	}
	now = now.Add(11 * time.Second)
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if j.Fetches() != 2 {
		t.Fatalf("fetches = %d, want 2", j.Fetches())
	}
	// A random kid does not cause another fetch inside MinRefresh.
	if _, err := v.Verify(ctx, j.SignKid(t, "random", j.Claims(now))); err == nil || j.Fetches() != 2 {
		t.Fatalf("random kid: %v, fetches %d", err, j.Fetches())
	}
	// After MaxAge (1h) the set is refetched on the next request.
	now = now.Add(time.Hour)
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); err != nil || j.Fetches() != 3 {
		t.Fatalf("max age refresh: %v, fetches %d", err, j.Fetches())
	}
}

func TestCanceledRequestDoesNotBreakFirstFetch(t *testing.T) {
	j := accessjwttest.NewIssuer(t)
	now := time.Now()
	v := newVerifier(t, j, &now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); err != nil {
		t.Fatalf("a canceled request context must not cancel the JWKS fetch: %v", err)
	}
}

func TestKnownKeySurvivesJWKSOutage(t *testing.T) {
	j := accessjwttest.NewIssuer(t)
	now := time.Now()
	v := newVerifier(t, j, &now)
	ctx := context.Background()
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); err != nil {
		t.Fatal(err)
	}
	j.Fail(true)
	now = now.Add(2 * time.Hour)
	if _, err := v.Verify(ctx, j.Sign(t, j.Claims(now))); err != nil {
		t.Fatalf("known key during outage: %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	bad := []accessjwt.Config{
		{AUD: "a", Emails: []string{"a@b.co"}},
		{TeamDomain: "t.cloudflareaccess.com", Emails: []string{"a@b.co"}},
		{TeamDomain: "t.cloudflareaccess.com", AUD: "a"},
	}
	for i, c := range bad {
		if _, err := accessjwt.New(c); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
	if got := accessjwt.NormalizeTeamDomain(" HTTPS://Team.CloudflareAccess.com/x "); got != "team.cloudflareaccess.com" {
		t.Fatal(got)
	}
}
