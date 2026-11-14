// Package bootstrap installs the external data-plane binaries (juicefs,
// cloud-hypervisor, virtiofsd, magpie, and the optional cloudflared) into
// $DATA_DIR/runtime/bin on first run. Download sources and SHA256 checksums
// are pinned in manifest.json, which is the single file to edit when
// upgrading a binary.
package bootstrap

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

//go:embed manifest.json
var manifestJSON []byte

// Archive formats accepted in manifest.json.
const (
	ArchiveRaw   = "raw"
	ArchiveTarGz = "tar.gz"
	ArchiveZip   = "zip"
)

// ChecksumUnverified marks a manifest entry that has no pinned checksum.
const ChecksumUnverified = "UNVERIFIED"

// ChecksumBuild marks a manifest entry that is not downloaded but built
// from source on the host; Artifact.Build names the build command.
const ChecksumBuild = "build"

// Manifest is the parsed manifest.json.
type Manifest struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Updated         string            `json:"updated"`
	ChecksumSources map[string]string `json:"checksumSources"`
	Binaries        []Binary          `json:"binaries"`
}

// Binary is one pinned upstream binary.
type Binary struct {
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Release     string              `json:"release"`
	RequiredFor []string            `json:"requiredFor"`
	Notes       string              `json:"notes,omitempty"`
	Platforms   map[string]Artifact `json:"platforms"`
}

// Artifact is the download of one binary for one GOOS/GOARCH.
type Artifact struct {
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	ChecksumSource string `json:"checksumSource"`
	Archive        string `json:"archive"`
	// Member is the path (or base name) of the executable inside an archive.
	Member string `json:"member,omitempty"`
	// Build is the command that builds the binary locally when
	// ChecksumSource is "build" (no URL).
	Build string `json:"build,omitempty"`
}

// BuiltLocally reports whether the artifact is built on the host instead of
// downloaded.
func (a Artifact) BuiltLocally() bool { return a.ChecksumSource == ChecksumBuild }

// DefaultManifest parses the embedded manifest.json.
func DefaultManifest() (*Manifest, error) {
	return ParseManifest(manifestJSON)
}

// ParseManifest decodes and validates a manifest document.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("bootstrap: parse manifest: %w", err)
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("bootstrap: unsupported manifest schemaVersion %d", m.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, b := range m.Binaries {
		if b.Name == "" || b.Version == "" {
			return nil, fmt.Errorf("bootstrap: manifest entry without name or version")
		}
		if seen[b.Name] {
			return nil, fmt.Errorf("bootstrap: duplicate manifest entry %q", b.Name)
		}
		seen[b.Name] = true
		for plat, a := range b.Platforms {
			if a.SHA256 != "" {
				if raw, err := hex.DecodeString(a.SHA256); err != nil || len(raw) != 32 {
					return nil, fmt.Errorf("bootstrap: %s %s: sha256 must be 64 hex characters", b.Name, plat)
				}
			}
			if a.BuiltLocally() {
				if a.Build == "" || a.URL != "" || a.SHA256 != "" {
					return nil, fmt.Errorf("bootstrap: %s %s: a %q entry needs a build command and no url or sha256", b.Name, plat, ChecksumBuild)
				}
				continue
			}
			if a.URL == "" {
				continue
			}
			switch a.Archive {
			case ArchiveRaw:
			case ArchiveTarGz, ArchiveZip:
				if a.Member == "" {
					return nil, fmt.Errorf("bootstrap: %s %s: archive entries need a member", b.Name, plat)
				}
			default:
				return nil, fmt.Errorf("bootstrap: %s %s: unknown archive %q", b.Name, plat, a.Archive)
			}
		}
	}
	return &m, nil
}

// Lookup returns the entry of a binary.
func (m *Manifest) Lookup(name string) (Binary, bool) {
	for _, b := range m.Binaries {
		if b.Name == name {
			return b, true
		}
	}
	return Binary{}, false
}

// Required reports whether the binary is needed by the given VM driver.
func (b Binary) Required(driver string) bool {
	return slices.Contains(b.RequiredFor, driver)
}
