package instabox_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lydiabox/lydiabox/internal/instabox"
)

func TestBaseURLRefusesPlainHTTP(t *testing.T) {
	if _, err := instabox.NewClient(instabox.Options{APIKey: "ibk_x", BaseURL: "http://api.example.test"}); err == nil {
		t.Fatal("plain http base URL accepted")
	}
	for _, u := range []string{"http://127.0.0.1:1", "http://localhost:8080", "http://[::1]:9"} {
		if _, err := instabox.NewClient(instabox.Options{APIKey: "ibk_x", BaseURL: u}); err != nil {
			t.Fatalf("loopback %s refused: %v", u, err)
		}
	}
}

// A redirect to another host does not carry the API key.
func TestRedirectDropsAPIKeyForAnotherHost(t *testing.T) {
	got := make(chan string, 1)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("X-API-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer other.Close()
	// "localhost" and "127.0.0.1" are different hosts for the redirect.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer api.Close()
	base := fmt.Sprintf("http://localhost:%d", api.Listener.Addr().(*net.TCPAddr).Port)
	c, err := instabox.NewClient(instabox.Options{APIKey: "ibk_secret", BaseURL: base, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.ListMachines(context.Background(), instabox.ListMachinesOptions{})
	select {
	case k := <-got:
		if k != "" {
			t.Fatalf("API key sent to another host: %q", k)
		}
	default:
		t.Fatal("the redirect target got no request")
	}
}
