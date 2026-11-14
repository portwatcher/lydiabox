// Package cloudflaretest is an in-memory fake of the Cloudflare v4 API with
// the endpoints that cloudflare.Setup and cloudflare.Teardown use.
package cloudflaretest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lydiabox/lydiabox/internal/cloudflare"
)

// Account and Token are what the fake accepts.
const (
	Account = "acct1"
	Token   = "tok"
)

// API is the fake. Its maps are keyed by ID; tests may read and change them
// between calls. Fail maps "METHOD /path-prefix" to an HTTP status that the
// next matching request gets.
type API struct {
	t   testing.TB
	srv *httptest.Server

	mu            sync.Mutex
	seq           int
	NoOrg         bool
	Tunnels       map[string]cloudflare.Tunnel
	TunnelTokens  map[string]string // tunnel ID -> token
	Configs       map[string]cloudflare.TunnelConfig
	Zones         []cloudflare.Zone
	Records       map[string]cloudflare.DNSRecord
	IDPs          []cloudflare.IdentityProvider
	Policies      map[string]cloudflare.Policy
	Apps          map[string]cloudflare.Application
	ServiceTokens map[string]cloudflare.ServiceToken
	TURNKeys      map[string]cloudflare.TURNKey
	Fail          map[string]int
	calls         []string
}

// New starts the fake with one zone, example.com.
func New(t testing.TB) *API {
	f := &API{
		t: t, Tunnels: map[string]cloudflare.Tunnel{}, TunnelTokens: map[string]string{}, Configs: map[string]cloudflare.TunnelConfig{},
		Zones: []cloudflare.Zone{{ID: "zone1", Name: "example.com"}}, Records: map[string]cloudflare.DNSRecord{},
		Policies: map[string]cloudflare.Policy{}, Apps: map[string]cloudflare.Application{}, ServiceTokens: map[string]cloudflare.ServiceToken{}, Fail: map[string]int{},
		TURNKeys: map[string]cloudflare.TURNKey{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the base URL of the fake API.
func (f *API) URL() string { return f.srv.URL }

// Client returns a client for the fake account.
func (f *API) Client(t testing.TB) *cloudflare.Client {
	c, err := cloudflare.NewClient(cloudflare.Options{BaseURL: f.srv.URL, Token: Token, AccountID: Account})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Calls counts requests whose "METHOD /path" starts with prefix.
func (f *API) Calls(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// ID returns a new unique ID.
func (f *API) ID(prefix string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id(prefix)
}

func (f *API) id(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s%d", prefix, f.seq)
}

func writeResult(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	ok := status < 300
	body := map[string]any{"success": ok, "errors": []cloudflare.Message{}, "result": result}
	if !ok {
		body["errors"] = []cloudflare.Message{{Code: 10000 + status, Message: "fake failure"}}
		body["result"] = nil
	}
	_ = json.NewEncoder(w).Encode(body)
}

func decode[T any](r *http.Request) T {
	var v T
	_ = json.NewDecoder(r.Body).Decode(&v)
	return v
}

func values[T any](m map[string]T) []T {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(m))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// writeList serves one page of items with result_info, like the real API.
func writeList[T any](w http.ResponseWriter, r *http.Request, items []T) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = 20
	}
	start := min((page-1)*per, len(items))
	end := min(start+per, len(items))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true, "errors": []cloudflare.Message{}, "result": items[start:end],
		"result_info": map[string]int{"page": page, "per_page": per, "count": end - start, "total_count": len(items),
			"total_pages": (len(items) + per - 1) / per},
	})
}

func (f *API) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+Token {
		writeResult(w, http.StatusForbidden, nil)
		return
	}
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	for k, status := range f.Fail {
		if strings.HasPrefix(call, k) {
			delete(f.Fail, k)
			writeResult(w, status, nil)
			return
		}
	}
	acct := "/accounts/" + Account
	p := r.URL.Path
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch {
	case p == acct+"/access/organizations":
		if f.NoOrg {
			writeResult(w, http.StatusNotFound, nil)
			return
		}
		writeResult(w, 200, cloudflare.Organization{Name: "team", AuthDomain: "team.cloudflareaccess.com"})
	case strings.HasPrefix(p, acct+"/cfd_tunnel"):
		f.serveTunnel(w, r, parts[3:])
	case p == "/zones":
		var out []cloudflare.Zone
		for _, z := range f.Zones {
			if z.Name == r.URL.Query().Get("name") && r.URL.Query().Get("account.id") == Account {
				out = append(out, z)
			}
		}
		writeResult(w, 200, out)
	case strings.HasPrefix(p, "/zones/zone1/dns_records"):
		f.serveDNS(w, r, parts[3:])
	case p == acct+"/access/identity_providers":
		if r.Method == http.MethodPost {
			idp := decode[cloudflare.IdentityProvider](r)
			idp.ID = f.id("idp")
			f.IDPs = append(f.IDPs, idp)
			writeResult(w, 200, idp)
			return
		}
		writeResult(w, 200, f.IDPs)
	case strings.HasPrefix(p, acct+"/access/policies"):
		serveCRUD(f, w, r, parts[4:], f.Policies, "pol", func(v *cloudflare.Policy, id string) { v.ID = id })
	case strings.HasPrefix(p, acct+"/access/apps"):
		serveCRUD(f, w, r, parts[4:], f.Apps, "app", func(v *cloudflare.Application, id string) {
			v.ID = id
			if old, ok := f.Apps[id]; ok && old.AUD != "" {
				v.AUD = old.AUD
			} else {
				v.AUD = "aud-" + id
			}
		})
	case strings.HasPrefix(p, acct+"/access/service_tokens"):
		f.serveServiceTokens(w, r, parts[4:])
	case strings.HasPrefix(p, acct+"/calls/turn_keys"):
		f.serveTURNKeys(w, r, parts[4:])
	default:
		f.t.Errorf("fake API: unexpected %s", call)
		writeResult(w, http.StatusNotFound, nil)
	}
}

