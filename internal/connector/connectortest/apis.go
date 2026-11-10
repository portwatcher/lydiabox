package connectortest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/lydiabox/lydiabox/internal/connector"
)

// Fixture identifiers served by the fake APIs.
const (
	NotionPageID = "0123456789abcdef0123456789abcdef"
	SlackTeam    = "acme"
)

func (f *Fake) unauthorized(w http.ResponseWriter) {
	f.writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
}

func (f *Fake) github(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if strings.HasPrefix(path, "/applications/") && r.Method == http.MethodDelete {
		id, secret, _ := r.BasicAuth()
		var v struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(body, &v)
		if id != ClientID || secret != ClientSecret || path != "/applications/"+ClientID+"/grant" {
			f.unauthorized(w)
			return
		}
		f.RevokeToken(v.AccessToken)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ti, ok := f.bearer(r, connector.GitHub)
	if !ok {
		f.unauthorized(w)
		return
	}
	switch {
	case path == "/user":
		w.Header().Set("X-OAuth-Scopes", "repo, read:org")
		f.writeJSON(w, http.StatusOK, map[string]any{"login": ti.Label, "id": 1})
	case path == "/user/repos":
		f.writeJSON(w, http.StatusOK, []map[string]any{{"full_name": "octo/hello", "private": false,
			"default_branch": "main", "html_url": "https://github.com/octo/hello"}})
	case path == "/repos/octo/hello/issues" && r.Method == http.MethodGet:
		f.writeJSON(w, http.StatusOK, []map[string]any{
			{"number": 1, "title": "Bug", "state": "open", "user": map[string]string{"login": "octo"},
				"labels": []map[string]string{{"name": "bug"}}},
			{"number": 2, "title": "A pull request", "state": "open", "pull_request": map[string]string{}},
		})
	case path == "/repos/octo/hello/issues" && r.Method == http.MethodPost:
		f.writeJSON(w, http.StatusCreated, map[string]any{"number": 3, "html_url": "https://github.com/octo/hello/issues/3", "state": "open"})
	case path == "/repos/octo/hello/pulls" && r.Method == http.MethodGet:
		f.writeJSON(w, http.StatusOK, []map[string]any{{"number": 2, "title": "Feature", "state": "open",
			"head": map[string]string{"ref": "feature"}, "base": map[string]string{"ref": "main"}}})
	case path == "/repos/octo/hello/pulls" && r.Method == http.MethodPost:
		f.writeJSON(w, http.StatusCreated, map[string]any{"number": 4, "html_url": "https://github.com/octo/hello/pull/4", "state": "open"})
	case path == "/repos/octo/hello/contents/README.md":
		f.writeJSON(w, http.StatusOK, map[string]any{"type": "file", "path": "README.md", "size": 11, "sha": "abc",
			"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("hello world"))})
	default:
		f.writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

func (f *Fake) drive(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if _, ok := f.bearer(r, connector.GDrive); !ok {
		f.writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401, "message": "Invalid Credentials"}})
		return
	}
	ti, _ := f.bearer(r, connector.GDrive)
	switch {
	case path == "/drive/v3/about":
		f.writeJSON(w, http.StatusOK, map[string]any{"user": map[string]string{"emailAddress": ti.Label}})
	case path == "/drive/v3/files" && r.Method == http.MethodGet:
		list := []map[string]string{}
		f.mu.Lock()
		for id, file := range f.files {
			list = append(list, map[string]string{"id": id, "name": file.Name, "mimeType": file.MimeType})
		}
		f.mu.Unlock()
		f.writeJSON(w, http.StatusOK, map[string]any{"files": list})
	case strings.HasPrefix(path, "/drive/v3/files/"):
		id, export := strings.CutSuffix(strings.TrimPrefix(path, "/drive/v3/files/"), "/export")
		f.mu.Lock()
		file, ok := f.files[id]
		f.mu.Unlock()
		if !ok {
			f.writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "File not found"}})
			return
		}
		switch {
		case export:
			w.Header().Set("Content-Type", r.URL.Query().Get("mimeType"))
			_, _ = w.Write(append([]byte("EXPORT:"), file.Content...))
		case r.URL.Query().Get("alt") == "media":
			w.Header().Set("Content-Type", file.MimeType)
			_, _ = w.Write(file.Content)
		default:
			f.writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": file.Name, "mimeType": file.MimeType,
				"size": fmt.Sprint(len(file.Content))})
		}
	case strings.HasPrefix(path, "/upload/drive/v3/files"):
		f.driveUpload(w, r, strings.TrimPrefix(strings.TrimPrefix(path, "/upload/drive/v3/files"), "/"), body)
	default:
		f.writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "not found"}})
	}
}

