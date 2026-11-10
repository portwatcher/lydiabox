package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func invoke(argv0 string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(context.Background(), argv0, args, Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb})
	return code, out.String(), errb.String()
}

func TestGuestDispatch(t *testing.T) {
	if code, out, _ := invoke("/usr/local/bin/lydiabox-guest", "--version"); code != 0 || !strings.HasPrefix(out, "lydiabox-guest ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, out, _ := invoke("lydiabox-guest"); code != 2 || !strings.Contains(out, "serve") || !strings.Contains(out, "git-cred") {
		t.Fatalf("usage: %d %q", code, out)
	}
	// git invokes the helper through a symlink named git-credential-lydiabox.
	if code, _, errOut := invoke("/usr/local/bin/git-credential-lydiabox", "bogus"); code != 1 || !strings.Contains(errOut, "git-cred") {
		t.Fatalf("alias: %d %q", code, errOut)
	}
	if code, _, _ := invoke("lydiabox-guest", "nope"); code != 2 {
		t.Fatalf("unknown: %d", code)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	Register(Command{Name: "serve"})
}
