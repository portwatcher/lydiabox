package storage

import (
	"strings"
	"testing"
)

func TestFUSEHint(t *testing.T) {
	none := func(string) bool { return false }
	all := func(string) bool { return true }
	cases := []struct {
		goos   string
		exists func(string) bool
		want   string
	}{
		{"darwin", none, "macFUSE"},
		{"darwin", all, ""},
		{"linux", none, "/dev/fuse"},
		{"linux", all, ""},
		{"windows", none, ""},
	}
	for _, c := range cases {
		got := fuseHint(c.goos, c.exists)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("fuseHint(%s) = %q, want %q", c.goos, got, c.want)
		}
	}
}
