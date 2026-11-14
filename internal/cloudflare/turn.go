package cloudflare

import (
	"context"
	"errors"
	"net/http"
)

// TURNKey is a Cloudflare Realtime TURN key. The desktop stream mints
// short-lived TURN credentials with it (internal/desktop/ice).
type TURNKey struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	// Secret is the bearer token of the key. Cloudflare returns it only
	// when it creates the key.
	Secret   string `json:"secret,omitempty"`
	Created  string `json:"created,omitempty"`
	Modified string `json:"modified,omitempty"`
}

// TURNKeys lists the TURN keys of the account (without secrets).
func (c *Client) TURNKeys(ctx context.Context) ([]TURNKey, error) {
	var keys []TURNKey
	_, err := c.do(ctx, http.MethodGet, c.accountPath("/calls/turn_keys"), nil, nil, &keys)
	return keys, err
}

// CreateTURNKey creates a TURN key and returns it with its secret.
func (c *Client) CreateTURNKey(ctx context.Context, name string) (TURNKey, error) {
	var k TURNKey
	_, err := c.do(ctx, http.MethodPost, c.accountPath("/calls/turn_keys"), nil, map[string]string{"name": name}, &k)
	return k, err
}

// DeleteTURNKey deletes a TURN key.
func (c *Client) DeleteTURNKey(ctx context.Context, uid string) error {
	_, err := c.do(ctx, http.MethodDelete, c.accountPath("/calls/turn_keys/%s", esc(uid)), nil, nil, nil)
	return err
}

func findTURNKey(ctx context.Context, c *Client, name string) (*TURNKey, error) {
	keys, err := c.TURNKeys(ctx)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		if keys[i].Name == name {
			return &keys[i], nil
		}
	}
	return nil, nil
}

// setupTURN creates (or keeps) the TURN key of the desktop stream. The
// secret is shown only once, so a key without a stored secret is replaced.
func setupTURN(ctx context.Context, c *Client, p SetupParams, n names, prev State, prevSecrets Secrets, st *State, sec *Secrets) error {
	found, err := findTURNKey(ctx, c, n.turn)
	if err != nil {
		return err
	}
	if found != nil && found.UID == prev.TURNKeyID && prevSecrets.TURNKeySecret != "" {
		st.TURNKeyID, sec.TURNKeySecret = found.UID, prevSecrets.TURNKeySecret
		p.Progress("reused TURN key %q", n.turn)
		return nil
	}
	if found != nil {
		if err := c.DeleteTURNKey(ctx, found.UID); err != nil && !IsNotFound(err) {
			return err
		}
	}
	k, err := c.CreateTURNKey(ctx, n.turn)
	if err != nil {
		return err
	}
	if k.UID == "" || k.Secret == "" {
		return errors.New("cloudflare: the TURN key response has no uid or secret")
	}
	st.TURNKeyID, sec.TURNKeySecret = k.UID, k.Secret
	if found != nil {
		p.Progress("replaced TURN key %q (the stored secret was missing)", n.turn)
	} else {
		p.Progress("created TURN key %q", n.turn)
	}
	return nil
}

// removeTURN deletes the TURN key that Setup made for the hostname.
func removeTURN(ctx context.Context, c *Client, n names, progress func(string, ...any)) error {
	found, err := findTURNKey(ctx, c, n.turn)
	if err != nil || found == nil {
		return err
	}
	if err := c.DeleteTURNKey(ctx, found.UID); err != nil && !IsNotFound(err) {
		return err
	}
	progress("deleted TURN key %q", n.turn)
	return nil
}
