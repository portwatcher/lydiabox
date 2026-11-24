package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/agentfs"
	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/bootstrap"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	"github.com/portwatcher/lydiabox/internal/store"
	"github.com/portwatcher/lydiabox/internal/vmm/chv"
	"github.com/portwatcher/lydiabox/internal/vmm/docker"
)

// cellResources are the host-wide names, addresses, and files of one cell.
type cellResources struct {
	tcp   map[string]string // name -> listen address
	udp   map[string]int
	names map[string]string // TAP, nftables table, subnet, container
	files map[string]string
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func loadCell(t *testing.T, env ...string) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{HomeDir: t.TempDir(), ConfigFile: os.DevNull,
		Environ: func() []string { return env }})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// wireCell runs the VM and harness wiring of one cell with the chv driver,
// a real state database, and fake storage, starts the VM component (it
// binds the guest port sockets), and lists the resources that the wiring
// uses.
func wireCell(t *testing.T, cfg *config.Config) cellResources {
	t.Helper()
	ctx := context.Background()
	if err := os.MkdirAll(cfg.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, cfg.DBPath(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	boot, err := bootstrap.New(bootstrap.Options{BinDir: cfg.BinDir(), Driver: config.DriverCHV})
	if err != nil {
		t.Fatal(err)
	}
	stor := dirStorage{home: filepath.Join(cfg.MountDir(), "home", "agent"), ws: filepath.Join(cfg.MountDir(), "workspace")}
	d := &deps{Config: cfg, Driver: config.DriverCHV, Logger: slog.New(slog.DiscardHandler), Store: st, Bootstrap: boot,
		Storage: stor, Files: agentfs.NewLocal(stor, agentfs.LocalOptions{}), Auth: api.NewAuthenticator("test-admin-token-0123456789abcdef"),
		VMStatus: api.StaticVMStatus{State: api.VMStopped, Driver: config.DriverCHV}}
	vm, err := wireVM(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wireHarness(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Start binds the guest port sockets of the cell under its run dir.
	if err := vm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vm.Stop(context.Background()) })

	tid := cfg.TenantID
	nw, err := guestNetwork(ctx, d, tid)
	if err != nil {
		t.Fatal(err)
	}
	up, err := url.Parse(gatewayUpstream(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if up.Host != cfg.Magpie.Addr {
		t.Fatalf("the guest LLM tunnel forwards to %s, magpie listens on %s", up.Host, cfg.Magpie.Addr)
	}
	paths := chv.PathsFor(cfg.RunDir(), tid)
	return cellResources{
		tcp: map[string]string{"api": cfg.ListenAddr, "s3 gateway": cfg.S3.GatewayAddr,
			"magpie": cfg.Magpie.Addr, "magpie web": cfg.Magpie.WebAddr},
		udp: map[string]int{"webrtc": webRTCPort(d)},
		names: map[string]string{"tap": nw.Tap, "nft table": chv.TableName(nw), "subnet": nw.Subnet(),
			"container": docker.ContainerName(tid), "tenant": tid},
		files: map[string]string{"data dir": cfg.DataDir, "state db": cfg.DBPath(), "magpie dir": cfg.MagpieDir(),
			"mount": cfg.MountDir(), "cache": cfg.CacheDir(), "jfs meta": cfg.JuiceFSMetaPath(),
			"chv api": paths.APISocket, "vsock": paths.Vsock, "gateway socket": chv.ListenerPath(paths.Vsock, rpc.PortHostGateway)},
	}
}

func within(a, b string) bool {
	rel, err := filepath.Rel(b, a)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// collisions lists every resource that two cells share.
func collisions(a, b cellResources) []string {
	var out []string
	for na, va := range a.tcp {
		for nb, vb := range b.tcp {
			if config.AddrsOverlap(va, vb) {
				out = append(out, fmt.Sprintf("tcp %s=%s / %s=%s", na, va, nb, vb))
			}
		}
	}
	for na, va := range a.udp {
		for nb, vb := range b.udp {
			if va != 0 && va == vb {
				out = append(out, fmt.Sprintf("udp %s / %s = %d", na, nb, va))
			}
		}
	}
	for n, va := range a.names {
		if va == b.names[n] {
			out = append(out, fmt.Sprintf("%s %s", n, va))
		}
	}
	for na, va := range a.files {
		for nb, vb := range b.files {
			if within(va, vb) || within(vb, va) {
				out = append(out, fmt.Sprintf("file %s=%s / %s=%s", na, va, nb, vb))
			}
		}
	}
	return out
}

func cellEnv(t *testing.T, tenant, dataDir string, extra ...string) []string {
	return append([]string{"LYDIABOX_TENANT_ID=" + tenant, "LYDIABOX_DATA_DIR=" + dataDir,
		"LYDIABOX_LISTEN_ADDR=" + freeAddr(t), "LYDIABOX_S3_GATEWAY_ADDR=" + freeAddr(t)}, extra...)
}

func shortDir(t *testing.T) string {
	t.Helper()
	// UNIX socket paths are short on macOS; t.TempDir is too long.
	dir, err := os.MkdirTemp("", "lbxc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestTwoCellsOnOneHostShareNothing(t *testing.T) {
	root := shortDir(t)
	a := loadCell(t, cellEnv(t, "alice", filepath.Join(root, "a"),
		"LYDIABOX_MAGPIE_ADDR="+freeAddr(t), "LYDIABOX_MAGPIE_WEB_ADDR="+freeAddr(t),
		"LYDIABOX_VM_SUBNET_INDEX=11", "LYDIABOX_WEBRTC_UDP_PORT=50011")...)
	b := loadCell(t, cellEnv(t, "bob", filepath.Join(root, "b"),
		"LYDIABOX_MAGPIE_ADDR="+freeAddr(t), "LYDIABOX_MAGPIE_WEB_ADDR="+freeAddr(t),
		"LYDIABOX_VM_SUBNET_INDEX=12", "LYDIABOX_WEBRTC_UDP_PORT=50012",
		"LYDIABOX_VM_TAP=lbxcellbob", "LYDIABOX_VM_NFT_TABLE=cell_bob")...)
	ra, rb := wireCell(t, a), wireCell(t, b)
	if c := collisions(ra, rb); len(c) > 0 {
		t.Fatalf("the cells collide:\n%s", strings.Join(c, "\n"))
	}
	if rb.names["tap"] != "lbxcellbob" || rb.names["nft table"] != "cell_bob" || rb.names["subnet"] != "172.30.12.0/24" {
		t.Fatalf("network overrides not used: %+v", rb.names)
	}
	// Both cells can hold all their TCP addresses at the same time.
	for _, r := range []cellResources{ra, rb} {
		for name, addr := range r.tcp {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("bind %s %s: %v", name, addr, err)
			}
			defer l.Close()
		}
	}
}

func TestTwoCellsWithDefaultsCollide(t *testing.T) {
	// The check above is meaningful only if it finds the collisions of two
	// cells that keep the single-user defaults.
	root := shortDir(t)
	ra := wireCell(t, loadCell(t, cellEnv(t, "alice", filepath.Join(root, "a"))...))
	rb := wireCell(t, loadCell(t, cellEnv(t, "bob", filepath.Join(root, "b"))...))
	got := strings.Join(collisions(ra, rb), "\n")
	for _, want := range []string{"tcp magpie=", "udp webrtc", "subnet 172.30.1.0/24"} {
		if !strings.Contains(got, want) {
			t.Errorf("collision %q not found in:\n%s", want, got)
		}
	}
}

func TestCellSettingsAreValidated(t *testing.T) {
	for _, env := range [][]string{
		{"LYDIABOX_TENANT_ID=Bad_Tenant"},
		{"LYDIABOX_VM_SUBNET_INDEX=255"},
		{"LYDIABOX_VM_TAP=a-name-that-is-too-long"},
		{"LYDIABOX_VM_NFT_TABLE=1table"},
		{"LYDIABOX_MAGPIE_ADDR=127.0.0.1:0"},
		{"LYDIABOX_MAGPIE_ADDR=127.0.0.1:8080", "LYDIABOX_LISTEN_ADDR=0.0.0.0:8080"},
	} {
		cfg, err := config.Load(config.LoadOptions{HomeDir: t.TempDir(), ConfigFile: os.DevNull, Environ: func() []string { return env }})
		if err == nil {
			err = cfg.Validate()
		}
		if err == nil {
			t.Errorf("%v accepted", env)
		}
	}
}
