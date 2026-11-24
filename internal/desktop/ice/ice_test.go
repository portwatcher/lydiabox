package ice

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/turn/v4"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestDefaultIsGoogleSTUN(t *testing.T) {
	p, err := New(Config{Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	g, err := p.Grant(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(g.Servers)
	if string(b) != `[{"urls":["stun:stun.l.google.com:19302"]}]` || !g.Expires.IsZero() {
		t.Fatalf("grant %s expires %v", b, g.Expires)
	}
	g.Release()
}

func TestStaticTURNAndNoSTUN(t *testing.T) {
	p, err := New(Config{STUNURLs: []string{}, TURNURLs: []string{"turns:turn.example.com:443?transport=tcp"},
		TURNUsername: "u", TURNPassword: "p", Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := p.Grant(context.Background(), "default")
	if len(g.Servers) != 1 || g.Servers[0].Username != "u" || g.Servers[0].Credential != "p" || !g.Expires.IsZero() {
		t.Fatalf("grant %+v", g)
	}
	if _, err := New(Config{TURNURLs: []string{"turn:x:3478"}}); err == nil {
		t.Fatal("TURN URLs without credentials accepted")
	}
}

func TestSharedSecretCredentialsAuthenticate(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	p, err := New(Config{TURNURLs: []string{"turn:turn.example.com:3478"}, TURNSecret: "s3cret", TTL: 10 * time.Minute,
		Now: func() time.Time { return now }, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := p.Grant(context.Background(), "tenant-a")
	turnSrv := g.Servers[1]
	if turnSrv.Username != "1900000600:tenant-a" || !g.Expires.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("grant %+v", g)
	}
	// The pion TURN server accepts the credential with the same secret.
	auth := turn.LongTermTURNRESTAuthHandler("s3cret", nil)
	key, ok := auth(turnSrv.Username, "lydiabox", &net.UDPAddr{})
	if !ok || string(key) != string(turn.GenerateAuthKey(turnSrv.Username, "lydiabox", turnSrv.Credential)) {
		t.Fatal("pion rejects the minted credential")
	}
}

// fakeCloudflare is the Cloudflare Realtime TURN API.
type fakeCloudflare struct {
	mu       sync.Mutex
	status   int
	minted   int
	ttls     []int64
	revoked  []string
	authBad  bool
	response string
}

func (f *fakeCloudflare) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/turn/keys/key-1/credentials/generate-ice-servers", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok-1" || r.Header.Get("Content-Type") != "application/json" {
			f.authBad = true
		}
		var body struct {
			TTL int64 `json:"ttl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body: %v", err)
		}
		f.ttls = append(f.ttls, body.TTL)
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"message":"Unauthorized"}]}`)
			return
		}
		f.minted++
		user := "cfuser" + strings.Repeat("x", f.minted)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		resp := f.response
		if resp == "" {
			resp = `{"iceServers":[{"urls":["stun:stun.cloudflare.com:3478","stun:stun.cloudflare.com:53"]},
{"urls":["turn:turn.cloudflare.com:3478?transport=udp","turn:turn.cloudflare.com:53?transport=udp",
"turn:turn.cloudflare.com:3478?transport=tcp","turns:turn.cloudflare.com:443?transport=tcp"],
"username":"USER","credential":"cred"}]}`
		}
		_, _ = io.WriteString(w, strings.ReplaceAll(resp, "USER", user))
	})
	mux.HandleFunc("POST /v1/turn/keys/key-1/credentials/{user}/revoke", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.revoked = append(f.revoked, r.PathValue("user"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func cloudflareProvider(t *testing.T, f *fakeCloudflare, ttl time.Duration, now func() time.Time) Provider {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	p, err := New(Config{TTL: ttl, Now: now, Logger: quiet(),
		Cloudflare: &Cloudflare{KeyID: "key-1", APIToken: "tok-1", BaseURL: srv.URL + "/v1/"}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCloudflareMint(t *testing.T) {
	f := &fakeCloudflare{}
	now := time.Unix(1_900_000_000, 0)
	p := cloudflareProvider(t, f, 30*time.Minute, func() time.Time { return now })
	g, err := p.Grant(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	want := []rpc.ICEServer{
		{URLs: []string{"stun:stun.cloudflare.com:3478"}},
		{URLs: []string{"turn:turn.cloudflare.com:3478?transport=udp", "turn:turn.cloudflare.com:3478?transport=tcp",
			"turns:turn.cloudflare.com:443?transport=tcp"}, Username: "cfuserx", Credential: "cred"},
	}
	got, _ := json.Marshal(g.Servers)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("servers\n got %s\nwant %s", got, exp)
	}
	if !g.Expires.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("expires %v", g.Expires)
	}
	f.mu.Lock()
	if f.authBad || len(f.ttls) != 1 || f.ttls[0] != 1800 {
		t.Fatalf("request: authBad %v ttls %v", f.authBad, f.ttls)
	}
	f.mu.Unlock()

	// Release revokes the credential once.
	g.Release()
	g.Release()
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.revoked) == 1 && f.revoked[0] == "cfuserx" })
}

func TestCloudflareRefreshMintsNewCredentials(t *testing.T) {
	f := &fakeCloudflare{}
	now := time.Unix(1_900_000_000, 0)
	p := cloudflareProvider(t, f, time.Hour, func() time.Time { return now })
	g1, err := p.Grant(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(55 * time.Minute)
	g2, err := p.Grant(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if g1.Servers[1].Username == g2.Servers[1].Username || !g2.Expires.After(g1.Expires) {
		t.Fatalf("refresh: %v %v / %v %v", g1.Servers[1].Username, g1.Expires, g2.Servers[1].Username, g2.Expires)
	}
}

func TestCloudflareError(t *testing.T) {
	f := &fakeCloudflare{status: http.StatusUnauthorized}
	p := cloudflareProvider(t, f, time.Hour, nil)
	_, err := p.Grant(context.Background(), "default")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "tok-1") {
		t.Fatalf("err %v", err)
	}
	f2 := &fakeCloudflare{response: `{"iceServers":[{"urls":"stun:stun.cloudflare.com:3478"}]}`}
	if _, err := cloudflareProvider(t, f2, time.Hour, nil).Grant(context.Background(), "default"); err == nil ||
		!strings.Contains(err.Error(), "no TURN server") {
		t.Fatalf("err %v", err)
	}
	if _, err := New(Config{Cloudflare: &Cloudflare{KeyID: "k"}}); err == nil {
		t.Fatal("Cloudflare without a token accepted")
	}
}

func TestCloudflareWithSTUNAndStaticTURN(t *testing.T) {
	f := &fakeCloudflare{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	p, err := New(Config{STUNURLs: []string{"stun:stun.example.com:3478"}, TURNURLs: []string{"turn:own.example.com:3478"},
		TURNUsername: "u", TURNPassword: "p", Logger: quiet(),
		Cloudflare: &Cloudflare{KeyID: "key-1", APIToken: "tok-1", BaseURL: srv.URL + "/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := p.Grant(context.Background(), "default")
	if err != nil || len(g.Servers) != 4 || g.Servers[0].URLs[0] != "stun:stun.example.com:3478" || g.Servers[1].Username != "u" {
		t.Fatalf("grant %+v err %v", g, err)
	}
}

func TestIsPort53(t *testing.T) {
	for u, want := range map[string]bool{
		"turn:turn.cloudflare.com:53?transport=udp":   true,
		"stun:stun.cloudflare.com:53":                 true,
		"turn:turn.cloudflare.com:5349":               false,
		"turns:turn.cloudflare.com:443?transport=tcp": false,
	} {
		if isPort53(u) != want {
			t.Errorf("isPort53(%q) != %v", u, want)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
