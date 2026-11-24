package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/portwatcher/lydiabox/internal/connector"
	"github.com/portwatcher/lydiabox/internal/storage/files"
)

var driveIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// googleExports maps Google-native types to their default export format.
var googleExports = map[string]struct{ mime, ext string }{
	"application/vnd.google-apps.document":     {"text/plain", ".txt"},
	"application/vnd.google-apps.spreadsheet":  {"text/csv", ".csv"},
	"application/vnd.google-apps.presentation": {"application/pdf", ".pdf"},
	"application/vnd.google-apps.drawing":      {"image/png", ".png"},
}

const driveFileFields = "id,name,mimeType,size,modifiedTime,webViewLink"

func gdriveTools() []tool {
	return []tool{
		{name: "gdrive_search", provider: connector.GDrive,
			description: "Search Google Drive files by name and full text.",
			props: map[string]any{"query": prop("string", "Text to search for"),
				"mime_type": prop("string", "Only return files of this MIME type"),
				"page_size": prop("integer", "Maximum results (1-100, default 20)")},
			required: []string{"query"}, run: gdriveSearch},
		{name: "gdrive_download_to_workspace", provider: connector.GDrive,
			description: "Download a Drive file into /workspace. Google Docs, Sheets, Slides, and Drawings are exported " +
				"(text, CSV, PDF, PNG unless export_mime_type is set). A path ending in / keeps the Drive file name.",
			props: map[string]any{"file_id": prop("string", "Drive file ID"),
				"path":             prop("string", "Target path relative to /workspace"),
				"overwrite":        prop("boolean", "Replace an existing file"),
				"export_mime_type": prop("string", "Export format for Google-native files")},
			required: []string{"file_id", "path"}, run: gdriveDownload},
		{name: "gdrive_upload_from_workspace", provider: connector.GDrive,
			description: "Upload a file from /workspace to Google Drive as a new file, or as a new revision of file_id.",
			props: map[string]any{"path": prop("string", "Source path relative to /workspace"),
				"name":      prop("string", "Drive file name (default: the source file name)"),
				"parent_id": prop("string", "Drive folder ID for a new file"),
				"file_id":   prop("string", "Existing Drive file to update"),
				"mime_type": prop("string", "Content type (default: from the file extension)")},
			required: []string{"path"}, run: gdriveUpload},
	}
}

// driveQuote escapes a value for a Drive query string literal.
func driveQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

type driveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	Size         string `json:"size,omitempty"`
	ModifiedTime string `json:"modifiedTime,omitempty"`
	WebViewLink  string `json:"webViewLink,omitempty"`
}

func gdriveSearch(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		Query    string `json:"query"`
		MimeType string `json:"mime_type"`
		PageSize int    `json:"page_size"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Query) == "" {
		return nil, invalidf("query is required")
	}
	q := "trashed = false and (name contains " + driveQuote(a.Query) + " or fullText contains " + driveQuote(a.Query) + ")"
	if a.MimeType != "" {
		q += " and mimeType = " + driveQuote(a.MimeType)
	}
	v := url.Values{"q": {q}, "pageSize": {strconv.Itoa(clamp(a.PageSize, 20, 1, 100))},
		"fields": {"files(" + driveFileFields + ")"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
	var out struct {
		Files []driveFile `json:"files"`
	}
	if err := c.do(http.MethodGet, c.p.API("/drive/v3/files?"+v.Encode()), nil, &out, nil); err != nil {
		return nil, err
	}
	if out.Files == nil {
		out.Files = []driveFile{}
	}
	return out, nil
}

// workspaceError turns sandbox and filesystem errors into tool errors.
func workspaceError(err error) error {
	switch {
	case errors.Is(err, files.ErrInvalidPath), errors.Is(err, files.ErrEscapes):
		return invalidf("%v", err)
	case errors.Is(err, fs.ErrExist):
		return invalidf("%v (set overwrite to replace it)", err)
	case errors.Is(err, fs.ErrNotExist):
		return invalidf("%v", err)
	case errors.Is(err, files.ErrTooLarge):
		return invalidf("file exceeds the transfer limit")
	}
	return err
}

func gdriveDownload(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		FileID         string `json:"file_id"`
		Path           string `json:"path"`
		Overwrite      bool   `json:"overwrite"`
		ExportMimeType string `json:"export_mime_type"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if !driveIDRe.MatchString(a.FileID) {
		return nil, invalidf("file_id %q is not a Drive file ID", a.FileID)
	}
	keepName := strings.HasSuffix(a.Path, "/")
	dir := strings.TrimSuffix(a.Path, "/")
	// Validate the target before any network call.
	if _, err := files.CleanRel(dir); err != nil || (dir == "" && !keepName) {
		if err == nil {
			err = invalidf("path is required")
		}
		return nil, workspaceError(err)
	}
	var meta driveFile
	if err := c.do(http.MethodGet, c.p.API("/drive/v3/files/"+a.FileID+"?"+url.Values{
		"fields": {driveFileFields}, "supportsAllDrives": {"true"}}.Encode()), nil, &meta, nil); err != nil {
		return nil, err
	}
	src := c.p.API("/drive/v3/files/" + a.FileID + "?alt=media&supportsAllDrives=true")
	contentType, name := meta.MimeType, meta.Name
	if strings.HasPrefix(meta.MimeType, "application/vnd.google-apps.") {
		exp, ok := googleExports[meta.MimeType]
		if a.ExportMimeType != "" {
			exp.mime, exp.ext, ok = a.ExportMimeType, "", true
		}
		if !ok {
			return nil, invalidf("Drive type %s cannot be downloaded; set export_mime_type", meta.MimeType)
		}
		src = c.p.API("/drive/v3/files/" + a.FileID + "/export?" + url.Values{"mimeType": {exp.mime}}.Encode())
		contentType, name = exp.mime, name+exp.ext
	}
	target := dir
	if keepName {
		base := path.Base(strings.ReplaceAll(name, "\\", "_"))
		if base == "." || base == ".." || base == "/" || base == "" {
			base = a.FileID
		}
		target = path.Join(dir, base)
	}
	req, err := connector.NewRequest(c.ctx, http.MethodGet, src, c.cred.AccessToken, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &connector.APIError{Provider: connector.GDrive, Message: "download failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &connector.APIError{Provider: connector.GDrive, Status: resp.StatusCode, Message: apiMessage(msg)}
	}
	res, err := c.b.files.Upload(c.ctx, c.b.tenantID, files.RootWorkspace, "", target, resp.Body, a.Overwrite, c.b.maxDown)
	if err != nil {
		return nil, workspaceError(err)
	}
	return map[string]any{"path": res.Path, "size": res.Size, "mimeType": contentType, "replaced": res.Replaced,
		"driveFile": meta}, nil
}

func apiMessage(body []byte) string {
	var v struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) == nil {
		return v.Error.Message
	}
	return ""
}

