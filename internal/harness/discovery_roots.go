package harness

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// guestRootPrefix maps a directory of a guest root to the path form that
// LydiaBox uses for it (/workspace or /home/agent).
type guestRootPrefix struct{ dir, form string }

// guestRoots returns the directories of the guest roots that differ from
// their LydiaBox form, longest first. On InstaBox, /workspace links to
// /data/workspace and /home/agent to /data/lydiabox/home; a harness that
// records its working directory (Claude Code writes process.cwd()) records
// the target, which the New session dialog and CleanGuestDir refuse. The
// answer is kept for the life of the process; a failed call is tried again
// at the next discovery.
func (g *GuestDiscovery) guestRoots(ctx context.Context, tenantID string) []guestRootPrefix {
	if known, ok := g.knownRoots(tenantID); ok {
		return known
	}
	var res rpc.FSRootsResult
	if err := g.Guest.FSCall(ctx, tenantID, rpc.MethodFSRoots, nil, &res); err != nil {
		g.log().Debug("read the guest roots", "err", err)
		return nil
	}
	forms := map[string]string{string(agentfs.RootWorkspace): GuestWorkspace, string(agentfs.RootHome): GuestHome}
	var out []guestRootPrefix
	for _, r := range res.Roots {
		form, dir := forms[r.Name], path.Clean(r.GuestPath)
		if form == "" || !path.IsAbs(dir) || dir == form || dir == "/" {
			continue
		}
		out = append(out, guestRootPrefix{dir: dir, form: form})
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].dir) > len(out[j].dir) })
	g.rootsMu.Lock()
	if g.roots == nil {
		g.roots = map[string][]guestRootPrefix{}
	}
	g.roots[tenantID] = out
	g.rootsMu.Unlock()
	return out
}

// knownRoots returns the roots that guestRoots read before, without a call.
func (g *GuestDiscovery) knownRoots(tenantID string) ([]guestRootPrefix, bool) {
	g.rootsMu.Lock()
	defer g.rootsMu.Unlock()
	known, ok := g.roots[tenantID]
	return known, ok
}

// canonicalDirs writes the working directories of metas in the LydiaBox
// form. It reports whether it changed one.
func canonicalDirs(metas []SessionMeta, roots []guestRootPrefix) bool {
	changed := false
	for i := range metas {
		if d := canonicalDir(metas[i].WorkspaceDir, roots); d != metas[i].WorkspaceDir {
			metas[i].WorkspaceDir, changed = d, true
		}
	}
	return changed
}

func canonicalDir(dir string, roots []guestRootPrefix) string {
	if dir == "" {
		return dir
	}
	clean := path.Clean(dir)
	for _, r := range roots {
		if clean == r.dir {
			return r.form
		}
		if rest, ok := strings.CutPrefix(clean, r.dir+"/"); ok {
			return r.form + "/" + rest
		}
	}
	return dir
}
