package version

import (
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	info := Get()
	if info.Version == "" || info.GoVersion == "" || !strings.Contains(info.Platform, "/") {
		t.Fatalf("incomplete info: %+v", info)
	}
	if !strings.Contains(info.String(), info.Version) {
		t.Fatalf("String() = %q", info.String())
	}
}
