package rpc

import (
	"encoding/json"
	"time"
)

// Guest file and session methods of the provider storage mode. The host
// reaches the persistent trees through them when it cannot mount the guest
// disk (docs/api.md section 6). Paths are relative to a named root ("home"
// or "workspace"); the guest resolves every path inside its configured root
// directory with an os.Root sandbox and runs file operations as the agent
// user. FS errors carry the agentfs error codes ("fs_not_exist", ...).
const (
	// MethodFSRoots (unary) returns FSRootsResult.
	MethodFSRoots = "fs.roots"
	// MethodFSStat (unary): FSPathParams -> FSEntry (lstat).
	MethodFSStat = "fs.stat"
	// MethodFSList (unary): FSPathParams -> FSListResult.
	MethodFSList = "fs.list"
	// MethodFSRead (streaming): FSReadParams -> FSEntry, then FrameData
	// chunks of the file from Offset, then one FSStreamEnd JSON frame.
	MethodFSRead = "fs.read"
	// MethodFSWrite (streaming): FSWriteParams -> {} when the write can
	// start; the client sends FrameData chunks and one FSWriteCommit JSON
	// frame; the guest answers with a Response frame holding FSWriteResult
	// or the error. Closing the stream before the commit aborts the write
	// and removes the temporary file.
	MethodFSWrite = "fs.write"
	// MethodFSMkdir (unary): FSMkdirParams -> FSEntry.
	MethodFSMkdir = "fs.mkdir"
	// MethodFSRename (unary): FSRenameParams -> FSEntry.
	MethodFSRename = "fs.rename"
	// MethodFSRemove (unary): FSRemoveParams -> {}.
	MethodFSRemove = "fs.remove"
	// MethodFSReadFile (unary): FSPathParams -> FSFileData. It follows
	// symlinks that stay inside the root and is bounded by MaxFileData.
	MethodFSReadFile = "fs.readfile"
	// MethodFSWriteFile (unary): FSWriteFileParams -> {}. It writes
	// atomically, creates parents, keeps the mode of an existing file, and
	// writes through a final symlink that stays inside the root.
	MethodFSWriteFile = "fs.writefile"
	// MethodFSMkdirAll (unary): FSPathParams -> {}.
	MethodFSMkdirAll = "fs.mkdirall"

	// MethodHarnessDiscover (unary): HarnessDiscoverParams ->
	// HarnessDiscoverResult. The guest runs the harness adapters on its
	// home root, so transcripts are not copied to the host.
	MethodHarnessDiscover = "harness.discover"
	// MethodHarnessPurge (unary): HarnessPurgeParams -> {}.
	MethodHarnessPurge = "harness.purge"
)

// Guest daemon environment of the file methods.
const (
	// EnvGuestHomeRoot is the guest directory of the "home" root
	// (default /home/agent; InstaBox uses /data/home).
	EnvGuestHomeRoot = "LYDIABOX_GUEST_HOME_ROOT"
	// EnvGuestWorkspaceRoot is the guest directory of the "workspace" root
	// (default /workspace).
	EnvGuestWorkspaceRoot = "LYDIABOX_GUEST_WORKSPACE_ROOT"
	// EnvGuestFSUser is the user that file operations run as when the
	// daemon runs as root (default "agent", uid 1000).
	EnvGuestFSUser = "LYDIABOX_GUEST_FS_USER"
)

// MaxFileData bounds the content of fs.readfile and fs.writefile (the
// base64 form must fit in one frame).
const MaxFileData = 2 << 20

// FSEntry describes one file or directory (agentfs.Entry).
type FSEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
}

// FSRoot names a root and its guest directory.
type FSRoot struct {
	Name      string `json:"name"`
	GuestPath string `json:"guestPath"`
}

// FSRootsResult answers MethodFSRoots.
type FSRootsResult struct {
	Roots []FSRoot `json:"roots"`
}

// FSPathParams names a path inside a root.
type FSPathParams struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

// FSListResult answers MethodFSList.
type FSListResult struct {
	Dir     FSEntry   `json:"dir"`
	Entries []FSEntry `json:"entries"`
}

// FSReadParams opens a regular file for reading from Offset.
type FSReadParams struct {
	Root   string `json:"root"`
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
}

// FSStreamEnd ends an fs.read stream; Error is set when the read failed.
type FSStreamEnd struct {
	Error *Error `json:"error,omitempty"`
}

// FSWriteParams starts an atomic write (agentfs.WriteOptions).
type FSWriteParams struct {
	Root         string     `json:"root"`
	Path         string     `json:"path"`
	Overwrite    bool       `json:"overwrite,omitempty"`
	MkdirParents bool       `json:"mkdirParents,omitempty"`
	IfModTime    *time.Time `json:"ifModTime,omitempty"`
	Limit        int64      `json:"limit"`
	Mode         uint32     `json:"mode,omitempty"`
	RegularOnly  bool       `json:"regularOnly,omitempty"`
}

// FSWriteCommit is the JSON frame that ends the content of an fs.write.
type FSWriteCommit struct {
	Size int64 `json:"size"`
}

// FSWriteResult answers a committed fs.write.
type FSWriteResult struct {
	FSEntry
	Replaced bool `json:"replaced"`
}

// FSMkdirParams creates a directory.
type FSMkdirParams struct {
	Root    string `json:"root"`
	Path    string `json:"path"`
	Parents bool   `json:"parents,omitempty"`
}

// FSRenameParams moves a path inside one root.
type FSRenameParams struct {
	Root      string `json:"root"`
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// FSRemoveParams removes a path.
type FSRemoveParams struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

// FSFileData is a whole small file.
type FSFileData struct {
	Data []byte `json:"data"`
}

// FSWriteFileParams writes a whole small file.
type FSWriteFileParams struct {
	Root string `json:"root"`
	Path string `json:"path"`
	Data []byte `json:"data"`
	Perm uint32 `json:"perm"`
}

// HarnessDiscoverParams selects the harnesses to discover (all when empty).
type HarnessDiscoverParams struct {
	Harnesses []string `json:"harnesses,omitempty"`
}

// HarnessDiscoverResult maps a harness to its sessions (a JSON array of
// harness.SessionMeta) or to the error of its adapter.
type HarnessDiscoverResult struct {
	Sessions map[string]json.RawMessage `json:"sessions"`
	Errors   map[string]string          `json:"errors,omitempty"`
}

// HarnessPurgeParams deletes the native transcript of one session.
type HarnessPurgeParams struct {
	Harness string `json:"harness"`
	ID      string `json:"id"`
}
