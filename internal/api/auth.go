package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/httpx"
)

// SessionCookie is the browser session cookie name.
const SessionCookie = "lydiabox_session"

// BasicUser is the fixed Basic Auth user name (WebDAV clients).
const BasicUser = "lydiabox"

// Authenticator validates the admin token presented as a Bearer header, a
// session cookie, or Basic Auth (user "lydiabox"), and, when EnableAccess
// was called, a Cloudflare Access token (access_auth.go), and, when
// EnableOwnerAssertion was called, the owner assertion of the control
// plane of a hosted cell (owner_auth.go).
type Authenticator struct {
	tokenSum [32]byte
	session  string
	limiter  *failureLimiter
	now      func() time.Time
	access   *accessLogin
	owner    *ownerLogin
}

// NewAuthenticator derives the session cookie value from token with
// HMAC-SHA256, so the raw token is never stored in the browser and rotating
// the token invalidates every session.
func NewAuthenticator(token string) *Authenticator {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("lydiabox-session-v1"))
	return &Authenticator{
		tokenSum: sha256.Sum256([]byte(token)),
		session:  hex.EncodeToString(mac.Sum(nil)),
		limiter:  newFailureLimiter(10, time.Minute),
		now:      time.Now,
	}
}

func (a *Authenticator) tokenOK(candidate string) bool {
	sum := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(sum[:], a.tokenSum[:]) == 1
}

type credentialKind int

const (
	credNone credentialKind = iota
	credBearer
	credBasic
	credCookie
	// credAccess is a Cloudflare Access token of an allowed email.
	credAccess
	// credOwner is an owner assertion of the control plane of a hosted
	// cell (owner_auth.go).
	credOwner
)

// authenticate reports which credential authenticated the request and, for
// an Access token, the email. An Authorization header always decides on its
// own, so a client that sends a wrong token never falls back to a cookie.
func (a *Authenticator) authenticate(r *http.Request) (credentialKind, string) {
	if h := r.Header.Get("Authorization"); h != "" {
		if tok, ok := strings.CutPrefix(h, "Bearer "); ok && a.tokenOK(strings.TrimSpace(tok)) {
			return credBearer, ""
		}
		if user, pass, ok := r.BasicAuth(); ok && user == BasicUser && a.tokenOK(pass) {
			return credBasic, ""
		}
		return credNone, ""
	}
	if email := a.ownerEmail(r); email != "" {
		return credOwner, email
	}
	if email := a.accessEmail(r); email != "" {
		return credAccess, email
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.session)) == 1 {
			return credCookie, ""
		}
	}
	return credNone, ""
}

// Authenticated reports whether r carries a valid credential.
func (a *Authenticator) Authenticated(r *http.Request) bool {
	kind, _ := a.authenticate(r)
	return kind != credNone
}

// Require wraps h with authentication. Cookie-authenticated requests that
// change state or open a WebSocket must come from the same origin, which
// blocks cross-site request forgery and cross-site WebSocket hijacking.
func (a *Authenticator) Require(h http.Handler, basicChallenge bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := a.clientIP(r)
		kind, _ := a.authenticate(r)
		// A valid session cookie, Access token, or owner assertion cannot
		// be guessed, so failed attempts of other clients must not lock
		// it out.
		guessable := kind != credCookie && kind != credAccess && kind != credOwner
		if guessable && a.limiter.blocked(ip, a.now()) {
			w.Header().Set("Retry-After", "60")
			httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many failed authentication attempts")
			return
		}
		if kind == credNone {
			if r.Header.Get("Authorization") != "" {
				a.limiter.fail(ip, a.now())
			}
			if basicChallenge {
				w.Header().Set("WWW-Authenticate", `Basic realm="LydiaBox", charset="UTF-8"`)
			} else {
				w.Header().Set("WWW-Authenticate", `Bearer realm="LydiaBox"`)
			}
			httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "authentication required")
			return
		}
		// The browser sends our cookie, CF_Authorization, and Basic
		// credentials that it cached after a WebDAV challenge on
		// cross-site requests, so all of them need the same-origin check.
		// Only a Bearer token is never sent by the browser on its own.
		if kind != credBearer && needsOriginCheck(r) && !sameOrigin(r) {
			httpx.WriteError(w, http.StatusForbidden, httpx.CodeForbidden, "cross-origin request rejected")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func needsOriginCheck(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	}
	return true
}

// sameOrigin accepts requests whose Origin (or Referer) host equals the
// request Host. Requests without either header are not browser
// cross-site requests and are accepted.
func sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return true
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failureLimiter blocks a client IP after max failed attempts inside window.
type failureLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newFailureLimiter(max int, window time.Duration) *failureLimiter {
	return &failureLimiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *failureLimiter) prune(ip string, now time.Time) []time.Time {
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.hits, ip)
		return nil
	}
	l.hits[ip] = kept
	return kept
}

func (l *failureLimiter) blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(ip, now)) >= l.max
}

func (l *failureLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[ip] = append(l.prune(ip, now), now)
	if len(l.hits) > 10000 {
		// Bound memory under a distributed brute force; drop the oldest state.
		for k := range l.hits {
			delete(l.hits, k)
			if len(l.hits) <= 5000 {
				break
			}
		}
	}
}

// LoginRequest is the body of POST /api/v1/auth/login.
type LoginRequest struct {
	Token string `json:"token"`
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	if a.limiter.blocked(ip, a.now()) {
		w.Header().Set("Retry-After", "60")
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many failed login attempts")
		return
	}
	var req LoginRequest
	if err := httpx.DecodeJSON(w, r, 4096, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	if !a.tokenOK(req.Token) {
		a.limiter.fail(ip, a.now())
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "invalid token")
		return
	}
	http.SetCookie(w, a.cookie(r, a.session, 30*24*time.Hour))
	httpx.WriteJSON(w, http.StatusOK, SessionState{Authenticated: true, Method: MethodToken, Access: a.access != nil})
}

func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, a.cookie(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (a *Authenticator) cookie(r *http.Request, value string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
	}
	if maxAge < 0 {
		c.MaxAge = -1
	} else {
		c.MaxAge = int(maxAge.Seconds())
	}
	return c
}
