package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"

	"github.com/lydiabox/lydiabox/internal/cloudflare"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/store"
)

// remoteProvider is the provider name of the remote_access table row.
const remoteProvider = "cloudflare"

// remoteStore is the part of *store.Store that remote access uses.
type remoteStore interface {
	GetRemoteAccess(ctx context.Context, tenantID, provider string) (store.RemoteAccess, error)
	PutRemoteAccess(ctx context.Context, tenantID string, ra store.RemoteAccess) error
	DeleteRemoteAccess(ctx context.Context, tenantID, provider string) error
}

// loadCloudflare returns the stored setup; ok is false when there is none.
func loadCloudflare(ctx context.Context, st remoteStore, tenantID string) (cloudflare.State, cloudflare.Secrets, bool, error) {
	var (
		state cloudflare.State
		sec   cloudflare.Secrets
	)
	ra, err := st.GetRemoteAccess(ctx, tenantID, remoteProvider)
	if errors.Is(err, store.ErrNotFound) {
		return state, sec, false, nil
	}
	if err != nil {
		return state, sec, false, err
	}
	if err := json.Unmarshal(ra.State, &state); err != nil {
		return state, sec, false, fmt.Errorf("remote access state: %w", err)
	}
	if len(ra.Secrets) > 0 {
		if err := json.Unmarshal(ra.Secrets, &sec); err != nil {
			return state, sec, false, fmt.Errorf("remote access secrets: %w", err)
		}
	}
	return state, sec, true, nil
}

func saveCloudflare(ctx context.Context, st remoteStore, tenantID string, state cloudflare.State, sec cloudflare.Secrets) error {
	sj, err := json.Marshal(state)
	if err != nil {
		return err
	}
	secj, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	return st.PutRemoteAccess(ctx, tenantID, store.RemoteAccess{Provider: remoteProvider, State: sj, Secrets: secj})
}

// openStateStore opens the state database for a command that runs without
// `lydiabox up`. Like `up`, it first restores the database from the S3
// snapshot when the local file is missing, so that a setup on a new host
// does not hide the restored state.
func openStateStore(ctx context.Context, env Env, cfg *config.Config, log *slog.Logger) (*store.Store, error) {
	if err := cfg.EnsureSecrets(); err != nil {
		return nil, err
	}
	if cfg.AuthTokenGenerated {
		fmt.Fprintf(env.Stdout, "Generated admin token (saved to %s):\n\n    %s\n\n", cfg.AuthTokenPath(), cfg.AuthToken)
	}
	if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) && cfg.ValidateServe() == nil && cfg.S3Configured() {
		objects, err := storage.NewS3Store(cfg.S3, nil)
		if err != nil {
			return nil, err
		}
		if _, err := storage.RestoreState(ctx, objects, cfg.TenantID, cfg.DBPath(), log); err != nil {
			return nil, err
		}
	}
	key, err := cfg.MasterKeyBytes()
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, cfg.DBPath(), key)
}

// defaultOrigin is the URL that cloudflared sends requests to: the listen
// port on loopback, or the explicit listen host when it is a specific IP.
func defaultOrigin(cfg *config.Config) string {
	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return "http://127.0.0.1:8080"
	}
	ip := net.ParseIP(host)
	if !cfg.Remote.ListenAddrSet || host == "" || host == "localhost" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// originMatches reports whether the stored origin reaches the listen address.
func originMatches(origin, listen string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	_, lport, err := net.SplitHostPort(listen)
	return err == nil && u.Port() == lport
}
