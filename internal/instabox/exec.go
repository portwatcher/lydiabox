package instabox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ExecRequest runs one command in a running machine as its login user
// (HOME /data/home). Set exactly one of Cmd (run with bash -lc) and Argv
// (run with no shell).
type ExecRequest struct {
	Cmd  string            `json:"cmd,omitempty"`
	Argv []string          `json:"argv,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
	Envs map[string]string `json:"envs,omitempty"`
	// Stdin is UTF-8 text of at most 1 MiB, then EOF.
	Stdin string `json:"stdin,omitempty"`
	// TimeoutSeconds defaults to 60 on the server; at most 600.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// NoHistory leaves the command out of the machine's exec history.
	NoHistory bool `json:"noHistory,omitempty"`
}

// ExecResult is the result of a command. A non-zero exit code is not an
// error.
type ExecResult struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exitCode"`
	TimedOut    bool   `json:"timedOut"`
	Truncated   bool   `json:"truncated"`
	Signal      string `json:"signal,omitempty"`
	InvalidUTF8 bool   `json:"invalidUtf8,omitempty"`
	OOMKilled   bool   `json:"oomKilled,omitempty"`
}

// maxExecTimeout is the server's exec timeout cap.
const maxExecTimeout = 600

func (r ExecRequest) validate() error {
	if (r.Cmd == "") == (len(r.Argv) == 0) {
		return errors.New("instabox: exec needs exactly one of cmd and argv")
	}
	if len(r.Argv) > 0 && r.Argv[0] == "" {
		return errors.New("instabox: exec argv[0] is empty")
	}
	if r.TimeoutSeconds < 0 || r.TimeoutSeconds > maxExecTimeout {
		return fmt.Errorf("instabox: exec timeout %d s is outside 0-%d", r.TimeoutSeconds, maxExecTimeout)
	}
	if len(r.Stdin) > 1<<20 {
		return errors.New("instabox: exec stdin is over 1 MiB; upload it with PutFile")
	}
	return nil
}

// execDeadline bounds an exec call: the command timeout plus a margin for
// a wake (45 s) and the network.
func execDeadline(ctx context.Context, r ExecRequest) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	t := r.TimeoutSeconds
	if t == 0 {
		t = 60
	}
	return context.WithTimeout(ctx, time.Duration(t)*time.Second+2*time.Minute)
}

// Exec runs a command and returns its output (each stream cut at 1 MiB).
// It sends X-Instabox-Exec-Keepalive, so that a proxy does not close a
// quiet request of a long command.
// Exec is not idempotent: a retry happens only after an answer that
// proves the command did not start (a rate or concurrency limit, or a
// machine that is still waking). A transport error is returned as is.
func (c *Client) Exec(ctx context.Context, id string, req ExecRequest) (*ExecResult, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := execDeadline(ctx, req)
	defer cancel()
	path := machinePath(id, "exec")
	resp, err := c.do(ctx, request{
		method: http.MethodPost, path: path, jsonBody: req, retry: retryNotStarted,
		header: map[string]string{"X-Instabox-Exec-Keepalive": "1"},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// With the keepalive header a long command's answer is 200, newlines
	// every 15 s, then the result, or {"error": Error, "status": N} when
	// it failed after the 200 went out.
	var raw json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("instabox: decode exec result: %w", err)
	}
	var late struct {
		Error  *ErrorDetail `json:"error"`
		Status int          `json:"status"`
	}
	if json.Unmarshal(raw, &late) == nil && late.Error != nil {
		return nil, &APIError{Method: http.MethodPost, Path: path, Status: late.Status, ErrorDetail: *late.Error}
	}
	var res ExecResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("instabox: decode exec result: %w", err)
	}
	return &res, nil
}

// ExecStream runs a command and copies its output to stdout and stderr
// (either may be nil) as the command writes it, from server-sent events.
// The result has empty Stdout and Stderr. Canceling ctx closes the stream,
// which kills the command.
func (c *Client) ExecStream(ctx context.Context, id string, req ExecRequest, stdout, stderr io.Writer) (*ExecResult, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := execDeadline(ctx, req)
	defer cancel()
	path := machinePath(id, "exec")
	resp, err := c.do(ctx, request{
		method: http.MethodPost, path: path, jsonBody: req,
		accept: "text/event-stream", retry: retryNotStarted,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A server without streaming answers with the plain result.
		var res ExecResult
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(&res); err != nil {
			return nil, fmt.Errorf("instabox: decode exec result: %w", err)
		}
		if err := writeOut(stdout, res.Stdout); err != nil {
			return nil, err
		}
		if err := writeOut(stderr, res.Stderr); err != nil {
			return nil, err
		}
		res.Stdout, res.Stderr = "", ""
		return &res, nil
	}
	var result *ExecResult
	err = readEvents(resp.Body, func(event, data string) (bool, error) {
		switch event {
		case "stdout", "stderr":
			var out struct {
				Data string `json:"data"`
			}
			if err := json.Unmarshal([]byte(data), &out); err != nil {
				return false, fmt.Errorf("instabox: decode exec %s event: %w", event, err)
			}
			w := stdout
			if event == "stderr" {
				w = stderr
			}
			return false, writeOut(w, out.Data)
		case "exit":
			result = &ExecResult{}
			if err := json.Unmarshal([]byte(data), result); err != nil {
				return false, fmt.Errorf("instabox: decode exec exit event: %w", err)
			}
			return true, nil
		case "error":
			apiErr := &APIError{Method: http.MethodPost, Path: path, Status: http.StatusOK}
			var body errorBody
			if json.Unmarshal([]byte(data), &body) == nil && body.Error != nil {
				apiErr.ErrorDetail = *body.Error
			} else {
				apiErr.Message = data
			}
			return true, apiErr
		}
		return false, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("instabox: exec stream ended without an exit event")
	}
	return result, nil
}

func writeOut(w io.Writer, s string) error {
	if w == nil || s == "" {
		return nil
	}
	_, err := io.WriteString(w, s)
	return err
}

// readEvents parses a text/event-stream body and calls fn for each event
// until fn returns done or an error, or the stream ends. Comment lines
// (": ping") are skipped.
func readEvents(r io.Reader, fn func(event, data string) (bool, error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var event string
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if event == "" && len(data) == 0 {
				continue
			}
			if event == "" {
				event = "message"
			}
			done, err := fn(event, strings.Join(data, "\n"))
			if err != nil || done {
				return err
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				data = append(data, value)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("instabox: read exec stream: %w", err)
	}
	return nil
}