func gdriveUpload(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
		FileID   string `json:"file_id"`
		MimeType string `json:"mime_type"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.FileID != "" && !driveIDRe.MatchString(a.FileID) {
		return nil, invalidf("file_id %q is not a Drive file ID", a.FileID)
	}
	if a.ParentID != "" && !driveIDRe.MatchString(a.ParentID) {
		return nil, invalidf("parent_id %q is not a Drive folder ID", a.ParentID)
	}
	f, err := c.b.files.OpenFile(c.ctx, c.b.tenantID, files.RootWorkspace, a.Path)
	if err != nil {
		return nil, workspaceError(err)
	}
	defer f.Close()
	info := f.Info()
	if info.Size > c.b.maxUp {
		return nil, invalidf("file exceeds the %d byte upload limit", c.b.maxUp)
	}
	meta := map[string]any{}
	name := a.Name
	if name == "" && a.FileID == "" {
		name = info.Name
	}
	if name != "" {
		meta["name"] = name
	}
	if a.ParentID != "" && a.FileID == "" {
		meta["parents"] = []string{a.ParentID}
	}
	ctype := a.MimeType
	if ctype == "" {
		ctype = mime.TypeByExtension(path.Ext(info.Name))
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	prefix, suffix, boundary, err := relatedEnvelope(meta, ctype)
	if err != nil {
		return nil, err
	}
	q := url.Values{"uploadType": {"multipart"}, "supportsAllDrives": {"true"}, "fields": {driveFileFields}}.Encode()
	method, target := http.MethodPost, c.p.Upload("/drive/v3/files?"+q)
	if a.FileID != "" {
		method, target = http.MethodPatch, c.p.Upload("/drive/v3/files/"+a.FileID+"?"+q)
	}
	body := io.MultiReader(bytes.NewReader(prefix), io.LimitReader(f, info.Size), bytes.NewReader(suffix))
	req, err := connector.NewRequest(c.ctx, method, target, c.cred.AccessToken, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(prefix)) + info.Size + int64(len(suffix))
	req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)
	var out driveFile
	if err := connector.Do(c.http, req, connector.GDrive, &out); err != nil {
		return nil, err
	}
	return map[string]any{"uploaded": path.Clean(a.Path), "size": info.Size, "driveFile": out}, nil
}

// relatedEnvelope builds the multipart/related framing around the media part
// so the request has an exact Content-Length.
func relatedEnvelope(meta map[string]any, contentType string) (prefix, suffix []byte, boundary string, err error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	mh := textproto.MIMEHeader{}
	mh.Set("Content-Type", "application/json; charset=UTF-8")
	part, err := w.CreatePart(mh)
	if err != nil {
		return nil, nil, "", err
	}
	if err := json.NewEncoder(part).Encode(meta); err != nil {
		return nil, nil, "", err
	}
	dh := textproto.MIMEHeader{}
	dh.Set("Content-Type", contentType)
	if _, err := w.CreatePart(dh); err != nil {
		return nil, nil, "", err
	}
	prefix = append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	if err := w.Close(); err != nil {
		return nil, nil, "", err
	}
	return prefix, append([]byte(nil), buf.Bytes()...), w.Boundary(), nil
}
