package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestConnectorStatusClientsAndAudit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertConnectorAccount(ctx, "default", ConnectorAccount{Provider: "github", AccountLabel: "octo", AccessToken: "tok-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConnectorAccountStatus(ctx, "default", "github", "octo", AccountExpired); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConnectorAccountStatus(ctx, "default", "github", "octo", "weird"); err == nil {
		t.Fatal("invalid status accepted")
	}
	if err := s.SetConnectorAccountStatus(ctx, "default", "github", "nobody", AccountOK); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
	list, _ := s.ListConnectorAccounts(ctx, "default", "github")
	if len(list) != 1 || list[0].Status != AccountExpired {
		t.Fatalf("list = %+v", list)
	}
	// Upsert resets the status.
	_ = s.UpsertConnectorAccount(ctx, "default", ConnectorAccount{Provider: "github", AccountLabel: "octo", AccessToken: "tok-2"})
	if a, _ := s.GetConnectorAccount(ctx, "default", "github", "octo"); a.Status != AccountOK || a.AccessToken != "tok-2" {
		t.Fatalf("account = %+v", a)
	}

	if _, err := s.GetConnectorClient(ctx, "default", "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no client: %v", err)
	}
	if err := s.PutConnectorClient(ctx, "default", ConnectorClient{Provider: "github", ClientID: "id", ClientSecret: "client-secret-value"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetConnectorClient(ctx, "default", "github")
	if err != nil || c.ClientID != "id" || c.ClientSecret != "client-secret-value" {
		t.Fatalf("client = %+v %v", c, err)
	}
	if _, err := s.GetConnectorClient(ctx, "other", "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant isolation: %v", err)
	}
	if err := s.DeleteConnectorClient(ctx, "default", "github"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnectorClient(ctx, "default", "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	_ = s.PutConnectorClient(ctx, "default", ConnectorClient{Provider: "slack", ClientID: "id", ClientSecret: "client-secret-value"})
	for _, suffix := range []string{"", "-wal"} {
		raw, _ := os.ReadFile(path + suffix)
		if bytes.Contains(raw, []byte("client-secret-value")) {
			t.Fatal("client secret stored in plaintext")
		}
	}

	for i := 0; i < 5; i++ {
		provider := "github"
		if i%2 == 1 {
			provider = "slack"
		}
		if _, err := s.AppendAudit(ctx, "default", "connector.mcp.call", fmt.Sprintf(`{"provider":%q,"n":%d}`, provider, i)); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.AppendAudit(ctx, "default", "storage.sync", `{"provider":"github"}`)
	_, _ = s.AppendAudit(ctx, "default", "connector.connected", `not json`)
	all, more, err := s.ListConnectorAudit(ctx, "default", ConnectorAuditQuery{})
	if err != nil || more || len(all) != 6 {
		t.Fatalf("all = %d %v %v", len(all), more, err)
	}
	gh, more, _ := s.ListConnectorAudit(ctx, "default", ConnectorAuditQuery{Provider: "github", Limit: 2})
	if len(gh) != 2 || !more || gh[0].Detail != `{"provider":"github","n":4}` {
		t.Fatalf("github page = %+v %v", gh, more)
	}
	rest, more, _ := s.ListConnectorAudit(ctx, "default", ConnectorAuditQuery{Provider: "github", Limit: 2, BeforeID: gh[1].ID})
	if len(rest) != 1 || more {
		t.Fatalf("last page = %+v %v", rest, more)
	}
}
