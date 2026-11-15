package instabox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PutFileOptions are the optional query fields of PutFile.
type PutFileOptions struct {
	// Mode is the octal mode, for example 0600; 0 keeps an existing
	// file's mode or gives a new file 0644.
	Mode uint32
	// Parents creates missing parent directories.
	Parents bool
}

// FileWritten is the answer of PutFile.
type FileWritten struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Mode    string `json:"mode"`
	Created bool   `json:"created"`
}

// PutFile uploads body to path in a running machine, as the machine's
// user (no sudo). The server renames the file into place only when every
// byte arrived. A body that is an io.Seeker (bytes.Reader, os.File) is
// retried after a transient failure; any other body is sent once.
func (c *Client) PutFile(ctx context.Context, id, path string, body io.Reader, opts PutFileOptions) (*FileWritten, error) {
	if !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return nil, fmt.Errorf("instabox: file path %q must be absolute and name a file", path)
	}
	q := url.Values{"path": {path}}
	if opts.Mode != 0 {
		q.Set("mode", fmt.Sprintf("%04o", opts.Mode&0o777))
	}
	if opts.Parents {
		q.Set("parents", "true")
	}
	resp, err := c.do(ctx, request{
		method: http.MethodPut, path: machinePath(id, "files"), query: q,
		body: body, contentType: "application/octet-stream",
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var w FileWritten
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxErrorBody)).Decode(&w); err != nil {
		return nil, fmt.Errorf("instabox: decode file write: %w", err)
	}
	return &w, nil
}

// PutFileBytes uploads data to path.
func (c *Client) PutFileBytes(ctx context.Context, id, path string, data []byte, opts PutFileOptions) (*FileWritten, error) {
	return c.PutFile(ctx, id, path, bytes.NewReader(data), opts)
}

// FileContent is an open download. Close it.
type FileContent struct {
	io.ReadCloser
	// Size is Content-Length, or -1.
	Size int64
	// Mode is the octal mode from X-Instabox-File-Mode.
	Mode string
	// ETag names this version of the file, for a resumed download.
	ETag string
}

// GetFile opens the raw bytes of path in a running machine. The request
// is retried before the body starts; a body cut later is the caller's to
// resume (Range is not used by this client).
func (c *Client) GetFile(ctx context.Context, id, path string) (*FileContent, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodGet, path: machinePath(id, "files", "content"),
		query: url.Values{"path": {path}}, accept: "application/octet-stream",
	})
	if err != nil {
		return nil, err
	}
	return &FileContent{
		ReadCloser: resp.Body,
		Size:       resp.ContentLength,
		Mode:       resp.Header.Get("X-Instabox-File-Mode"),
		ETag:       resp.Header.Get("ETag"),
	}, nil
}

// ReadFile downloads path into memory; a file over limit bytes is an
// error (limit <= 0 means 64 MiB).
func (c *Client) ReadFile(ctx context.Context, id, path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 64 << 20
	}
	f, err := c.GetFile(ctx, id, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("instabox: read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("instabox: %s is larger than %d bytes", path, limit)
	}
	return data, nil
}

// FileEntry is one entry of a directory listing.
type FileEntry struct {
	Name    string    `json:"name"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
}

// FileInfo is the answer of StatFile: a directory with its entries, or a
// file with its text content (at most 256 KiB, empty when binary).
type FileInfo struct {
	Path      string      `json:"path"`
	Type      string      `json:"type"`
	Size      int64       `json:"size"`
	Mode      string      `json:"mode"`
	ModTime   time.Time   `json:"modTime"`
	Binary    bool        `json:"binary,omitempty"`
	Content   string      `json:"content,omitempty"`
	Entries   []FileEntry `json:"entries,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
}

// StatFile lists a directory or reads a small text file of a running
// machine (GET /v1/machines/{id}/files).
func (c *Client) StatFile(ctx context.Context, id, path string) (*FileInfo, error) {
	var fi FileInfo
	err := c.call(ctx, request{method: http.MethodGet, path: machinePath(id, "files"), query: url.Values{"path": {path}}}, &fi)
	if err != nil {
		return nil, err
	}
	return &fi, nil
}
