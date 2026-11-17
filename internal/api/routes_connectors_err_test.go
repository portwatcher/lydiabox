package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lydiabox/lydiabox/internal/httpx"
)

// TestConnectorErrorOfACanceledRequest: a page change cancels the load of
// the connectors list; that is not a server error (no 500, no ERROR line).
func TestConnectorErrorOfACanceledRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	ConnectorRoutes{}.writeErr(rec, fmt.Errorf("store: list connectors: %w", context.Canceled))
	if rec.Code != httpx.StatusClientClosedRequest {
		t.Fatalf("canceled: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	ConnectorRoutes{}.writeErr(rec, errors.New("disk full"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("other error: status %d", rec.Code)
	}
}
