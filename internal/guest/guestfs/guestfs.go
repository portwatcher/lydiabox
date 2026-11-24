//go:build unix

// Package guestfs serves the persistent guest trees to the host in the
// provider storage mode (internal/guest/rpc fs.* methods). Each root is a
// configured guest directory (/home/agent and /workspace by default;
// InstaBox uses /data/home). Every path is cleaned with agentfs.CleanRel and
// resolved through an os.Root opened on the root directory, so "..",
// absolute paths, and symlinks that leave the root are refused by the
// lookup itself. When the daemon runs as root, file operations run with the
// file system identity of the agent user (Linux setfsuid), so they cannot
// touch files the agent user cannot touch.
package guestfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/portwatcher/lydiabox/internal/agentfs"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// Identity is the file system identity of the agent user.
type Identity struct {
	UID, GID int
	Groups   []int
}

// Options configures a Service.
type Options struct {
	// Home and Workspace are the absolute guest directories of the roots.
	Home      string
	Workspace string
	// As, when set, runs every file operation with this identity. It needs
	// root on Linux.
	As *Identity
}

// Service performs sandboxed file operations inside the guest roots.
type Service struct {
	roots map[agentfs.Root]string
	as    *Identity
}

// New validates opts and returns a Service.
func New(opts Options) (*Service, error) {
	if opts.Home == "" {
		opts.Home = agentfs.DefaultGuestHome
	}
	if opts.Workspace == "" {
		opts.Workspace = agentfs.DefaultGuestWorkspace
	}
	for _, d := range []string{opts.Home, opts.Workspace} {
		if !filepath.IsAbs(d) {
			return nil, fmt.Errorf("guestfs: root %q must be an absolute path", d)
		}
	}
	if opts.As != nil && os.Geteuid() != 0 {
		return nil, errors.New("guestfs: switching the file system identity needs root")
	}
	if opts.As != nil && !canSwitchIdentity {
		return nil, errors.New("guestfs: the guest daemon runs as root, which is supported only on Linux")
	}
	return &Service{
		roots: map[agentfs.Root]string{agentfs.RootHome: filepath.Clean(opts.Home), agentfs.RootWorkspace: filepath.Clean(opts.Workspace)},
		as:    opts.As,
	}, nil
}

// FromEnv reads the roots and the identity from the guest environment
// (rpc.EnvGuestHomeRoot, rpc.EnvGuestWorkspaceRoot, rpc.EnvGuestFSUser).
// The identity is set only when the daemon runs as root.
func FromEnv() (Options, error) {
	opts := Options{Home: os.Getenv(rpc.EnvGuestHomeRoot), Workspace: os.Getenv(rpc.EnvGuestWorkspaceRoot)}
	if os.Geteuid() != 0 {
		return opts, nil
	}
	name := os.Getenv(rpc.EnvGuestFSUser)
	if name == "" {
		name = "agent"
	}
	id, err := LookupIdentity(name)
	if err != nil {
		return opts, err
	}
	opts.As = id
	return opts, nil
}

// LookupIdentity resolves a user name or numeric uid. A missing "agent"
// user falls back to uid and gid 1000.
func LookupIdentity(name string) (*Identity, error) {
	u, err := user.Lookup(name)
	if err != nil {
		u, err = user.LookupId(name)
	}
	if err != nil {
		if name == "agent" {
			return &Identity{UID: 1000, GID: 1000}, nil
		}
		return nil, fmt.Errorf("guestfs: file system user %q: %w", name, err)
	}
	id := &Identity{}
	if id.UID, err = strconv.Atoi(u.Uid); err != nil {
		return nil, fmt.Errorf("guestfs: uid of %q: %w", name, err)
	}
	if id.GID, err = strconv.Atoi(u.Gid); err != nil {
		return nil, fmt.Errorf("guestfs: gid of %q: %w", name, err)
	}
	if gids, err := u.GroupIds(); err == nil {
		for _, g := range gids {
			if n, err := strconv.Atoi(g); err == nil {
				id.Groups = append(id.Groups, n)
			}
		}
	}
	return id, nil
}

// Dir returns the guest directory of a root.
func (s *Service) Dir(root agentfs.Root) (string, error) {
	d, ok := s.roots[root]
	if !ok {
		return "", fmt.Errorf("%w %q", agentfs.ErrUnknownRoot, root)
	}
	return d, nil
}

// Roots lists the roots in display order.
func (s *Service) Roots() []agentfs.RootInfo {
	out := make([]agentfs.RootInfo, 0, len(agentfs.RootNames))
	for _, n := range agentfs.RootNames {
		out = append(out, agentfs.RootInfo{Name: n, GuestPath: s.roots[n]})
	}
	return out
}

// Do runs fn with the agent identity (directly when no identity is set).
func (s *Service) Do(fn func() error) error {
	if s.as == nil {
		return fn()
	}
	return runAs(s.as, fn)
}

// with cleans p, opens the root, and runs fn with the agent identity.
func (s *Service) with(root agentfs.Root, p string, fn func(r *os.Root, rel string) error) error {
	rel, err := agentfs.CleanRel(p)
	if err != nil {
		return err
	}
	dir, err := s.Dir(root)
	if err != nil {
		return err
	}
	return s.Do(func() error {
		r, err := os.OpenRoot(dir)
		if err != nil {
			return fmt.Errorf("open root %s: %w", root, err)
		}
		defer r.Close()
		return agentfs.MapRootErr(fn(r, rel))
	})
}

