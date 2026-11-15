package harness

import (
	"fmt"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"path"
	"strings"
)

// Dirs resolves the host paths of the guest trees (storage.Provider in
// juicefs storage mode; used by LocalDiscovery).
type Dirs interface {
	HomeDir(tenantID string) (string, error)
	WorkspaceDir(tenantID string) (string, error)
}

// CleanGuestDir validates a guest directory: absolute, below /workspace or
// /home/agent, without "..". It returns the cleaned path.
func CleanGuestDir(dir string) (string, error) {
	if dir == "" {
		return GuestWorkspace, nil
	}
	if !path.IsAbs(dir) || strings.ContainsAny(dir, "\\\x00") {
		return "", fmt.Errorf("%w: workspaceDir must be an absolute guest path", ErrBadRequest)
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: workspaceDir must not contain a parent segment", ErrBadRequest)
		}
	}
	clean := path.Clean(dir)
	for _, root := range []string{GuestWorkspace, GuestHome} {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return clean, nil
		}
	}
	return "", fmt.Errorf("%w: workspaceDir must be inside %s or %s", ErrBadRequest, GuestWorkspace, GuestHome)
}

// guestRoot maps a guest directory to (agentfs root, path relative to it).
func guestRoot(guestDir string) (agentfs.Root, string, error) {
	clean, err := CleanGuestDir(guestDir)
	if err != nil {
		return "", "", err
	}
	root, prefix := agentfs.RootWorkspace, GuestWorkspace
	if clean == GuestHome || strings.HasPrefix(clean, GuestHome+"/") {
		root, prefix = agentfs.RootHome, GuestHome
	}
	return root, strings.TrimPrefix(strings.TrimPrefix(clean, prefix), "/"), nil
}