func (f *API) serveTunnel(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		var out []cloudflare.Tunnel
		for _, t := range f.Tunnels {
			if t.Name == r.URL.Query().Get("name") {
				out = append(out, t)
			}
		}
		writeResult(w, 200, out)
	case len(rest) == 0 && r.Method == http.MethodPost:
		body := decode[map[string]string](r)
		if body["config_src"] != "cloudflare" || body["tunnel_secret"] == "" {
			writeResult(w, http.StatusBadRequest, nil)
			return
		}
		t := cloudflare.Tunnel{ID: f.id("tun"), Name: body["name"], ConfigSrc: "cloudflare"}
		f.Tunnels[t.ID] = t
		f.TunnelTokens[t.ID] = "token-" + t.ID
		writeResult(w, 200, t)
	case len(rest) == 2 && rest[1] == "token":
		writeResult(w, 200, f.TunnelTokens[rest[0]])
	case len(rest) == 2 && rest[1] == "configurations":
		f.Configs[rest[0]] = decode[map[string]cloudflare.TunnelConfig](r)["config"]
		writeResult(w, 200, map[string]any{})
	case len(rest) == 2 && rest[1] == "connections":
		writeResult(w, 200, map[string]any{})
	case len(rest) == 1 && r.Method == http.MethodDelete:
		delete(f.Tunnels, rest[0])
		writeResult(w, 200, map[string]any{})
	default:
		writeResult(w, http.StatusNotFound, nil)
	}
}

