package ice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// CloudflareAPIBase is the Cloudflare Realtime API base URL.
const CloudflareAPIBase = "https://rtc.live.cloudflare.com/v1"

// Cloudflare mints Cloudflare Realtime TURN credentials from a TURN key
// (https://developers.cloudflare.com/realtime/turn/generate-credentials/).
type Cloudflare struct {
	// KeyID and APIToken are the TURN key ID and its API token.
	KeyID, APIToken string
	// BaseURL replaces CloudflareAPIBase (tests).
	BaseURL string
	// HTTPClient defaults to a client with a 15 s timeout.
	HTTPClient *http.Client

	logger *slog.Logger
}

func (c *Cloudflare) init(logger *slog.Logger) error {
	if c.KeyID == "" || c.APIToken == "" {
		return errors.New("ice: Cloudflare TURN needs a key ID and an API token")
	}
	if c.BaseURL == "" {
		c.BaseURL = CloudflareAPIBase
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	c.logger = logger
	return nil
}

type cfServer struct {
	URLs       json.RawMessage `json:"urls"`
	Username   string          `json:"username"`
	Credential string          `json:"credential"`
}

// Mint generates ICE servers with credentials valid for ttl. username is
// the TURN username, which Revoke takes.
func (c *Cloudflare) Mint(ctx context.Context, ttl time.Duration) (servers []rpc.ICEServer, username string, err error) {
	body, _ := json.Marshal(map[string]int64{"ttl": int64(ttl / time.Second)})
	u := c.BaseURL + "/turn/keys/" + url.PathEscape(c.KeyID) + "/credentials/generate-ice-servers"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("ice: Cloudflare TURN: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("ice: Cloudflare TURN: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("ice: Cloudflare TURN credentials: HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var out struct {
		ICEServers []cfServer `json:"iceServers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", fmt.Errorf("ice: Cloudflare TURN credentials: invalid response: %w", err)
	}
	for _, s := range out.ICEServers {
		urls, err := decodeURLs(s.URLs)
		if err != nil {
			return nil, "", fmt.Errorf("ice: Cloudflare TURN credentials: %w", err)
		}
		if len(urls) == 0 {
			continue
		}
		servers = append(servers, rpc.ICEServer{URLs: urls, Username: s.Username, Credential: s.Credential})
		if s.Username != "" && username == "" {
			username = s.Username
		}
	}
	if username == "" {
		return nil, "", errors.New("ice: Cloudflare TURN credentials: the response has no TURN server")
	}
	return servers, username, nil
}

// Revoke ends the credentials of username.
func (c *Cloudflare) Revoke(ctx context.Context, username string) error {
	u := c.BaseURL + "/turn/keys/" + url.PathEscape(c.KeyID) + "/credentials/" + url.PathEscape(username) + "/revoke"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIToken)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	return nil
}

func (c *Cloudflare) revokeAsync(username string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := c.Revoke(ctx, username); err != nil && c.logger != nil {
			c.logger.Warn("revoke Cloudflare TURN credentials", "err", err)
		}
	}()
}

// decodeURLs accepts the string and the array form of RTCIceServer.urls and
// drops the port 53 URLs, which browsers block (they time out).
func decodeURLs(raw json.RawMessage) ([]string, error) {
	var list []string
	if len(raw) > 0 && raw[0] == '"' {
		var one string
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		list = []string{one}
	} else if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("invalid urls: %w", err)
		}
	}
	out := list[:0]
	for _, u := range list {
		if !isPort53(u) {
			out = append(out, u)
		}
	}
	return out, nil
}

// isPort53 reports whether a stun:/turn: URL uses port 53.
func isPort53(u string) bool {
	_, rest, ok := strings.Cut(u, ":")
	if !ok {
		return false
	}
	hostport, _, _ := strings.Cut(rest, "?")
	return strings.HasSuffix(hostport, ":53")
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
