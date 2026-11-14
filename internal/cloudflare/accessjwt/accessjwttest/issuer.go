// Package accessjwttest is a fake Cloudflare Access issuer for tests: an
// RSA key, a JWKS server, and a token signer.
package accessjwttest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test values of the fake team.
const (
	TeamDomain = "team.cloudflareaccess.com"
	AUD        = "aud-tag-1234"
	Email      = "me@example.com"
)

// Issuer signs Access tokens and serves its JWKS.
type Issuer struct {
	srv *httptest.Server

	mu      sync.Mutex
	key     *rsa.PrivateKey
	kid     int
	fetches int
	fail    bool
}

// NewIssuer starts a JWKS server with a fresh 2048-bit key.
func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	j := &Issuer{}
	j.Rotate(t)
	j.srv = httptest.NewServer(http.HandlerFunc(j.serve))
	t.Cleanup(j.srv.Close)
	return j
}

// CertsURL is the JWKS URL.
func (j *Issuer) CertsURL() string { return j.srv.URL + "/cdn-cgi/access/certs" }

// Rotate replaces the signing key and its key ID.
func (j *Issuer) Rotate(t testing.TB) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	j.mu.Lock()
	j.key = k
	j.kid++
	j.mu.Unlock()
}

// Fetches counts JWKS requests.
func (j *Issuer) Fetches() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.fetches
}

// Fail makes the JWKS endpoint answer 503.
func (j *Issuer) Fail(v bool) {
	j.mu.Lock()
	j.fail = v
	j.mu.Unlock()
}

func (j *Issuer) kidString() string { return fmt.Sprintf("kid-%d", j.kid) }

func (j *Issuer) serve(w http.ResponseWriter, r *http.Request) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.fetches++
	if j.fail || r.URL.Path != "/cdn-cgi/access/certs" {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	pub := j.key.PublicKey
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
		"kid": j.kidString(), "kty": "RSA", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// Claims returns valid claims for Email at now.
func (j *Issuer) Claims(now time.Time) map[string]any {
	return map[string]any{
		"aud": []string{AUD}, "email": Email, "exp": now.Add(10 * time.Minute).Unix(),
		"iat": now.Unix(), "nbf": now.Unix(), "iss": "https://" + TeamDomain,
		"type": "app", "sub": "user-1", "identity_nonce": "n",
	}
}

// Sign signs claims with the current key and key ID.
func (j *Issuer) Sign(t testing.TB, claims map[string]any) string {
	j.mu.Lock()
	kid := j.kidString()
	j.mu.Unlock()
	return j.sign(t, "RS256", kid, claims)
}

// SignKid signs with the current key under another key ID.
func (j *Issuer) SignKid(t testing.TB, kid string, claims map[string]any) string {
	return j.sign(t, "RS256", kid, claims)
}

// SignWith signs with the current key but writes alg into the header.
func (j *Issuer) SignWith(t testing.TB, alg string, claims map[string]any) string {
	j.mu.Lock()
	kid := j.kidString()
	j.mu.Unlock()
	return j.sign(t, alg, kid, claims)
}

func (j *Issuer) sign(t testing.TB, alg, kid string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	p, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(signing))
	j.mu.Lock()
	sig, err := rsa.SignPKCS1v15(rand.Reader, j.key, crypto.SHA256, sum[:])
	j.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Part returns one dot-separated part of a token.
func Part(token string, i int) string {
	parts := strings.Split(token, ".")
	if i >= len(parts) {
		return ""
	}
	return parts[i]
}
