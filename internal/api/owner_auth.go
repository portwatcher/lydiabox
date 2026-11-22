package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// OwnerAssertionHeader carries the owner assertion of the control plane
// of a hosted cell (LydiaBox Cloud in its OpenID Connect mode).
const OwnerAssertionHeader = "X-Lydiabox-Owner-Assertion"

// Rules of an owner assertion. The format and the key derivation are
// defined by the control plane (lydiabox-cloud internal/access/owner):
// "v1.<payload>.<mac>", payload = base64url JSON {"aud", "tid", "email",
// "iat", "exp"}, mac = base64url HMAC-SHA256(key, ownerMACPrefix+payload).
const (
	ownerAudience    = "lydiabox-cell"
	ownerMACPrefix   = "lydiabox owner assertion v1."
	ownerMaxLifetime = 60 // seconds
	ownerMaxSkew     = 5  // seconds
	ownerMaxLength   = 4096
)

type ownerLogin struct {
	key       []byte
	tenantID  string
	email     string
	logoutURL string
	now       func() time.Time
	log       *slog.Logger
	lastWarn  atomic.Int64
}

// EnableOwnerAssertion turns on the owner login of a hosted cell: a
// request with a valid owner assertion of the control plane for this
// tenant and the owner email is the admin. key is the 32-byte HMAC key of
// this cell. The control plane removes the header from every client
// request and makes it only after it verified the owner. Call it before
// the server starts.
func (a *Authenticator) EnableOwnerAssertion(key []byte, tenantID, email, logoutURL string, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	a.owner = &ownerLogin{key: append([]byte(nil), key...), tenantID: tenantID, email: strings.ToLower(email),
		logoutURL: logoutURL, now: time.Now, log: log.With("component", "owner-assertion")}
}

// ownerClaims is the payload of an assertion.
type ownerClaims struct {
	Audience string `json:"aud"`
	TenantID string `json:"tid"`
	Email    string `json:"email"`
	IssuedAt int64  `json:"iat"`
	Expiry   int64  `json:"exp"`
}

// ownerEmail returns the owner email when r has a valid assertion, else
// "". It fails closed: no login, no header, more than one header, a bad
// MAC, another audience, tenant, or owner, an expired assertion, or one
// that lives longer than ownerMaxLifetime.
func (a *Authenticator) ownerEmail(r *http.Request) string {
	o := a.owner
	if o == nil || len(o.key) == 0 {
		return ""
	}
	vals := r.Header.Values(OwnerAssertionHeader)
	if len(vals) == 0 {
		return ""
	}
	if len(vals) != 1 {
		o.warn("more than one assertion")
		return ""
	}
	if reason := o.verify(vals[0]); reason != "" {
		o.warn(reason)
		return ""
	}
	return o.email
}

// verify returns "" for a valid assertion, else the reason.
func (o *ownerLogin) verify(token string) string {
	parts := strings.Split(token, ".")
	if len(token) > ownerMaxLength || len(parts) != 3 || parts[0] != "v1" {
		return "malformed"
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	m := hmac.New(sha256.New, o.key)
	m.Write([]byte(ownerMACPrefix + parts[1]))
	if err != nil || !hmac.Equal(got, m.Sum(nil)) {
		return "bad signature"
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "malformed payload"
	}
	var c ownerClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return "malformed payload"
	}
	now := o.now().Unix()
	switch {
	case !hmac.Equal([]byte(c.Audience), []byte(ownerAudience)):
		return "wrong audience"
	case !hmac.Equal([]byte(c.TenantID), []byte(o.tenantID)):
		return "wrong tenant"
	case o.email == "" || !hmac.Equal([]byte(c.Email), []byte(o.email)):
		return "wrong owner"
	case c.Expiry <= now:
		return "expired"
	case c.IssuedAt > now+ownerMaxSkew:
		return "issued in the future"
	case c.Expiry-c.IssuedAt > ownerMaxLifetime:
		return "lifetime too long"
	}
	return ""
}

// warn logs a refused assertion at most once a minute.
func (o *ownerLogin) warn(reason string) {
	now := time.Now().Unix()
	last := o.lastWarn.Load()
	if now-last < 60 || !o.lastWarn.CompareAndSwap(last, now) {
		o.log.Debug("owner assertion refused", "reason", reason)
		return
	}
	o.log.Warn("owner assertion refused", "reason", reason)
}
