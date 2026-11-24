// Package ice builds the ICE servers of each desktop viewer: STUN URLs, TURN
// URLs with static credentials, TURN URLs with short-lived credentials from a
// shared secret (the TURN REST scheme of coturn's static-auth-secret), and
// Cloudflare Realtime TURN credentials minted through the Cloudflare API. The
// browser and the guest peer of a viewer get the same servers.
package ice

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // The TURN REST scheme specifies HMAC-SHA1.
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// DefaultSTUNURL is the STUN server used when no ICE setting is configured.
const DefaultSTUNURL = "stun:stun.l.google.com:19302"

// DefaultTTL is the lifetime of minted TURN credentials.
const DefaultTTL = time.Hour

// Grant is the ICE configuration of one viewer.
type Grant struct {
	Servers []rpc.ICEServer
	// Expires is when the earliest minted credential ends; zero when every
	// credential is static.
	Expires time.Time

	release func()
	once    *sync.Once
}

// Release revokes the minted credentials of the grant where the TURN
// service supports it. It is safe to call more than once.
func (g Grant) Release() {
	if g.release != nil && g.once != nil {
		g.once.Do(g.release)
	}
}

// Provider returns the ICE configuration of a new viewer.
type Provider interface {
	Grant(ctx context.Context, tenantID string) (Grant, error)
}

// Config configures New.
type Config struct {
	// STUNURLs are stun: or stuns: URLs. nil selects DefaultSTUNURL unless
	// Cloudflare is set (its response has STUN URLs); an empty, non-nil
	// slice disables STUN.
	STUNURLs []string
	// TURNURLs are turn: or turns: URLs (for example
	// turn:turn.example.com:3478?transport=tcp).
	TURNURLs []string
	// TURNUsername and TURNPassword are static TURN credentials.
	TURNUsername, TURNPassword string
	// TURNSecret, when set, mints time-limited TURN REST credentials
	// (username "<expiry>:<tenant>", password base64(HMAC-SHA1(secret,
	// username))) instead of the static credentials.
	TURNSecret string
	// TTL is the lifetime of minted credentials (default DefaultTTL).
	TTL time.Duration
	// Cloudflare, when set, mints Cloudflare Realtime TURN credentials.
	Cloudflare *Cloudflare
	// Now is the clock (tests).
	Now    func() time.Time
	Logger *slog.Logger
}

// Static returns a provider that always gives servers.
func Static(servers []rpc.ICEServer) Provider { return staticProvider(servers) }

type staticProvider []rpc.ICEServer

func (s staticProvider) Grant(context.Context, string) (Grant, error) {
	return Grant{Servers: append([]rpc.ICEServer(nil), s...)}, nil
}

// New returns the provider of cfg.
func New(cfg Config) (Provider, error) {
	if len(cfg.TURNURLs) > 0 && cfg.TURNSecret == "" && (cfg.TURNUsername == "" || cfg.TURNPassword == "") {
		return nil, errors.New("ice: TURN URLs need a username and password, or a shared secret")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.STUNURLs == nil && cfg.Cloudflare == nil {
		cfg.STUNURLs = []string{DefaultSTUNURL}
	}
	if cfg.Cloudflare != nil {
		if err := cfg.Cloudflare.init(cfg.Logger); err != nil {
			return nil, err
		}
	}
	return &provider{cfg: cfg}, nil
}

type provider struct{ cfg Config }

func (p *provider) Grant(ctx context.Context, tenantID string) (Grant, error) {
	var g Grant
	now := p.cfg.Now()
	if len(p.cfg.STUNURLs) > 0 {
		g.Servers = append(g.Servers, rpc.ICEServer{URLs: append([]string(nil), p.cfg.STUNURLs...)})
	}
	if len(p.cfg.TURNURLs) > 0 {
		turn := rpc.ICEServer{URLs: append([]string(nil), p.cfg.TURNURLs...), Username: p.cfg.TURNUsername, Credential: p.cfg.TURNPassword}
		if p.cfg.TURNSecret != "" {
			exp := now.Add(p.cfg.TTL)
			turn.Username, turn.Credential = RESTCredentials(p.cfg.TURNSecret, tenantID, exp)
			g.Expires = exp
		}
		g.Servers = append(g.Servers, turn)
	}
	if cf := p.cfg.Cloudflare; cf != nil {
		servers, username, err := cf.Mint(ctx, p.cfg.TTL)
		if err != nil {
			return Grant{}, err
		}
		g.Servers = append(g.Servers, servers...)
		if exp := now.Add(p.cfg.TTL); g.Expires.IsZero() || exp.Before(g.Expires) {
			g.Expires = exp
		}
		if username != "" {
			g.release = func() { cf.revokeAsync(username) }
			g.once = &sync.Once{}
		}
	}
	return g, nil
}

// RESTCredentials returns TURN REST API credentials (the coturn
// use-auth-secret scheme) for user that expire at exp.
func RESTCredentials(secret, user string, exp time.Time) (username, password string) {
	username = strconv.FormatInt(exp.Unix(), 10) + ":" + user
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// NewGrant returns a grant; release, when not nil, runs once on Release.
func NewGrant(servers []rpc.ICEServer, expires time.Time, release func()) Grant {
	return Grant{Servers: servers, Expires: expires, release: release, once: &sync.Once{}}
}
