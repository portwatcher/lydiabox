// Package instaboximage holds the system bundle of the LydiaBox InstaBox
// image (docs/instabox.md, "System upgrade"): the image's own small files
// and the root script system-upgrade.sh. The instabox driver
// (internal/vmm/instabox) installs the bundle on a machine whose system
// files are older than the files of this build. InstaBox has no
// operation that replaces the root disk and keeps /data, so a machine
// keeps the system files of its snapshot without this.
package instaboximage

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"
)

// File is one file that deploy/instabox/Dockerfile copies into the image.
// Files is the single source of the destination paths: a test compares
// it with the COPY lines of the Dockerfile.
type File struct {
	// Src is the name of the file in deploy/instabox.
	Src string
	// Dest is the absolute path in the image.
	Dest string
	// Mode is the file mode in the image.
	Mode fs.FileMode
}

// Files are the image files that the bundle installs.
var Files = []File{
	{Src: "codex-config.toml", Dest: "/usr/local/share/lydiabox/etc/codex-config.toml", Mode: 0o644},
	{Src: "claude-managed-settings.json", Dest: "/usr/local/share/lydiabox/etc/claude-managed-settings.json", Mode: 0o644},
	{Src: "xfce4-panel.xml", Dest: "/usr/local/share/lydiabox/etc/xfce4-panel.xml", Mode: 0o644},
	{Src: "supervisord.conf", Dest: "/etc/lydiabox/instabox-supervisord.conf", Mode: 0o644},
	{Src: "guest.env.example", Dest: "/etc/lydiabox/guest.env.example", Mode: 0o644},
	{Src: "lydiabox-instabox-entry", Dest: "/usr/local/sbin/lydiabox-instabox-entry", Mode: 0o755},
	{Src: "lydiabox-instabox-guest", Dest: "/usr/local/bin/lydiabox-instabox-guest", Mode: 0o755},
	{Src: "lydiabox-instabox-xfce", Dest: "/usr/local/bin/lydiabox-instabox-xfce", Mode: 0o755},
}

// ScriptName is the root script in the bundle. Run it as
// `sh system-upgrade.sh BUNDLE_DIR`.
const ScriptName = "system-upgrade.sh"

// MarkerPath is the file on the machine's root disk that holds the
// SHA-256 of the last bundle that was installed with success.
const MarkerPath = "/etc/lydiabox/system.sha256"

//go:embed system-upgrade.sh codex-config.toml claude-managed-settings.json xfce4-panel.xml supervisord.conf guest.env.example lydiabox-instabox-entry lydiabox-instabox-guest lydiabox-instabox-xfce
var embedded embed.FS

// Bundle is the system bundle: a tar archive and its SHA-256.
type Bundle struct {
	Tar    []byte
	SHA256 string
}

// System returns the bundle of this build. It is made one time.
var System = sync.OnceValues(func() (Bundle, error) { return Build(embedded, Files) })

// tarTime is the modification time of every entry, so that the same
// files always give the same archive.
var tarTime = time.Unix(0, 0).UTC()

// Build makes the bundle from the files in fsys: ScriptName (mode 0755),
// files.list ("MODE DEST NAME" for each file), and files/NAME for each
// file. The archive is deterministic: sorted names, a fixed time, owner
// 0:0, and the USTAR format.
func Build(fsys fs.FS, files []File) (Bundle, error) {
	type entry struct {
		data []byte
		mode fs.FileMode
	}
	entries := map[string]entry{}
	script, err := fs.ReadFile(fsys, ScriptName)
	if err != nil {
		return Bundle{}, err
	}
	entries[ScriptName] = entry{script, 0o755}
	var list bytes.Buffer
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f.Src)
		if err != nil {
			return Bundle{}, err
		}
		fmt.Fprintf(&list, "%04o %s %s\n", f.Mode.Perm(), f.Dest, f.Src)
		entries["files/"+f.Src] = entry{data, 0o644}
	}
	entries["files.list"] = entry{list.Bytes(), 0o644}

	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range names {
		e := entries[n]
		hdr := &tar.Header{
			Typeflag: tar.TypeReg, Name: n, Size: int64(len(e.data)), Mode: int64(e.mode),
			ModTime: tarTime, Uid: 0, Gid: 0, Format: tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return Bundle{}, err
		}
		if _, err := tw.Write(e.data); err != nil {
			return Bundle{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Bundle{}, err
	}
	sum := sha256.Sum256(buf.Bytes())
	return Bundle{Tar: buf.Bytes(), SHA256: hex.EncodeToString(sum[:])}, nil
}
