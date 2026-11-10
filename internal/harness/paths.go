package harness

import (
	"fmt"
	"path"
	"strings"
)

// Dirs resolves the host paths of the guest trees (storage.Provider).
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

// hostRoot maps a guest directory to (host root, path relative to it).
func hostRoot(d Dirs, tenantID, guestDir string) (string, string, error) {
	clean, err := CleanGuestDir(guestDir)
	if err != nil {
		return "", "", err
	}
	root, rel := GuestWorkspace, ""
	if clean == GuestHome || strings.HasPrefix(clean, GuestHome+"/") {
		root = GuestHome
	}
	rel = strings.TrimPrefix(strings.TrimPrefix(clean, root), "/")
	var host string
	if root == GuestHome {
		host, err = d.HomeDir(tenantID)
	} else {
		host, err = d.WorkspaceDir(tenantID)
	}
	if err != nil {
		return "", "", err
	}
	return host, rel, nil
}

func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}
