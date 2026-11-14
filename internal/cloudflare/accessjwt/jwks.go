package accessjwt

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// jwk is one key of the team JWKS.
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// keySet caches the team signing keys. A lookup of an unknown key ID
// refetches the set, at most once per minRefresh, so a flood of tokens with
// random key IDs cannot turn into a flood of fetches.
type keySet struct {
	url        string
	client     *http.Client
	now        func() time.Time
	minRefresh time.Duration
	maxAge     time.Duration

	mu        sync.Mutex // serializes fetches
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	lastTry   time.Time
}

// errUnknownKey is returned when the key ID is not in the refreshed set.
var errUnknownKey = errors.New("accessjwt: unknown signing key")

func (s *keySet) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	k, ok := s.keys[kid]
	stale := s.fetchedAt.IsZero() || now.Sub(s.fetchedAt) >= s.maxAge
	if ok && !stale {
		return k, nil
	}
	if !s.lastTry.IsZero() && now.Sub(s.lastTry) < s.minRefresh {
		if ok {
			return k, nil
		}
		return nil, errUnknownKey
	}
	s.lastTry = now
	keys, err := s.fetch(ctx)
	if err != nil {
		if ok {
			// Keep serving a known key while the certs endpoint is down.
			return k, nil
		}
		return nil, err
	}
	s.keys, s.fetchedAt = keys, now
	if k, ok = keys[kid]; ok {
		return k, nil
	}
	return nil, errUnknownKey
}

func (s *keySet) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	// A client that disconnects must not cancel the shared fetch: the
	// refetch limit would then refuse every new key for MinRefresh.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accessjwt: fetch %s: %w", s.url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("accessjwt: fetch %s: HTTP %d", s.url, resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("accessjwt: decode %s: %w", s.url, err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		pub, err := rsaKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("accessjwt: %s has no RS256 signing keys", s.url)
	}
	return keys, nil
}

func rsaKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	e := new(big.Int).SetBytes(eb)
	if len(nb) < 256 || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31 {
		return nil, errors.New("accessjwt: weak or invalid RSA key")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e.Int64())}, nil
}
