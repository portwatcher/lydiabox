package instabox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// CreateMachine creates a machine (POST /v1/machines). The answer is the
// create operation; Operation.MachineID is the new machine. Follow it
// with WaitOperation.
func (c *Client) CreateMachine(ctx context.Context, req CreateMachineRequest) (*Operation, error) {
	if req.TemplateID != "" && req.SnapshotID != "" {
		return nil, errors.New("instabox: create from a template or a snapshot, not both")
	}
	var op Operation
	err := c.call(ctx, request{method: http.MethodPost, path: "/v1/machines", jsonBody: req, idempotent: true}, &op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// GetMachine returns a machine by ID (m_...) or name.
func (c *Client) GetMachine(ctx context.Context, id string) (*Machine, error) {
	var m Machine
	if err := c.call(ctx, request{method: http.MethodGet, path: machinePath(id)}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMachines returns one page of machines.
func (c *Client) ListMachines(ctx context.Context, opts ListMachinesOptions) (*MachineList, error) {
	q := url.Values{}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	for k, v := range opts.Metadata {
		q.Add("metadata", k+":"+v)
	}
	var list MachineList
	if err := c.call(ctx, request{method: http.MethodGet, path: "/v1/machines", query: q}, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// ListAllMachines follows nextCursor through every page.
func (c *Client) ListAllMachines(ctx context.Context, opts ListMachinesOptions) ([]Machine, error) {
	var all []Machine
	for {
		page, err := c.ListMachines(ctx, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Machines...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all, nil
		}
		opts.Cursor = *page.NextCursor
	}
}

// UpdateResult is the answer of UpdateMachine: the machine (a synchronous
// change) or an operation (a resize with Apply "restart").
type UpdateResult struct {
	Machine   *Machine
	Operation *Operation
}

// UpdateMachine changes a machine (PATCH /v1/machines/{id}).
func (c *Client) UpdateMachine(ctx context.Context, id string, req UpdateMachineRequest) (*UpdateResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	resp, err := c.do(ctx, request{method: http.MethodPatch, path: machinePath(id), jsonBody: req, idempotent: true})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, fmt.Errorf("instabox: read PATCH %s: %w", machinePath(id), err)
	}
	res := &UpdateResult{}
	if resp.StatusCode == http.StatusAccepted {
		res.Operation = &Operation{}
		err = json.Unmarshal(data, res.Operation)
	} else {
		res.Machine = &Machine{}
		err = json.Unmarshal(data, res.Machine)
	}
	if err != nil {
		return nil, fmt.Errorf("instabox: decode PATCH %s: %w", machinePath(id), err)
	}
	return res, nil
}

// SetIdlePause sets the idle time before the machine pauses itself
// (autoSuspendAfterSeconds). Paid plans take 5 to 30 minutes in whole
// minutes, or 0 to turn the idle pause off; the free plan takes only 30
// minutes.
func (c *Client) SetIdlePause(ctx context.Context, id string, after time.Duration) (*Machine, error) {
	if after < 0 || after%time.Minute != 0 {
		return nil, fmt.Errorf("instabox: idle pause %s is not a whole number of minutes", after)
	}
	secs := int(after / time.Second)
	res, err := c.UpdateMachine(ctx, id, UpdateMachineRequest{AutoSuspendAfterSeconds: &secs})
	if err != nil {
		return nil, err
	}
	return res.Machine, nil
}

// SetMaxRun sets the maximum continuous run in minutes (-1: unlimited).
func (c *Client) SetMaxRun(ctx context.Context, id string, minutes int) (*Machine, error) {
	if minutes < -1 {
		return nil, fmt.Errorf("instabox: invalid maximum run %d minutes", minutes)
	}
	res, err := c.UpdateMachine(ctx, id, UpdateMachineRequest{MaxRunMinutes: &minutes})
	if err != nil {
		return nil, err
	}
	return res.Machine, nil
}

// StartOptions are the optional fields of a start.
type StartOptions struct {
	VCPUs  int `json:"vcpus,omitempty"`
	MemMiB int `json:"memMiB,omitempty"`
}

// StartMachine starts a stopped machine, or resumes a paused one.
func (c *Client) StartMachine(ctx context.Context, id string, opts StartOptions) (*Operation, error) {
	return c.lifecycle(ctx, id, "start", opts)
}

// StopOptions are the optional fields of a stop.
type StopOptions struct {
	// TimeoutSeconds is how long to wait for a clean shutdown.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// StopMachine shuts a running machine down; files on /data stay. A
// paused machine answers ErrConflict: resume it first.
func (c *Client) StopMachine(ctx context.Context, id string, opts StopOptions) (*Operation, error) {
	return c.lifecycle(ctx, id, "stop", opts)
}

// PauseMachine pauses a running machine with its memory and processes
// (the API's suspend). Only Resume or Start bring it back.
func (c *Client) PauseMachine(ctx context.Context, id string) (*Operation, error) {
	return c.lifecycle(ctx, id, "suspend", struct{}{})
}

// ResumeMachine resumes a paused machine.
func (c *Client) ResumeMachine(ctx context.Context, id string) (*Operation, error) {
	return c.lifecycle(ctx, id, "resume", struct{}{})
}

func (c *Client) lifecycle(ctx context.Context, id, action string, body any) (*Operation, error) {
	var op Operation
	err := c.call(ctx, request{method: http.MethodPost, path: machinePath(id, action), jsonBody: body, idempotent: true}, &op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// DeleteMachine deletes a machine and its disks. It sends
// X-Confirm-Delete with id, which must be the machine's ID or exact name.
func (c *Client) DeleteMachine(ctx context.Context, id string) (*Operation, error) {
	var op Operation
	err := c.call(ctx, request{
		method: http.MethodDelete, path: machinePath(id), idempotent: true,
		header: map[string]string{"X-Confirm-Delete": id},
	}, &op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// WakeMachine resumes a machine that paused itself for idleness (it waits
// up to 45 s on the server) and records activity on a running one. A
// machine paused by PauseMachine or stopped stays down.
func (c *Client) WakeMachine(ctx context.Context, id string) (*Machine, error) {
	var m Machine
	if err := c.call(ctx, request{method: http.MethodPost, path: machinePath(id, "wake"), jsonBody: struct{}{}}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetOperation returns an operation. A positive wait long-polls (capped
// at 30 s by the server) until the operation changes or ends.
func (c *Client) GetOperation(ctx context.Context, id string, wait time.Duration) (*Operation, error) {
	q := url.Values{}
	if wait > 0 {
		q.Set("wait", strconv.Itoa(int((wait+time.Second-1)/time.Second))+"s")
	}
	var op Operation
	err := c.call(ctx, request{method: http.MethodGet, path: "/v1/operations/" + url.PathEscape(id), query: q}, &op)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// operationPoll is the long-poll wait of WaitOperation.
const operationPoll = 25 * time.Second

// WaitOperation long-polls op until it succeeds or fails. A failed
// operation returns an *APIError whose Operation is set and whose code is
// the operation's error code (for example no_capacity). ctx bounds the
// whole wait.
func (c *Client) WaitOperation(ctx context.Context, op *Operation) (*Operation, error) {
	cur := op
	for {
		switch cur.State {
		case OpSucceeded:
			return cur, nil
		case OpFailed:
			e := &APIError{Method: http.MethodGet, Path: "/v1/operations/" + cur.ID, Operation: cur}
			if cur.Error != nil {
				e.ErrorDetail = *cur.Error
				e.RetryAfter = time.Duration(cur.Error.RetryAfterSeconds) * time.Second
			}
			return cur, e
		}
		next, err := c.GetOperation(ctx, cur.ID, operationPoll)
		if err != nil {
			return cur, err
		}
		cur = next
	}
}

// ActivityLease keeps a machine from its idle pause until ExpiresAt.
type ActivityLease struct {
	ID        string    `json:"id"`
	MachineID string    `json:"machineId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// PutActivityLease creates or renews a task lease (30 to 3600 s; 0 is the
// server default of 300 s). It wakes an idle-paused machine first.
func (c *Client) PutActivityLease(ctx context.Context, id, leaseID string, ttl time.Duration) (*ActivityLease, error) {
	body := struct {
		TTLSeconds int `json:"ttlSeconds,omitempty"`
	}{int(ttl / time.Second)}
	var lease ActivityLease
	err := c.call(ctx, request{method: http.MethodPut, path: machinePath(id, "activity-leases", url.PathEscape(leaseID)), jsonBody: body}, &lease)
	if err != nil {
		return nil, err
	}
	return &lease, nil
}

// DeleteActivityLease releases a task lease; an absent lease is no error.
func (c *Client) DeleteActivityLease(ctx context.Context, id, leaseID string) error {
	return c.call(ctx, request{method: http.MethodDelete, path: machinePath(id, "activity-leases", url.PathEscape(leaseID))}, nil)
}
