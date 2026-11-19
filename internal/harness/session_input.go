package harness

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MaxInputBytes bounds the text of one prompt sent to a terminal.
const MaxInputBytes = 64 << 10

// Delays of the input of a prompt. submitDelay separates the pasted text
// from Enter: Codex and Claude Code read fast input followed by Enter as a
// paste and turn the Enter into a newline (paste-burst detection).
var (
	submitDelay    = 250 * time.Millisecond
	doubleEscDelay = 150 * time.Millisecond
)

// SendInput types a prompt into the terminal of a live session and
// submits it. A program that turned on bracketed paste mode gets the text
// as one paste; Enter follows after submitDelay.
func (m *Manager) SendInput(ctx context.Context, tenantID, id, text string) error {
	text = cleanInput(text)
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: the text is empty", ErrBadRequest)
	}
	if len(text) > MaxInputBytes {
		return fmt.Errorf("%w: the text is longer than %d bytes", ErrBadRequest, MaxInputBytes)
	}
	room, err := m.Room(ctx, tenantID, id)
	if err != nil {
		return err
	}
	payload := text
	if room.Activity().BracketedPaste {
		payload = "\x1b[200~" + text + "\x1b[201~"
	}
	if err := room.Write([]byte(payload)); err != nil {
		return fmt.Errorf("%w: terminal input: %v", ErrUnavailable, err)
	}
	if err := sleepCtx(ctx, submitDelay); err != nil {
		return err
	}
	if err := room.Write([]byte("\r")); err != nil {
		return fmt.Errorf("%w: terminal input: %v", ErrUnavailable, err)
	}
	return nil
}

// Interrupt stops the current turn of a live session: Escape for the
// harnesses (twice for opencode, which asks for a second press), Ctrl-C for
// a shell.
func (m *Manager) Interrupt(ctx context.Context, tenantID, id string) error {
	name, _, _, _, _, ok := m.liveInfo(tenantID, id)
	if !ok {
		var err error
		if name, _, err = SplitSessionID(id); err != nil {
			return err
		}
	}
	room, err := m.Room(ctx, tenantID, id)
	if err != nil {
		return err
	}
	keys := []string{"\x1b"}
	switch name {
	case Shell:
		keys = []string{"\x03"}
	case OpenCode:
		keys = []string{"\x1b", "\x1b"}
	}
	for i, k := range keys {
		if i > 0 {
			if err := sleepCtx(ctx, doubleEscDelay); err != nil {
				return err
			}
		}
		if err := room.Write([]byte(k)); err != nil {
			return fmt.Errorf("%w: terminal input: %v", ErrUnavailable, err)
		}
	}
	return nil
}

// cleanInput normalizes line ends and drops control characters other than
// newline and tab, so that a prompt cannot end the paste or send keys.
func cleanInput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return '\n'
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, s)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
