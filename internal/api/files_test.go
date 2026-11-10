package api_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/storage/files"
)

func basicCreds(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

type upload struct{ name, body string }

func multipartBody(t *testing.T, parts ...upload) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("note", "ignored field")
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		// Write the raw filename like a browser does for webkitRelativePath.
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, p.name))
		h.Set("Content-Type", "application/octet-stream")
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, p.body)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func q(p string) string { return url.QueryEscape(p) }

func TestFileAPICRUD(t *testing.T) {
	e := newEnv(t)
	jsonHdr := map[string]string{"Content-Type": "application/json"}

	resp, body := e.do(t, "GET", "/api/v1/fs/roots", "", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"guestPath":"/home/agent"`) {
		t.Fatalf("roots: %d %s", resp.StatusCode, body)
	}

	// mkdir
	resp, body = e.do(t, "POST", "/api/v1/fs/workspace/mkdir", "", strings.NewReader(`{"path":"proj/src","parents":true}`), jsonHdr)
	if resp.StatusCode != 201 || decode[files.Entry](t, body).Type != "dir" {
		t.Fatalf("mkdir: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(t, "POST", "/api/v1/fs/workspace/mkdir", "", strings.NewReader(`{"path":"proj"}`), jsonHdr)
	if resp.StatusCode != 409 || errCode(t, body) != "conflict" {
		t.Fatalf("mkdir existing: %d %s", resp.StatusCode, body)
	}

	// save and read text
	resp, body = e.do(t, "PUT", "/api/v1/fs/workspace/text?path="+q("proj/src/main.go"), "",
		strings.NewReader(`{"content":"package main\n"}`), jsonHdr)
	if resp.StatusCode != 200 {
		t.Fatalf("save: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(t, "GET", "/api/v1/fs/workspace/text?path="+q("proj/src/main.go"), "", nil, nil)
	tf := decode[files.TextFile](t, body)
	if resp.StatusCode != 200 || tf.Content != "package main\n" || tf.Name != "main.go" {
		t.Fatalf("read: %d %s", resp.StatusCode, body)
	}
	stale := tf.ModTime.Add(-time.Minute).Format(time.RFC3339Nano)
	resp, body = e.do(t, "PUT", "/api/v1/fs/workspace/text?path="+q("proj/src/main.go"), "",
		strings.NewReader(`{"content":"x","baseModTime":"`+stale+`"}`), jsonHdr)
	if resp.StatusCode != 409 {
		t.Fatalf("stale save: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(t, "PUT", "/api/v1/fs/workspace/text?path=a.txt", "", strings.NewReader(`{"content":"x","extra":1}`), jsonHdr)
	if resp.StatusCode != 400 {
		t.Fatalf("unknown field: %d %s", resp.StatusCode, body)
	}

	// folder upload with relative paths
	mb, ct := multipartBody(t, upload{"photos/2026/a.txt", "AAAA"}, upload{"photos/b.bin", "0123456789"})
	resp, body = e.do(t, "POST", "/api/v1/fs/home/upload?dir="+q("media"), "", mb, map[string]string{"Content-Type": ct})
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	up := decode[struct{ Files []files.UploadResult }](t, body)
	if len(up.Files) != 2 || up.Files[0].Path != "media/photos/2026/a.txt" || up.Files[1].Size != 10 {
		t.Fatalf("upload result = %+v", up)
	}
	if data, _ := os.ReadFile(filepath.Join(e.dirs.home, "media/photos/2026/a.txt")); string(data) != "AAAA" {
		t.Fatal("uploaded content mismatch")
	}
	mb, ct = multipartBody(t, upload{"photos/b.bin", "new"})
	if resp, _ := e.do(t, "POST", "/api/v1/fs/home/upload?dir=media", "", mb, map[string]string{"Content-Type": ct}); resp.StatusCode != 409 {
		t.Fatalf("upload conflict: %d", resp.StatusCode)
	}
	mb, ct = multipartBody(t, upload{"photos/b.bin", "new"})
	if resp, _ := e.do(t, "POST", "/api/v1/fs/home/upload?dir=media&overwrite=true", "", mb, map[string]string{"Content-Type": ct}); resp.StatusCode != 201 {
		t.Fatalf("upload overwrite: %d", resp.StatusCode)
	}
	if resp, body := e.do(t, "POST", "/api/v1/fs/home/upload", "", strings.NewReader("x"), map[string]string{"Content-Type": "text/plain"}); resp.StatusCode != 400 {
		t.Fatalf("non-multipart upload: %d %s", resp.StatusCode, body)
	}

	// list and stat
	resp, body = e.do(t, "GET", "/api/v1/fs/home/list?path=media/photos", "", nil, nil)
	list := decode[struct {
		Dir     files.Entry
		Entries []files.Entry
	}](t, body)
	if resp.StatusCode != 200 || list.Dir.Path != "media/photos" || len(list.Entries) != 2 || list.Entries[0].Name != "2026" || list.Entries[1].Name != "b.bin" {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(t, "GET", "/api/v1/fs/home/stat?path=media/photos/b.bin", "", nil, nil)
	if resp.StatusCode != 200 || decode[files.Entry](t, body).Size != 3 {
		t.Fatalf("stat: %d %s", resp.StatusCode, body)
	}

	// download, full and ranged
	resp, body = e.do(t, "GET", "/api/v1/fs/home/download?path="+q("media/photos/2026/a.txt"), "", nil, nil)
	if resp.StatusCode != 200 || string(body) != "AAAA" || !strings.Contains(resp.Header.Get("Content-Disposition"), `attachment; filename=a.txt`) {
		t.Fatalf("download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	os.WriteFile(filepath.Join(e.dirs.workspace, "big.bin"), []byte("0123456789"), 0o644)
	resp, body = e.do(t, "GET", "/api/v1/fs/workspace/download?path=big.bin", "", nil, map[string]string{"Range": "bytes=2-5"})
	if resp.StatusCode != 206 || string(body) != "2345" {
		t.Fatalf("range download: %d %q", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/fs/workspace/download?path=proj", "", nil, nil); resp.StatusCode != 400 {
		t.Fatalf("download dir: %d", resp.StatusCode)
	}

	// rename
	resp, body = e.do(t, "POST", "/api/v1/fs/workspace/rename", "", strings.NewReader(`{"from":"big.bin","to":"proj/big.bin"}`), jsonHdr)
	if resp.StatusCode != 200 || decode[files.Entry](t, body).Path != "proj/big.bin" {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(t, "POST", "/api/v1/fs/workspace/rename", "", strings.NewReader(`{"from":"proj","to":"../outside"}`), jsonHdr)
	if resp.StatusCode != 400 {
		t.Fatalf("rename outside: %d", resp.StatusCode)
	}

	// delete
	if resp, _ := e.do(t, "DELETE", "/api/v1/fs/workspace/entry?path=proj", "", nil, nil); resp.StatusCode == 204 {
		t.Fatal("non-recursive delete of a non-empty dir must fail")
	}
	if resp, _ := e.do(t, "DELETE", "/api/v1/fs/workspace/entry?path=proj&recursive=true", "", nil, nil); resp.StatusCode != 204 {
		t.Fatalf("recursive delete: %d", resp.StatusCode)
	}
	if resp, body := e.do(t, "GET", "/api/v1/fs/workspace/stat?path=proj", "", nil, nil); resp.StatusCode != 404 || errCode(t, body) != "not_found" {
		t.Fatalf("stat deleted: %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "DELETE", "/api/v1/fs/workspace/entry?path=", "", nil, nil); resp.StatusCode != 400 {
		t.Fatalf("delete root: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/fs/etc/list", "", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown root: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/fs/workspace/list", "-", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("file API without auth: %d", resp.StatusCode)
	}
}

func TestFileAPITraversal(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(filepath.Join(e.secret, "id_rsa"), []byte("PRIVATE"), 0o600)
	os.Symlink(e.secret, filepath.Join(e.dirs.workspace, "escape"))

	for _, p := range []string{"../secret/id_rsa", "/etc/passwd", "a/../../x", `..\secret`, "proj/../.."} {
		resp, body := e.do(t, "GET", "/api/v1/fs/workspace/text?path="+q(p), "", nil, nil)
		if resp.StatusCode != 400 || errCode(t, body) != "bad_request" {
			t.Errorf("path %q: %d %s", p, resp.StatusCode, body)
		}
	}
	for _, p := range []string{"escape/id_rsa", "escape"} {
		resp, body := e.do(t, "GET", "/api/v1/fs/workspace/download?path="+q(p), "", nil, nil)
		if resp.StatusCode != 403 || strings.Contains(string(body), "PRIVATE") {
			t.Errorf("symlink %q: %d %s", p, resp.StatusCode, body)
		}
	}
	resp, body := e.do(t, "GET", "/api/v1/fs/workspace/list?path=escape", "", nil, nil)
	if resp.StatusCode != 403 || errCode(t, body) != "forbidden" {
		t.Fatalf("list symlink: %d %s", resp.StatusCode, body)
	}

	for _, name := range []string{"../evil.txt", "/abs.txt", "a/../../evil.txt", "..%2Fevil.txt"} {
		mb, ct := multipartBody(t, upload{name, "pwned"})
		resp, body := e.do(t, "POST", "/api/v1/fs/workspace/upload", "", mb, map[string]string{"Content-Type": ct})
		if resp.StatusCode != 400 {
			t.Errorf("upload %q: %d %s", name, resp.StatusCode, body)
		}
	}
	mb, ct := multipartBody(t, upload{"x.txt", "pwned"})
	if resp, _ := e.do(t, "POST", "/api/v1/fs/workspace/upload?dir=escape", "", mb, map[string]string{"Content-Type": ct}); resp.StatusCode != 403 {
		t.Errorf("upload into symlink: %d", resp.StatusCode)
	}
	mb, ct = multipartBody(t, upload{"x.txt", "pwned"})
	if resp, _ := e.do(t, "POST", "/api/v1/fs/workspace/upload?dir=..", "", mb, map[string]string{"Content-Type": ct}); resp.StatusCode != 400 {
		t.Errorf("upload dir=..: %d", resp.StatusCode)
	}
	entries, _ := os.ReadDir(e.secret)
	if len(entries) != 1 {
		t.Fatalf("files escaped into the secret dir: %v", entries)
	}
	parent := filepath.Dir(e.dirs.workspace)
	if _, err := os.Stat(filepath.Join(parent, "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("upload escaped the root")
	}
}
