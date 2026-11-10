package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/storage/files"
	"github.com/lydiabox/lydiabox/internal/tenant"
	"github.com/lydiabox/lydiabox/internal/webui"
)

const token = "test-admin-token-0123456789abcdef"

type dirs struct{ home, workspace string }

func (d dirs) HomeDir(string) (string, error)      { return d.home, nil }
func (d dirs) WorkspaceDir(string) (string, error) { return d.workspace, nil }

type fakeStorage struct{}

func (fakeStorage) Status(tid string) storage.Status {
	return storage.Status{TenantID: tid, Mounted: true}
}

type env struct {
	srv    *httptest.Server
	dirs   dirs
	secret string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	d := dirs{home: filepath.Join(base, "jfs/home/agent"), workspace: filepath.Join(base, "jfs/workspace")}
	secret := filepath.Join(base, "secret")
	for _, p := range []string{d.home, d.workspace, secret} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	auth := api.NewAuthenticator(token)
	dav := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Dav", tenant.FromContext(r.Context())+" "+r.Method+" "+r.URL.Path)
		w.WriteHeader(207)
	})
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		api.CoreRoutes{Auth: auth, VM: api.StaticVMStatus{State: api.VMStopped, Driver: "docker"}, Storage: fakeStorage{}, S3Gateway: fakeGateway{}},
		api.FilesRoutes{Handler: files.NewHandler(files.NewService(d, files.Options{}))},
		api.WebDAVRoutes{Handler: dav},
		api.WebUIRoutes{Handler: webui.Handler(webui.FS())},
	)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{srv: srv, dirs: d, secret: secret}
}

