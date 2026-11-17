package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// Handler exposes Service over HTTP. internal/api/routes_fs.go maps the
// routes in docs/api.md to these methods.
type Handler struct {
	svc *Service
	// MaxUploadBytes bounds one upload request (default 10 GiB).
	MaxUploadBytes int64
}

// NewHandler wraps a Service.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, MaxUploadBytes: 10 << 30}
}

// writeErr maps service errors to the JSON error envelope. A request whose
// client went away gets 499: the error is then a result of the
// cancellation (context.Canceled, or a closed guest stream), not a fault.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, httpx.CodeInternal
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled):
		status, code = httpx.StatusClientClosedRequest, httpx.CodeCanceled
	case errors.Is(err, ErrInvalidPath), errors.Is(err, ErrIsRoot), errors.Is(err, ErrNotDir),
		errors.Is(err, ErrIsDir), errors.Is(err, ErrNotRegular):
		status, code = http.StatusBadRequest, httpx.CodeBadRequest
	case errors.Is(err, ErrEscapes):
		status, code = http.StatusForbidden, httpx.CodeForbidden
	case errors.Is(err, ErrUnknownRoot), errors.Is(err, fs.ErrNotExist):
		status, code = http.StatusNotFound, httpx.CodeNotFound
	case errors.Is(err, fs.ErrExist), errors.Is(err, ErrConflict):
		status, code = http.StatusConflict, httpx.CodeConflict
	case errors.Is(err, ErrTooLarge):
		status, code = http.StatusRequestEntityTooLarge, httpx.CodeTooLarge
	case errors.Is(err, ErrNotText):
		status, code = http.StatusUnsupportedMediaType, httpx.CodeUnsupported
	case errors.Is(err, fs.ErrPermission):
		status, code = http.StatusForbidden, httpx.CodeForbidden
	case errors.Is(err, ErrUnavailable):
		status, code = http.StatusServiceUnavailable, httpx.CodeUnavailable
		if errors.Is(err, ErrStopped) {
			// The user stopped the VM: an expected state, not a fault.
			httpx.NoteExpected(w)
		}
	}
	httpx.WriteError(w, status, code, err.Error())
}

func boolParam(r *http.Request, name string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(name))
	return v
}

// Roots handles GET /api/v1/fs/roots.
func (h *Handler) Roots(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"roots": h.svc.Roots(r.Context(), tenant.FromContext(r.Context()))})
}

// List handles GET /api/v1/fs/{root}/list?path=.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	self, entries, err := h.svc.List(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"root": r.PathValue("root"), "dir": self, "entries": entries})
}

// Stat handles GET /api/v1/fs/{root}/stat?path=.
func (h *Handler) Stat(w http.ResponseWriter, r *http.Request) {
	e, err := h.svc.Stat(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// ReadText handles GET /api/v1/fs/{root}/text?path=.
func (h *Handler) ReadText(w http.ResponseWriter, r *http.Request) {
	tf, err := h.svc.ReadText(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tf)
}

// SaveTextRequest is the body of PUT /api/v1/fs/{root}/text.
type SaveTextRequest struct {
	Content string `json:"content"`
	// BaseModTime enables optimistic concurrency: the save fails with 409
	// when the file changed since this modification time.
	BaseModTime *time.Time `json:"baseModTime,omitempty"`
}

// SaveText handles PUT /api/v1/fs/{root}/text?path=.
func (h *Handler) SaveText(w http.ResponseWriter, r *http.Request) {
	var req SaveTextRequest
	if err := httpx.DecodeJSON(w, r, h.svc.opts.MaxTextBytes*2+4096, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	e, err := h.svc.WriteText(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), r.URL.Query().Get("path"), req.Content, req.BaseModTime)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// Download handles GET /api/v1/fs/{root}/download?path=. It streams the file
// with Range and conditional request support.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	f, err := h.svc.OpenFile(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	defer f.Close()
	fi := f.Info()
	disposition := "attachment"
	if boolParam(r, "inline") {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": fi.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, fi.Name, fi.ModTime, f)
}

// Upload handles POST /api/v1/fs/{root}/upload?dir=&overwrite=. The body is
// multipart/form-data; every part named "file" is stored at dir/<filename>,
// where filename may hold a relative path ("photos/2026/a.jpg") for folder
// uploads. Parts are streamed to disk without buffering in memory.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	tid, root := tenant.FromContext(r.Context()), r.PathValue("root")
	dir, overwrite := r.URL.Query().Get("dir"), boolParam(r, "overwrite")
	if _, err := CleanRel(dir); err != nil {
		writeErr(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.MaxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "expected multipart/form-data: "+err.Error())
		return
	}
	results := []UploadResult{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			uploadFailed(w, r, results, err)
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		name, err := rawFilename(part.Header.Get("Content-Disposition"))
		if err != nil {
			part.Close()
			uploadFailed(w, r, results, err)
			return
		}
		res, err := h.svc.Upload(r.Context(), tid, root, dir, name, part, overwrite, h.MaxUploadBytes)
		part.Close()
		if err != nil {
			uploadFailed(w, r, results, err)
			return
		}
		results = append(results, res)
	}
	if len(results) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, `no part named "file" in the upload`)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"files": results})
}

// uploadFailed reports the first failing file; files stored before it remain.
func uploadFailed(w http.ResponseWriter, r *http.Request, done []UploadResult, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		err = fmt.Errorf("%w: upload exceeds %d bytes", ErrTooLarge, mbe.Limit)
	}
	if len(done) > 0 {
		err = fmt.Errorf("%w (%d file(s) stored before the failure)", err, len(done))
	}
	writeErr(w, r, err)
}

// rawFilename returns the filename parameter without the base-name reduction
// that multipart.Part.FileName applies, so relative folder paths survive.
func rawFilename(cd string) (string, error) {
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return "", fmt.Errorf("%w: bad Content-Disposition: %v", ErrInvalidPath, err)
	}
	name := params["filename"]
	if name == "" {
		return "", fmt.Errorf("%w: file part without filename", ErrInvalidPath)
	}
	if un, err := url.PathUnescape(name); err == nil && un != name {
		// Reject percent-encoded traversal such as "..%2Fetc".
		if _, err := CleanRel(un); err != nil {
			return "", err
		}
	}
	return name, nil
}

// MkdirRequest is the body of POST /api/v1/fs/{root}/mkdir.
type MkdirRequest struct {
	Path    string `json:"path"`
	Parents bool   `json:"parents"`
}

// Mkdir handles POST /api/v1/fs/{root}/mkdir.
func (h *Handler) Mkdir(w http.ResponseWriter, r *http.Request) {
	var req MkdirRequest
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	e, err := h.svc.Mkdir(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), req.Path, req.Parents)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, e)
}

// RenameRequest is the body of POST /api/v1/fs/{root}/rename.
type RenameRequest struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"`
}

// Rename handles POST /api/v1/fs/{root}/rename.
func (h *Handler) Rename(w http.ResponseWriter, r *http.Request) {
	var req RenameRequest
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	e, err := h.svc.Rename(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), req.From, req.To, req.Overwrite)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// Delete handles DELETE /api/v1/fs/{root}/entry?path=&recursive=.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	err := h.svc.Delete(r.Context(), tenant.FromContext(r.Context()), r.PathValue("root"), r.URL.Query().Get("path"), boolParam(r, "recursive"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