func (f *API) serveDNS(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		var out []cloudflare.DNSRecord
		for _, rec := range f.Records {
			if rec.Name == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		writeResult(w, 200, out)
	case len(rest) == 0 && r.Method == http.MethodPost:
		rec := decode[cloudflare.DNSRecord](r)
		rec.ID = f.id("rec")
		f.Records[rec.ID] = rec
		writeResult(w, 200, rec)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		delete(f.Records, rest[0])
		writeResult(w, 200, map[string]string{"id": rest[0]})
	default:
		writeResult(w, http.StatusNotFound, nil)
	}
}

func (f *API) serveServiceTokens(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		out := make([]cloudflare.ServiceToken, 0, len(f.ServiceTokens))
		for _, t := range f.ServiceTokens {
			t.ClientSecret = ""
			out = append(out, t)
		}
		writeResult(w, 200, out)
	case len(rest) == 0 && r.Method == http.MethodPost:
		t := decode[cloudflare.ServiceToken](r)
		t.ID = f.id("svc")
		t.ClientID = t.ID + ".access"
		t.ClientSecret = "secret-" + t.ID
		f.ServiceTokens[t.ID] = t
		writeResult(w, 200, t)
	case len(rest) == 2 && rest[1] == "rotate":
		t, ok := f.ServiceTokens[rest[0]]
		if !ok {
			writeResult(w, http.StatusNotFound, nil)
			return
		}
		f.seq++
		t.ClientSecret = fmt.Sprintf("rotated-%d", f.seq)
		f.ServiceTokens[t.ID] = t
		writeResult(w, 200, t)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		delete(f.ServiceTokens, rest[0])
		writeResult(w, 200, map[string]string{"id": rest[0]})
	default:
		writeResult(w, http.StatusNotFound, nil)
	}
}

// serveTURNKeys follows the Realtime TURN key API: the 64-character
// secret appears only in the create response.
func (f *API) serveTURNKeys(w http.ResponseWriter, r *http.Request, rest []string) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		out := make([]cloudflare.TURNKey, 0, len(f.TURNKeys))
		for _, k := range values(f.TURNKeys) {
			k.Secret = ""
			out = append(out, k)
		}
		writeResult(w, 200, out)
	case len(rest) == 0 && r.Method == http.MethodPost:
		body := decode[map[string]string](r)
		f.seq++
		k := cloudflare.TURNKey{UID: fmt.Sprintf("%032x", f.seq), Name: body["name"], Secret: fmt.Sprintf("%064x", 1000+f.seq),
			Created: "2026-11-14T00:00:00Z", Modified: "2026-11-14T00:00:00Z"}
		f.TURNKeys[k.UID] = k
		writeResult(w, 200, k)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		if _, ok := f.TURNKeys[rest[0]]; !ok {
			writeResult(w, http.StatusNotFound, nil)
			return
		}
		delete(f.TURNKeys, rest[0])
		writeResult(w, 200, map[string]any{})
	default:
		writeResult(w, http.StatusNotFound, nil)
	}
}

func serveCRUD[T any](f *API, w http.ResponseWriter, r *http.Request, rest []string, m map[string]T, prefix string, setID func(*T, string)) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		writeList(w, r, values(m))
	case len(rest) == 0 && r.Method == http.MethodPost:
		v := decode[T](r)
		setID(&v, f.id(prefix))
		var id string
		raw, _ := json.Marshal(v)
		var meta struct{ ID string }
		_ = json.Unmarshal(raw, &meta)
		id = meta.ID
		m[id] = v
		writeResult(w, 200, v)
	case len(rest) == 1 && r.Method == http.MethodPut:
		if _, ok := m[rest[0]]; !ok {
			writeResult(w, http.StatusNotFound, nil)
			return
		}
		v := decode[T](r)
		setID(&v, rest[0])
		m[rest[0]] = v
		writeResult(w, 200, v)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		if _, ok := m[rest[0]]; !ok {
			writeResult(w, http.StatusNotFound, nil)
			return
		}
		delete(m, rest[0])
		writeResult(w, 200, map[string]string{"id": rest[0]})
	default:
		writeResult(w, http.StatusNotFound, nil)
	}
}
