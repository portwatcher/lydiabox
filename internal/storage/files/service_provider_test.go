//go:build unix

package files

import (
	"testing"

	"github.com/lydiabox/lydiabox/internal/agentfs/agentfstest"
)

func init() {
	sandboxes["provider"] = func(t *testing.T, d testDirs) *Service {
		g := agentfstest.Start(t, d.home, d.workspace)
		return NewService(g.FS, Options{MaxTextBytes: 1024})
	}
}
