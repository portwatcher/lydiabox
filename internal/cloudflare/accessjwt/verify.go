// Package accessjwt verifies the Cloudflare Access application token that
// Access adds to every request it lets through (the Cf-Access-Jwt-Assertion
// header and the CF_Authorization cookie). It uses only the standard
// library: RS256 signatures against the team JWKS, then issuer, audience,
// expiry, and the email allow list.
package accessjwt

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Header is the request header that carries the Access token.
const Header = "Cf-Access-Jwt-Assertion"

// Cookie is the browser cookie that carries the Access token.
const Cookie = "CF_Authorization"

// Errors of Verify. Every failure wraps ErrInvalid.
var (
	ErrInvalid    = errors.New("accessjwt: invalid Access token")
	ErrNotAllowed = fmt.Errorf("%w: email is not on the allow list", ErrInvalid)
	ErrNoEmail    = fmt.Errorf("%w: token has no email (service token)", ErrInvalid)
)

// Config configures a Verifier.
type Config struct {
	// TeamDomain is the Zero Trust team domain, for example
	// myteam.cloudflareaccess.com. A scheme and a trailing slash are ignored.
	TeamDomain string
	// AUD is the audience tag of the Access application.
	AUD string
	// Emails is the allow list; "@example.com" allows a whole domain.
	Emails []string
	// CertsURL overrides https://<team>/cdn-cgi/access/certs (tests).
	CertsURL   string
	HTTPClient *http.Client
	Now        func() time.Time
	// MinRefresh is the shortest time between two JWKS fetches (default 30s).
	MinRefresh time.Duration
	// MaxAge refetches the JWKS on the next request after this time
	// (default 1h), so rotated keys are picked up without a restart.
	MaxAge time.Duration
	// Leeway tolerates clock skew on exp, nbf, and iat (default 60s).
	Leeway time.Duration
}

// Verifier checks Access tokens. It is safe for concurrent use.
type Verifier struct {
	issuer string
	aud    string
	emails []string
	now    func() time.Time
	leeway time.Duration
	keys   *keySet
}

// NormalizeTeamDomain strips a scheme, a path, and a trailing slash.
func NormalizeTeamDomain(d string) string {
	d = strings.TrimSpace(strings.ToLower(d))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	if i := strings.IndexByte(d, '/'); i >= 0 {
		d = d[:i]
	}
	return d
}

// New validates cfg.
func New(cfg Config) (*Verifier, error) {
	team := NormalizeTeamDomain(cfg.TeamDomain)
	if team == "" || strings.ContainsAny(team, " :@") {
		return nil, fmt.Errorf("accessjwt: invalid team domain %q", cfg.TeamDomain)
	}
	if strings.TrimSpace(cfg.AUD) == "" {
		return nil, errors.New("accessjwt: audience tag is required")
	}
	var emails []string
	for _, e := range cfg.Emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			emails = append(emails, e)
		}
	}
	if len(emails) == 0 {
		return nil, errors.New("accessjwt: the email allow list is empty")
	}
	if cfg.CertsURL == "" {
		cfg.CertsURL = "https://" + team + "/cdn-cgi/access/certs"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MinRefresh <= 0 {
		cfg.MinRefresh = 30 * time.Second
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = time.Hour
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = time.Minute
	}
	return &Verifier{
		issuer: "https://" + team,
		aud:    strings.TrimSpace(cfg.AUD),
		emails: emails,
		now:    cfg.Now,
		leeway: cfg.Leeway,
		keys: &keySet{url: cfg.CertsURL, client: cfg.HTTPClient, now: cfg.Now,
			minRefresh: cfg.MinRefresh, maxAge: cfg.MaxAge},
	}, nil
}

// TeamDomain returns the normalized team domain.
func (v *Verifier) TeamDomain() string { return strings.TrimPrefix(v.issuer, "https://") }

// Claims are the verified claims of an Access token.
type Claims struct {
	Email      string   `json:"email"`
	Subject    string   `json:"sub"`
	Issuer     string   `json:"iss"`
	Audience   audience `json:"aud"`
	Expiry     float64  `json:"exp"`
	NotBefore  float64  `json:"nbf"`
	IssuedAt   float64  `json:"iat"`
	Type       string   `json:"type"`
	CommonName string   `json:"common_name"`
}

// audience accepts the "aud" claim as a string or an array.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// Verify checks the token and returns its claims. The email must be on the
// allow list; a service token (no email) returns ErrNoEmail.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16<<10 {
		return Claims{}, fmt.Errorf("%w: malformed", ErrInvalid)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := decodePart(parts[0], &header); err != nil {
		return Claims{}, fmt.Errorf("%w: header: %v", ErrInvalid, err)
	}
	if header.Alg != "RS256" {
		return Claims{}, fmt.Errorf("%w: algorithm %q is not RS256", ErrInvalid, header.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: signature encoding", ErrInvalid)
	}
	key, err := v.keys.key(ctx, header.Kid)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return Claims{}, fmt.Errorf("%w: bad signature", ErrInvalid)
	}
	var c Claims
	if err := decodePart(parts[1], &c); err != nil {
		return Claims{}, fmt.Errorf("%w: claims: %v", ErrInvalid, err)
	}
	if err := v.checkClaims(c); err != nil {
		return c, err
	}
	return c, nil
}

func (v *Verifier) checkClaims(c Claims) error {
	if c.Issuer != v.issuer {
		return fmt.Errorf("%w: issuer %q", ErrInvalid, c.Issuer)
	}
	if !slices.Contains(c.Audience, v.aud) {
		return fmt.Errorf("%w: audience does not match the application", ErrInvalid)
	}
	now := v.now()
	if c.Expiry == 0 || now.After(unix(c.Expiry).Add(v.leeway)) {
		return fmt.Errorf("%w: expired", ErrInvalid)
	}
	if c.NotBefore != 0 && now.Add(v.leeway).Before(unix(c.NotBefore)) {
		return fmt.Errorf("%w: not valid yet", ErrInvalid)
	}
	if c.IssuedAt != 0 && now.Add(v.leeway).Before(unix(c.IssuedAt)) {
		return fmt.Errorf("%w: issued in the future", ErrInvalid)
	}
	if c.Email == "" {
		return ErrNoEmail
	}
	if !v.Allowed(c.Email) {
		return ErrNotAllowed
	}
	return nil
}

// Allowed reports whether email is on the allow list.
func (v *Verifier) Allowed(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at <= 0 {
		return false
	}
	for _, e := range v.emails {
		if e == email || (strings.HasPrefix(e, "@") && e == email[at:]) {
			return true
		}
	}
	return false
}

// VerifyAccess returns the email of a token (api.AccessVerifier). The
// error is nil only for an allowed email. With ErrNotAllowed, the email of
// the otherwise valid token is returned too, so the web UI can say who is
// signed in at Cloudflare; every other error returns no email.
func (v *Verifier) VerifyAccess(ctx context.Context, token string) (string, error) {
	c, err := v.Verify(ctx, token)
	if err != nil && !errors.Is(err, ErrNotAllowed) {
		return "", err
	}
	return strings.ToLower(c.Email), err
}

func unix(f float64) time.Time { return time.Unix(int64(f), 0) }

func decodePart(part string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