// do sends a request with the bearer token unless auth is "-" (none) or a
// full Authorization header value.
func (e *env) do(t *testing.T, method, path, auth string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	switch auth {
	case "":
		req.Header.Set("Authorization", "Bearer "+token)
	case "-":
	default:
		req.Header.Set("Authorization", auth)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return v
}

func errCode(t *testing.T, data []byte) string {
	t.Helper()
	return decode[struct {
		Error struct{ Code string } `json:"error"`
	}](t, data).Error.Code
}

func TestHealthAndStatus(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(t, "GET", "/api/v1/health", "-", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok"`) {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(t, "GET", "/api/v1/status", "-", nil, nil)
	if resp.StatusCode != 401 || errCode(t, body) != "unauthorized" || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("status without auth: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(t, "GET", "/api/v1/status", "", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	st := decode[api.StatusResponse](t, body)
	if st.VM.State != "STOPPED" || st.VM.Driver != "docker" || !st.Storage.Mounted || st.TenantID != "default" || st.Version.Version == "" {
		t.Fatalf("status = %+v", st)
	}
	if gw := st.Storage.S3Gateway; !gw.Enabled || !gw.Running || gw.Addr != "127.0.0.1:8090" || gw.Path != "/s3" || gw.Bucket != "s3" {
		t.Fatalf("storage.s3Gateway = %+v", gw)
	}
	if !strings.Contains(string(body), `"s3Gateway":{"enabled":true,"addr":"127.0.0.1:8090","path":"/s3","bucket":"s3","running":true}`) {
		t.Fatalf("status JSON = %s", body)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", resp.Header)
	}
}

type fakeGateway struct{}

func (fakeGateway) Status(tid string) storage.S3GatewayStatus {
	return storage.S3GatewayStatus{Enabled: true, Addr: "127.0.0.1:8090", Path: "/s3", Bucket: "s3", Running: true}
}

func TestAuthMethods(t *testing.T) {
	e := newEnv(t)
	basic := "Basic " + basicCreds("lydiabox", token)
	if resp, _ := e.do(t, "GET", "/api/v1/status", basic, nil, nil); resp.StatusCode != 200 {
		t.Fatalf("basic auth: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/status", "Basic "+basicCreds("root", token), nil, nil); resp.StatusCode != 401 {
		t.Fatalf("wrong basic user: %d", resp.StatusCode)
	}

	// Cookie session.
	resp, body := e.do(t, "POST", "/api/v1/auth/login", "-", strings.NewReader(`{"token":"wrong"}`), nil)
	if resp.StatusCode != 401 {
		t.Fatalf("bad login: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(t, "POST", "/api/v1/auth/login", "-", strings.NewReader(`{"token":"`+token+`"}`), nil)
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookie {
			cookie = c
		}
	}
	if resp.StatusCode != 200 || cookie == nil || !cookie.HttpOnly || cookie.Value == token || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login: %d cookie=%+v", resp.StatusCode, cookie)
	}
	ck := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	if _, body := e.do(t, "GET", "/api/v1/auth/session", "-", nil, ck); !strings.Contains(string(body), "true") {
		t.Fatalf("session: %s", body)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/status", "-", nil, ck); resp.StatusCode != 200 {
		t.Fatalf("cookie GET: %d", resp.StatusCode)
	}

	// Cross-origin state changes with a cookie are refused; same origin passes.
	mk := `{"path":"x"}`
	cross := map[string]string{"Cookie": ck["Cookie"], "Origin": "https://evil.example", "Content-Type": "application/json"}
	if resp, body := e.do(t, "POST", "/api/v1/fs/workspace/mkdir", "-", strings.NewReader(mk), cross); resp.StatusCode != 403 || errCode(t, body) != "forbidden" {
		t.Fatalf("cross-origin POST: %d %s", resp.StatusCode, body)
	}
	ws := map[string]string{"Cookie": ck["Cookie"], "Origin": "https://evil.example", "Upgrade": "websocket", "Connection": "Upgrade"}
	if resp, _ := e.do(t, "GET", "/api/v1/status", "-", nil, ws); resp.StatusCode != 403 {
		t.Fatalf("cross-origin websocket: %d", resp.StatusCode)
	}
	same := map[string]string{"Cookie": ck["Cookie"], "Origin": e.srv.URL}
	if resp, body := e.do(t, "POST", "/api/v1/fs/workspace/mkdir", "-", strings.NewReader(mk), same); resp.StatusCode != 201 {
		t.Fatalf("same-origin POST: %d %s", resp.StatusCode, body)
	}

	resp, _ = e.do(t, "POST", "/api/v1/auth/logout", "-", nil, nil)
	if resp.StatusCode != 204 || len(resp.Cookies()) != 1 || resp.Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout: %d %+v", resp.StatusCode, resp.Cookies())
	}
	if resp, _ := e.do(t, "GET", "/api/v1/status", "-", nil, map[string]string{"Cookie": api.SessionCookie + "=forged"}); resp.StatusCode != 401 {
		t.Fatal("forged cookie accepted")
	}
}

func TestFailedAuthRateLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 10; i++ {
		if resp, _ := e.do(t, "GET", "/api/v1/status", "Bearer nope", nil, nil); resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	resp, body := e.do(t, "GET", "/api/v1/status", "", nil, nil)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || errCode(t, body) != "rate_limited" {
		t.Fatalf("after 10 failures: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(t, "POST", "/api/v1/auth/login", "-", strings.NewReader(`{"token":"`+token+`"}`), nil)
	if resp.StatusCode != 429 {
		t.Fatalf("login while blocked: %d", resp.StatusCode)
	}
}

func TestWebDAVRouteAndWebUI(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.do(t, "PROPFIND", "/webdav/workspace/", "-", nil, nil)
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("webdav without auth: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = e.do(t, "PROPFIND", "/webdav/workspace/", "Basic "+basicCreds("lydiabox", token), nil, nil)
	if resp.StatusCode != 207 || resp.Header.Get("X-Dav") != "default PROPFIND /webdav/workspace/" {
		t.Fatalf("webdav: %d %q", resp.StatusCode, resp.Header.Get("X-Dav"))
	}

	resp, body := e.do(t, "GET", "/files", "-", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<html") || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("SPA route: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(t, "GET", "/api/v1/nope", "-", nil, nil)
	if resp.StatusCode != 404 || errCode(t, body) != "not_found" {
		t.Fatalf("unknown API route: %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "GET", "/assets/missing.js", "-", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("missing asset: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/", "-", nil, nil); resp.StatusCode != 405 {
		t.Fatalf("POST /: %d", resp.StatusCode)
	}
}

func TestDuplicateRoutePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a pattern twice must panic")
		}
	}()
	h := api.RegistrarFunc(func(r api.Router) { r.HandleFunc("GET /x", func(http.ResponseWriter, *http.Request) {}) })
	api.NewServer(api.Options{Auth: api.NewAuthenticator(token), TenantID: "default"}, h, h)
}

func TestPanicRecovery(t *testing.T) {
	auth := api.NewAuthenticator(token)
	s, err := api.NewServer(api.Options{Auth: auth, TenantID: "default", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		api.RegistrarFunc(func(r api.Router) {
			r.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") }, api.Public())
		}))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "internal") {
		t.Fatalf("panic: %d %s", rec.Code, rec.Body)
	}
}
