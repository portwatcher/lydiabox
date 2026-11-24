package instabox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	instaboximage "github.com/portwatcher/lydiabox/deploy/instabox"
)

func TestSystemRootCanBeQuoted(t *testing.T) {
	if strings.Contains(systemRoot, "'") {
		t.Fatal("systemRoot has a single quote; runSystem quotes it with single quotes")
	}
	cmd := runSystem(strings.Repeat("a", 64))
	if !strings.Contains(cmd, `sudo -n /bin/sh -c '`) || !strings.HasSuffix(cmd, "exit $rc\n") {
		t.Fatalf("command:\n%s", cmd)
	}
}

// TestSystemUpgradeInDocker runs the real command of the driver (the root
// exec and system-upgrade.sh) in a Debian 13 container (the release of the
// InstaBox template; LYDIABOX_TEST_DOCKER_IMAGE selects another, for
// example ubuntu:24.04) as UID 1000 with sudo, as on a machine: a machine
// of an old snapshot with no bubblewrap, no lbx link, and an old guest
// binary on the image path. It needs Docker and network access for
// apt-get: set LYDIABOX_TEST_DOCKER=1 (make instabox-system-test).
func TestSystemUpgradeInDocker(t *testing.T) {
	if os.Getenv("LYDIABOX_TEST_DOCKER") != "1" {
		t.Skip("set LYDIABOX_TEST_DOCKER=1 to run the system upgrade in a container")
	}
	image := os.Getenv("LYDIABOX_TEST_DOCKER_IMAGE")
	if image == "" {
		image = "debian:trixie"
	}
	b, err := instaboximage.System()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "bundle.tar")
	if err := os.WriteFile(good, b.Tar, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.tar")
	if err := os.WriteFile(bad, append(append([]byte(nil), b.Tar...), 0), 0o600); err != nil {
		t.Fatal(err)
	}

	name := fmt.Sprintf("lydiabox-system-test-%d", os.Getpid())
	docker(t, "run", "-d", "--name", name, image, "sleep", "3600")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	root := func(script string) string { t.Helper(); return docker(t, "exec", name, "sh", "-c", script) }
	root(`set -e
apt-get update -q >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends sudo git >/dev/null
getent passwd 1000 >/dev/null || useradd --uid 1000 --create-home --shell /bin/bash machine
u=$(getent passwd 1000 | cut -d: -f1)
echo "$u ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/u1000
chmod 0440 /etc/sudoers.d/u1000
mkdir -p /data/.lydiabox/bin
printf old >/usr/local/bin/lydiabox-guest
printf new >/data/.lydiabox/bin/lydiabox-guest
chmod 0755 /usr/local/bin/lydiabox-guest /data/.lydiabox/bin/lydiabox-guest
chown -R 1000:1000 /data
! command -v bwrap
! test -e /usr/local/bin/lbx`)

	run := func(tarPath, hash string) (string, string, int) {
		t.Helper()
		docker(t, "cp", tarPath, name+":"+systemUploadPath)
		root("chown 1000:1000 " + systemUploadPath + " && chmod 0600 " + systemUploadPath)
		c := exec.Command("docker", "exec", "-u", "1000", name, "bash", "-lc", runSystem(hash))
		var out, errb bytes.Buffer
		c.Stdout, c.Stderr = &out, &errb
		err := c.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), errb.String(), code
	}
	clean := func() {
		t.Helper()
		if got := root(`ls -d /run/lydiabox-system.* ` + systemUploadPath + ` 2>/dev/null || true`); got != "" {
			t.Fatalf("temporary files left: %s", got)
		}
	}
	state := `stat -c '%n %i %Y %a %u' /usr/local/bin/lydiabox-guest /usr/local/bin/lbx /usr/local/bin/git-credential-lydiabox ` +
		instaboximage.MarkerPath
	for _, f := range instaboximage.Files {
		state += " " + f.Dest
	}

	// A bundle that is not the uploaded one changes nothing.
	_, stderr, code := run(bad, b.SHA256)
	if code != 3 || !strings.Contains(stderr, "expected SHA-256") {
		t.Fatalf("bad bundle: exit %d, stderr %s", code, stderr)
	}
	clean()
	if root(`test -e /usr/local/bin/lbx && echo yes || true`) != "" {
		t.Fatal("a bad bundle changed the machine")
	}

	// The first run.
	stdout, stderr, code := run(good, b.SHA256)
	if code != 0 {
		t.Fatalf("first run: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	t.Logf("first run:\n%s", stdout)
	for _, want := range []string{"changed: /usr/local/bin/lbx -> /usr/local/bin/lydiabox-guest",
		"changed: /usr/local/bin/lydiabox-guest", "changed: installed the packages bubblewrap"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("no line %q in:\n%s", want, stdout)
		}
	}
	clean()
	checks := root(`set -e
command -v bwrap
readlink /usr/local/bin/lbx /usr/local/bin/git-credential-lydiabox
cat /usr/local/bin/lydiabox-guest; echo
git config --system --get credential.helper
cat ` + instaboximage.MarkerPath)
	want := "/usr/bin/bwrap\n/usr/local/bin/lydiabox-guest\n/usr/local/bin/lydiabox-guest\nnew\n" +
		"/usr/local/bin/lydiabox-guest git-cred\n" + b.SHA256
	if checks != want {
		t.Fatalf("machine state:\n%s\nwant:\n%s", checks, want)
	}
	for _, f := range instaboximage.Files {
		got := root(fmt.Sprintf(`stat -c '%%a %%u' %s && sha256sum %s | cut -c1-64`, f.Dest, f.Dest))
		data, _ := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "instabox", f.Src))
		sum := sha256.Sum256(data)
		if exp := fmt.Sprintf("%o 0\n%s", f.Mode, hex.EncodeToString(sum[:])); got != exp {
			t.Fatalf("%s: %q, want %q", f.Dest, got, exp)
		}
	}

	// A second run changes nothing.
	before := root(state)
	stdout, stderr, code = run(good, b.SHA256)
	if code != 0 || strings.Contains(stdout, "changed:") {
		t.Fatalf("second run: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if after := root(state); after != before {
		t.Fatalf("the second run changed files:\n%s\n%s", before, after)
	}
	clean()

	// A failed package step fails the run and writes no marker.
	root(`set -e
DEBIAN_FRONTEND=noninteractive apt-get remove -y -q bubblewrap >/dev/null
rm -f ` + instaboximage.MarkerPath + `
rm -rf /var/lib/apt/lists/* /etc/apt/sources.list.d/*
echo "deb http://invalid.invalid/debian trixie main" >/etc/apt/sources.list`)
	stdout, stderr, code = run(good, b.SHA256)
	if code == 0 || !strings.Contains(stderr, "ERROR: apt-get") {
		t.Fatalf("failed apt-get: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	t.Logf("failed apt-get, exit %d:\n%s", code, stderr)
	if root(`test -e `+instaboximage.MarkerPath+` && echo yes || true`) != "" {
		t.Fatal("a failed run wrote the marker")
	}
	clean()
}

// docker runs a docker command and returns its trimmed standard output.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	c := exec.Command("docker", args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		t.Fatalf("docker %s: %v\n%s\n%s", strings.Join(args[:min(len(args), 3)], " "), err, out.String(), errb.String())
	}
	return strings.TrimSpace(out.String())
}
