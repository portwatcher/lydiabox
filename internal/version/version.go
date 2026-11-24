// Package version exposes build metadata injected by the Makefile through
// -ldflags "-X github.com/portwatcher/lydiabox/internal/version.Version=...".
package version

import (
	"fmt"
	"runtime"
)

// These variables are overwritten at link time. The defaults identify a
// development build produced by a plain `go build`.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info is the structured form of the build metadata, used by the CLI and the
// status API.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the build metadata of the running binary.
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String formats the build metadata on one line.
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s, %s)", i.Version, i.Commit, i.BuildDate, i.GoVersion, i.Platform)
}