// Lstat describes one path without following a final symlink.
func (s *Service) Lstat(root agentfs.Root, p string) (agentfs.Entry, error) {
	var e agentfs.Entry
	err := s.with(root, p, func(r *os.Root, rel string) error {
		fi, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		e = agentfs.EntryFrom(rel, fi)
		return nil
	})
	return e, err
}

// List returns a directory and its entries.
func (s *Service) List(root agentfs.Root, p string) (agentfs.Entry, []agentfs.Entry, error) {
	var (
		self    agentfs.Entry
		entries []agentfs.Entry
	)
	err := s.with(root, p, func(r *os.Root, rel string) error {
		var err error
		self, entries, err = agentfs.ListRoot(r, rel)
		return err
	})
	return self, entries, err
}

// Open opens a regular file for reading. Reads on the returned file need no
// identity switch: the kernel checked access when the file was opened.
func (s *Service) Open(root agentfs.Root, p string) (*os.File, agentfs.Entry, error) {
	var (
		f *os.File
		e agentfs.Entry
	)
	err := s.with(root, p, func(r *os.Root, rel string) error {
		var err error
		f, e, err = agentfs.OpenRegular(r, rel)
		return err
	})
	return f, e, err
}

// Write atomically writes src to p.
func (s *Service) Write(root agentfs.Root, p string, src io.Reader, o agentfs.WriteOptions) (agentfs.WriteResult, error) {
	var res agentfs.WriteResult
	err := s.with(root, p, func(r *os.Root, rel string) error {
		var err error
		res, err = agentfs.WriteRoot(r, rel, src, o, nil)
		return err
	})
	return res, err
}

// Mkdir creates a directory.
func (s *Service) Mkdir(root agentfs.Root, p string, parents bool) (agentfs.Entry, error) {
	var e agentfs.Entry
	err := s.with(root, p, func(r *os.Root, rel string) error {
		var err error
		e, err = agentfs.MkdirRoot(r, rel, parents, nil)
		return err
	})
	return e, err
}

// Rename moves from to to inside one root.
func (s *Service) Rename(root agentfs.Root, from, to string, overwrite bool) (agentfs.Entry, error) {
	toRel, err := agentfs.CleanRel(to)
	if err != nil {
		return agentfs.Entry{}, err
	}
	var e agentfs.Entry
	err = s.with(root, from, func(r *os.Root, fromRel string) error {
		var err error
		e, err = agentfs.RenameRoot(r, fromRel, toRel, overwrite)
		return err
	})
	return e, err
}

// Remove removes a path; with recursive a directory tree.
func (s *Service) Remove(root agentfs.Root, p string, recursive bool) error {
	return s.with(root, p, func(r *os.Root, rel string) error {
		return agentfs.RemoveRoot(r, rel, recursive)
	})
}

// ReadFile reads a whole file of at most limit bytes, following symlinks
// that stay inside the root.
func (s *Service) ReadFile(root agentfs.Root, p string, limit int64) ([]byte, error) {
	var data []byte
	err := s.with(root, p, func(r *os.Root, rel string) error {
		f, e, err := agentfs.OpenRegular(r, rel)
		if err != nil {
			return err
		}
		defer f.Close()
		if e.Size > limit {
			return fmt.Errorf("%w: %d bytes exceeds %d", agentfs.ErrTooLarge, e.Size, limit)
		}
		data, err = io.ReadAll(io.LimitReader(f, limit+1))
		if err == nil && int64(len(data)) > limit {
			err = agentfs.ErrTooLarge
		}
		return err
	})
	return data, err
}

// WriteFile atomically writes data, creating parents. An existing file
// keeps its mode; a final symlink that stays inside the root is written
// through.
func (s *Service) WriteFile(root agentfs.Root, p string, data []byte, perm fs.FileMode) error {
	dir, err := s.Dir(root)
	if err != nil {
		return err
	}
	return s.with(root, p, func(r *os.Root, rel string) error {
		target, err := followLink(r, dir, rel)
		if err != nil {
			return err
		}
		_, err = agentfs.WriteRoot(r, target, bytes.NewReader(data), agentfs.WriteOptions{
			Overwrite: true, MkdirParents: true, Limit: -1, Mode: perm, RegularOnly: true,
		}, nil)
		return err
	})
}

// MkdirAll creates a directory and its parents.
func (s *Service) MkdirAll(root agentfs.Root, p string) error {
	return s.with(root, p, func(r *os.Root, rel string) error {
		return agentfs.MkdirAllRoot(r, rel, nil)
	})
}

// followLink resolves a final symlink chain of rel to a path inside the
// root. A link that leaves the root is ErrEscapes; a missing path is
// returned unchanged.
func followLink(r *os.Root, dir, rel string) (string, error) {
	for hops := 0; hops < 16; hops++ {
		if rel == "." {
			return rel, nil
		}
		fi, err := r.Lstat(rel)
		if errors.Is(err, fs.ErrNotExist) {
			return rel, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return rel, nil
		}
		target, err := readlinkIn(r, rel)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			back, err := filepath.Rel(dir, filepath.Clean(target))
			if err != nil || !filepath.IsLocal(back) {
				return "", fmt.Errorf("%w: %s", agentfs.ErrEscapes, filepath.ToSlash(rel))
			}
			rel = back
			continue
		}
		next := filepath.Join(filepath.Dir(rel), target)
		if !filepath.IsLocal(next) {
			return "", fmt.Errorf("%w: %s", agentfs.ErrEscapes, filepath.ToSlash(rel))
		}
		rel = next
	}
	return "", fmt.Errorf("%w: too many levels of symbolic links", agentfs.ErrInvalidPath)
}