func (f *Fake) driveUpload(w http.ResponseWriter, r *http.Request, id string, body []byte) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" || r.URL.Query().Get("uploadType") != "multipart" ||
		r.ContentLength != int64(len(body)) {
		f.writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "bad upload"}})
		return
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	metaPart, err := mr.NextPart()
	if err != nil {
		f.writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "no metadata"}})
		return
	}
	var meta struct {
		Name    string   `json:"name"`
		Parents []string `json:"parents"`
	}
	_ = json.NewDecoder(metaPart).Decode(&meta)
	media, err := mr.NextPart()
	if err != nil {
		f.writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "no media"}})
		return
	}
	content, _ := io.ReadAll(media)
	f.mu.Lock()
	if id == "" {
		f.seq++
		id = fmt.Sprintf("upload%d", f.seq)
	} else if old, ok := f.files[id]; ok && meta.Name == "" {
		meta.Name = old.Name
	}
	f.files[id] = DriveFile{Name: meta.Name, MimeType: media.Header.Get("Content-Type"), Content: content}
	f.mu.Unlock()
	f.writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": meta.Name, "mimeType": media.Header.Get("Content-Type"),
		"size": fmt.Sprint(len(content))})
}

func (f *Fake) notion(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if path == "/v1/oauth/revoke" && r.Method == http.MethodPost {
		id, secret, _ := r.BasicAuth()
		var v struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(body, &v)
		if id != ClientID || secret != ClientSecret || r.Header.Get("Notion-Version") == "" {
			f.unauthorized(w)
			return
		}
		f.RevokeToken(v.Token)
		f.writeJSON(w, http.StatusOK, map[string]string{"request_id": "req-1"})
		return
	}
	ti, ok := f.bearer(r, connector.Notion)
	if !ok || r.Header.Get("Notion-Version") == "" {
		f.writeJSON(w, http.StatusUnauthorized, map[string]string{"object": "error", "code": "unauthorized", "message": "API token is invalid."})
		return
	}
	page := map[string]any{"object": "page", "id": NotionPageID, "url": "https://www.notion.so/" + NotionPageID,
		"properties": map[string]any{"Name": map[string]any{"type": "title", "title": []map[string]string{{"plain_text": "Plan"}}}}}
	switch {
	case path == "/v1/users/me":
		f.writeJSON(w, http.StatusOK, map[string]any{"object": "user", "id": "bot-1", "name": "LydiaBox",
			"bot": map[string]any{"workspace_name": ti.Label}})
	case path == "/v1/search":
		f.writeJSON(w, http.StatusOK, map[string]any{"results": []any{page}, "has_more": false})
	case path == "/v1/pages/"+NotionPageID:
		f.writeJSON(w, http.StatusOK, page)
	case path == "/v1/blocks/"+NotionPageID+"/children" && r.Method == http.MethodGet:
		f.mu.Lock()
		blocks := append([]map[string]any{}, f.notionBlocks...)
		f.mu.Unlock()
		f.writeJSON(w, http.StatusOK, map[string]any{"results": blocks, "has_more": false})
	case path == "/v1/blocks/"+NotionPageID+"/children" && r.Method == http.MethodPatch:
		var in struct {
			Children []map[string]any `json:"children"`
		}
		_ = json.Unmarshal(body, &in)
		// Notion answers rich text with plain_text filled in.
		for _, blk := range in.Children {
			typ, _ := blk["type"].(string)
			content, _ := blk[typ].(map[string]any)
			rts, _ := content["rich_text"].([]any)
			for _, rt := range rts {
				m, _ := rt.(map[string]any)
				if txt, ok := m["text"].(map[string]any); ok {
					m["plain_text"] = txt["content"]
				}
			}
		}
		f.mu.Lock()
		f.notionBlocks = append(f.notionBlocks, in.Children...)
		f.mu.Unlock()
		f.writeJSON(w, http.StatusOK, map[string]any{"results": in.Children})
	default:
		f.writeJSON(w, http.StatusNotFound, map[string]string{"object": "error", "message": "not found"})
	}
}

func (f *Fake) slack(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	ti, ok := f.bearer(r, connector.Slack)
	if !ok {
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	switch path {
	case "/api/auth.test":
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": ti.Label, "team": SlackTeam})
	case "/api/auth.revoke":
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.RevokeToken(tok)
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": true})
	case "/api/conversations.list":
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": []map[string]any{
			{"id": "C1", "name": "general", "is_member": true, "num_members": 3}}})
	case "/api/chat.postMessage":
		var in struct {
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal(body, &in)
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": in.Channel, "ts": "1700000000.000100"})
	case "/api/search.messages":
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": map[string]any{"total": 1, "matches": []map[string]any{
			{"channel": map[string]string{"id": "C1", "name": "general"}, "username": "octo", "text": "deploy " + r.URL.Query().Get("query"),
				"ts": "1700000000.000100"}}}})
	default:
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown_method"})
	}
}
