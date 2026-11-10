package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GitInspector reads the git state of a workspace on the host.
type GitInspector interface {
	// Inspect returns `git status --short` and `git diff --stat` of dir (a
	// host path). A directory that is not a repository returns empty
	// strings and no error.
	Inspect(ctx context.Context, dir string) (status, diffStat string, err error)
}

// CommandRunner runs one command and returns its standard output.
type CommandRunner func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error)

// ExecGit inspects a workspace with the git binary. The repository is
// guest-controlled, so git runs with a neutralized configuration: no
// system or global config, no fsmonitor, hooks, pager, external diff,
// textconv, or filter drivers, and repositories whose config includes other
// files or whose .git points elsewhere are not inspected.
type ExecGit struct {
	// Run defaults to os/exec.
	Run     CommandRunner
	Timeout time.Duration
}

func osRun(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

var driverRe = regexp.MustCompile(`(?mi)^\s*\[\s*(filter|diff|merge)\s+"([^"]+)"\s*\]`)

// safeConfig returns -c overrides for a repository config, or an error when
// the repository must not be inspected.
func safeConfig(dir string) ([]string, error) {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil {
		return nil, errNotRepo
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory (worktree or symlink); not inspected", gitPath)
	}
	cfg, err := os.ReadFile(filepath.Join(gitPath, "config"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lower := strings.ToLower(string(cfg))
	if strings.Contains(lower, "[include") {
		return nil, errors.New("repository config includes other files; not inspected")
	}
	args := []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "core.untrackedCache=false",
		"-c", "safe.directory=*", "-c", "core.pager=cat", "-c", "core.attributesFile=/dev/null"}
	for _, m := range driverRe.FindAllStringSubmatch(string(cfg), -1) {
		kind, name := strings.ToLower(m[1]), m[2]
		switch kind {
		case "filter":
			args = append(args, "-c", "filter."+name+".clean=", "-c", "filter."+name+".smudge=",
				"-c", "filter."+name+".process=", "-c", "filter."+name+".required=false")
		case "diff":
			args = append(args, "-c", "diff."+name+".textconv=", "-c", "diff."+name+".command=")
		case "merge":
			args = append(args, "-c", "merge."+name+".driver=")
		}
	}
	return args, nil
}

var errNotRepo = errors.New("not a git repository")

// Inspect implements GitInspector.
func (g ExecGit) Inspect(ctx context.Context, dir string) (string, string, error) {
	cfg, err := safeConfig(dir)
	if errors.Is(err, errNotRepo) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	run := g.Run
	if run == nil {
		run = osRun
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		"GIT_DIR=" + filepath.Join(dir, ".git"), "GIT_WORK_TREE=" + dir, "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)}
	base := append([]string{"--no-pager"}, cfg...)
	status, err := run(ctx, dir, env, "git", append(base, "status", "--short", "--untracked-files=normal")...)
	if err != nil {
		return "", "", err
	}
	diff, err := run(ctx, dir, env, "git", append(base, "diff", "--stat", "--no-ext-diff", "--no-textconv", "--no-color")...)
	if err != nil {
		return "", "", err
	}
	return strings.TrimRight(status, "\n"), strings.TrimRight(diff, "\n"), nil
}
