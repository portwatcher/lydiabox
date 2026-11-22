package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lydiabox/lydiabox/internal/cloudflare/accessjwt"
	"github.com/lydiabox/lydiabox/internal/httpx"
)

// AccessVerifier checks a Cloudflare Access token (*accessjwt.Verifier
// implements it). err is nil only for an allowed email; for a valid token
// of an email that is not allowed, it returns that email with an error.
type AccessVerifier interface {
	VerifyAccess(ctx context.Context, token string) (email string, err error)
}

type accessLogin struct {
	verifier AccessVerifier
	log      *slog.Logger
	lastWarn atomic.Int64
}

// EnableAccess turns on Cloudflare Access login: a request with a valid
// Cf-Access-Jwt-Assertion header or CF_Authorization cookie of an allowed
// email is the admin, without the token prompt. The admin token keeps
// working for Bearer, Basic, and session cookie clients. Call it before
// the server starts.
//
// It also makes the failed-login limiter use the Cf-Connecting-IP header of
// loopback requests: every request through cloudflared arrives from
// loopback, so without the header one client could lock out all others.
// Requests on a UNIX socket (a cell) always use that header when present.
func (a *Authenticator) EnableAccess(v AccessVerifier, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	a.access = &accessLogin{verifier: v, log: log.With("component", "access")}
}

// accessEmail returns the allowed email of the Access token of r, or "".
func (a *Authenticator) accessEmail(r *http.Request) string {
	email, allowed := a.accessIdentity(r)
	if !allowed {
		return ""
	}
	return email
}

// accessIdentity returns the email of a valid Access token of r and whether
// it is on the allow list.
func (a *Authenticator) accessIdentity(r *http.Request) (string, bool) {
	if a.access == nil {
		return "", false
	}
	tok := strings.TrimSpace(r.Header.Get(accessjwt.Header))
	if tok == "" {
		if c, err := r.Cookie(accessjwt.Cookie); err == nil {
			tok = c.Value
		}
	}
	if tok == "" {
		return "", false
	}
	email, err := a.access.verifier.VerifyAccess(r.Context(), tok)
	if err != nil {
		a.access.warn(err)
		return email, false
	}
	return email, email != ""
}

// warn logs a rejected Access token at most once a minute; a wrong
// audience tag or team domain otherwise floods the log.
func (l *accessLogin) warn(err error) {
	now := time.Now().Unix()
	last := l.lastWarn.Load()
	if now-last < 60 || !l.lastWarn.CompareAndSwap(last, now) {
		l.log.Debug("Cloudflare Access token rejected", "err", err)
		return
	}
	l.log.Warn("Cloudflare Access token rejected", "err", err)
}

// clientIP is the address used by the failed-login limiter.
func (a *Authenticator) clientIP(r *http.Request) string {
	ip := clientIP(r)
	parsed := net.ParseIP(ip)
	if parsed == nil {
		// A UNIX socket peer (a cell): every connection comes from the
		// cell host and has the same address, so use the client address
		// that the edge sets. Requests without it (the control plane)
		// share one key.
		if cf := net.ParseIP(strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip"))); cf != nil {
			return cf.String()
		}
		return ip
	}
	if (a.access == nil && a.owner == nil) || !parsed.IsLoopback() {
		return ip
	}
	if cf := net.ParseIP(strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip"))); cf != nil {
		return cf.String()
	}
	return ip
}

// Values of SessionState.Method.
const (
	MethodToken  = "token"
	MethodAccess = "access"
	// MethodOwner is the owner assertion of the control plane of a hosted
	// cell.
	MethodOwner = "owner"
)

// SessionState is the body of GET /api/v1/auth/session.
type SessionState struct {
	Authenticated bool `json:"authenticated"`
	// Method is "token" (admin token: cookie, Bearer, or Basic) or
	// "access" (Cloudflare Access); empty when not authenticated.
	Method string `json:"method,omitempty"`
	// Email is the Cloudflare Access email. When Authenticated is false,
	// it is a valid Access login whose email is not on the allow list.
	Email string `json:"email,omitempty"`
	// Access reports that Cloudflare Access login is on.
	Access bool `json:"access"`
	// LogoutURL is where the workstation signs out with MethodOwner (the
	// control plane ends the session).
	LogoutURL string `json:"logoutUrl,omitempty"`
}

func (a *Authenticator) sessionStatus(w http.ResponseWriter, r *http.Request) {
	kind, email := a.authenticate(r)
	st := SessionState{Authenticated: kind != credNone, Access: a.access != nil}
	switch kind {
	case credAccess:
		st.Method, st.Email = MethodAccess, email
	case credOwner:
		st.Method, st.Email, st.LogoutURL = MethodOwner, email, a.owner.logoutURL
	case credNone:
		if email, _ := a.accessIdentity(r); email != "" {
			st.Email = email
		}
	default:
		st.Method = MethodToken
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}
