package magpie

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// magpie output goes to the host log; a token in it is masked.
func TestMagpieOutputMasksTokens(t *testing.T) {
	var buf bytes.Buffer
	w := logWriter{log: slog.New(slog.NewTextHandler(&buf, nil)), level: slog.LevelInfo}
	token := "sk-ant-ort01-" + strings.Repeat("y", 40)
	if _, err := w.Write([]byte("refreshed token " + token + "\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), token) || !strings.Contains(buf.String(), "[redacted]") {
		t.Fatalf("log keeps the token: %s", buf.String())
	}
}
