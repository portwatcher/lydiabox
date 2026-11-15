package instabox

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// CreateSnapshot saves a machine as a snapshot. A stopped machine gives a
// ready snapshot at once; a running one a pending snapshot that becomes
// ready or failed (WaitSnapshot).
func (c *Client) CreateSnapshot(ctx context.Context, id string, req CreateSnapshotRequest) (*Snapshot, error) {
	var s Snapshot
	err := c.call(ctx, request{method: http.MethodPost, path: machinePath(id, "snapshots"), jsonBody: req, idempotent: true}, &s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSnapshots returns the snapshots of the key's workspace, oldest
// first.
func (c *Client) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	var list struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	if err := c.call(ctx, request{method: http.MethodGet, path: "/v1/snapshots"}, &list); err != nil {
		return nil, err
	}
	return list.Snapshots, nil
}

// GetSnapshot returns a snapshot by ID (sn_... or im_...).
func (c *Client) GetSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	var s Snapshot
	if err := c.call(ctx, request{method: http.MethodGet, path: "/v1/snapshots/" + url.PathEscape(id)}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// FindSnapshot returns the snapshot with the exact name, or ErrNotFound.
func (c *Client) FindSnapshot(ctx context.Context, name string) (*Snapshot, error) {
	list, err := c.ListSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, &APIError{Method: http.MethodGet, Path: "/v1/snapshots", Status: http.StatusNotFound,
		ErrorDetail: ErrorDetail{Code: CodeNotFound, Message: "no snapshot named " + name}}
}

// DeleteSnapshot deletes a snapshot; machines created from it stay. It
// sends X-Confirm-Delete with id, the snapshot's ID or exact name.
func (c *Client) DeleteSnapshot(ctx context.Context, id string) error {
	return c.call(ctx, request{
		method: http.MethodDelete, path: "/v1/snapshots/" + url.PathEscape(id), idempotent: true,
		header: map[string]string{"X-Confirm-Delete": id},
	}, nil)
}

// snapshotPoll is the interval of WaitSnapshot.
var snapshotPoll = 3 * time.Second

// WaitSnapshot polls a pending snapshot until it is ready or failed. A
// failed snapshot is returned with an *APIError.
func (c *Client) WaitSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	for {
		s, err := c.GetSnapshot(ctx, id)
		if err != nil {
			return nil, err
		}
		switch s.Status {
		case SnapshotReady:
			return s, nil
		case SnapshotFailed:
			return s, &APIError{Method: http.MethodGet, Path: "/v1/snapshots/" + id,
				ErrorDetail: ErrorDetail{Code: "snapshot_failed", Message: s.Error}}
		}
		if err := c.sleep(ctx, snapshotPoll); err != nil {
			return nil, err
		}
	}
}
