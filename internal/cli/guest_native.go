package cli

import (
	"context"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/agentfs/remotefs"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/magpie"
	"github.com/lydiabox/lydiabox/internal/magpie/wire"
)

// Guest-native mode (docs/design.md section 4.4.2): the harnesses sign in
// inside the VM and send model traffic directly to the provider. The host
// runs no magpie, routes no model traffic, and does not read or write the
// credential files of the harnesses.

// gatewayOffReason is the answer of the guest LLM tunnel in guest-native
// mode.
const gatewayOffReason = "the model gateway is off in guest-native mode; sign in to the harness in the VM"

// credentialPaths are the harness credential files that guest-native mode
// keeps from the host (wire.CredentialFiles).
func credentialPaths() agentfs.CredentialPaths {
	return agentfs.CredentialPaths{
		Files: wire.CredentialFiles,
		Names: []string{".credentials.json", "oauth_creds.json"},
		Dirs:  wire.CredentialDirs,
		Allow: wire.CredentialAllow,
	}
}

// guestFiles returns fsys with the credential files guarded in
// guest-native mode, else fsys.
func guestFiles(cfg *config.Config, fsys agentfs.FS) agentfs.FS {
	if fsys == nil || cfg == nil || !cfg.GuestNative() {
		return fsys
	}
	return agentfs.Guard(fsys, credentialPaths())
}

// remoteFS returns the remotefs.FS below a guard, or nil.
func remoteFS(fsys agentfs.FS) *remotefs.FS {
	for {
		switch f := fsys.(type) {
		case *remotefs.FS:
			return f
		case interface{ Unwrap() agentfs.FS }:
			fsys = f.Unwrap()
		default:
			return nil
		}
	}
}

// nativeUsed returns the models of the sessions, one per harness and
// model, for the native model list. It does not wake the VM.
func nativeUsed(mgr *harness.Manager, tenantID string) func(ctx context.Context) []magpie.NativeModel {
	return func(ctx context.Context) []magpie.NativeModel {
		ss, err := mgr.List(ctx, tenantID, harness.Filter{})
		if err != nil {
			return nil
		}
		out := make([]magpie.NativeModel, 0, len(ss))
		for _, s := range ss {
			if s.Harness != harness.Shell && s.Model != "" {
				out = append(out, magpie.NativeModel{Harness: s.Harness, Model: s.Model})
			}
		}
		return out
	}
}
