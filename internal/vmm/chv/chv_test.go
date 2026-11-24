package chv

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/vmm"
)

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "lbxc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func testSpec(dir string) vmm.Spec {
	return vmm.Spec{
		CPUs: 4, MemMB: 8192, GPUPCIAddr: "0000:01:00.0",
		Kernel: "/img/vmlinux", Initramfs: "/img/initramfs.cpio.gz", Rootfs: "/img/rootfs.squashfs",
		GuestBinary: filepath.Join(dir, "lydiabox-guest"), RootUpper: "/jfs/system/root-upper.img",
		HomeDir: "/jfs/home/agent", WorkspaceDir: "/jfs/workspace", WebRTCPort: 50000,
	}
}

func TestBuildConfigPayload(t *testing.T) {
	p := PathsFor("/run", "default")
	cfg, err := BuildConfig(testSpec("/bin"), p, DefaultNetwork("default"), "default")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(cfg)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	want := map[string]any{
		"cpus":    map[string]any{"boot_vcpus": 4.0, "max_vcpus": 4.0},
		"memory":  map[string]any{"size": float64(8192 << 20), "shared": true},
		"payload": map[string]any{"kernel": "/img/vmlinux", "initramfs": "/img/initramfs.cpio.gz", "cmdline": BaseCmdline()},
		"disks": []any{
			map[string]any{"path": "/img/rootfs.squashfs", "readonly": true, "image_type": "Raw"},
			map[string]any{"path": "/run/vm/default/lydiabox-guest.ext4", "readonly": true, "image_type": "Raw"},
			map[string]any{"path": "/jfs/system/root-upper.img", "readonly": false, "image_type": "Raw"},
		},
		"fs": []any{
			map[string]any{"tag": "lydia_home", "socket": "/run/vm/default/vfs-home.sock", "num_queues": 1.0, "queue_size": 1024.0},
			map[string]any{"tag": "lydia_workspace", "socket": "/run/vm/default/vfs-workspace.sock", "num_queues": 1.0, "queue_size": 1024.0},
		},
		"vsock":   map[string]any{"cid": 3.0, "socket": "/run/vm/default/vsock.sock"},
		"net":     []any{map[string]any{"tap": TapName("default"), "ip": "172.30.0.1", "mask": "255.255.255.0", "mac": MAC("default")}},
		"devices": []any{map[string]any{"path": "/sys/bus/pci/devices/0000:01:00.0"}},
		"serial":  map[string]any{"mode": "File", "file": "/run/vm/default/serial.log"},
		"console": map[string]any{"mode": "Off"},
		"rng":     map[string]any{"src": "/dev/urandom"},
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s:\n got %#v\nwant %#v", k, got[k], v)
		}
	}
	if len(TapName("default")) > 15 || !strings.HasPrefix(MAC("x"), "52:54:00:") {
		t.Fatal("tap name or MAC format")
	}
	spec := testSpec("/bin")
	spec.GPUPCIAddr = ""
	cfg, _ = BuildConfig(spec, p, Network{Disabled: true}, "default")
	b, _ = json.Marshal(cfg)
	if strings.Contains(string(b), `"devices"`) || strings.Contains(string(b), `"net"`) {
		t.Fatalf("optional sections present: %s", b)
	}
	spec.Kernel = ""
	if _, err := BuildConfig(spec, p, Network{}, "default"); err == nil {
		t.Fatal("missing kernel accepted")
	}
}

func TestNetworkRules(t *testing.T) {
	nw := DefaultNetwork("default")
	script := NftScript(nw, 50000)
	for _, w := range []string{
		"ip saddr 172.30.0.0/24 oifname != \"" + nw.Tap + "\" masquerade",
		"udp dport 50000 dnat to 172.30.0.2",
		"ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 100.64.0.0/10, 127.0.0.0/8 } drop",
		"iifname \"" + nw.Tap + "\" drop",
	} {
		if !strings.Contains(script, w) {
			t.Fatalf("missing %q in\n%s", w, script)
		}
	}
	add := IptablesRules(nw, 50000, true)
	del := IptablesRules(nw, 50000, false)
	if len(add) != len(del) || add[0][2] != "-A" || del[0][2] != "-D" {
		t.Fatalf("iptables rules %v %v", add[0], del[0])
	}
}

func TestTenantNetwork(t *testing.T) {
	def := DefaultNetwork("default")
	if def != TenantNetwork("default", 0) || def.Subnet() != "172.30.0.0/24" || def.cmdline() != BaseCmdline() {
		t.Fatalf("default network %+v %q", def, def.cmdline())
	}
	alice := TenantNetwork("alice", 7)
	if alice.Subnet() != "172.30.7.0/24" || alice.HostIP != "172.30.7.1" || alice.GuestIP != "172.30.7.2" || alice.Tap == def.Tap {
		t.Fatalf("alice network %+v", alice)
	}
	p := PathsFor("/run", "alice")
	spec := testSpec("/bin")
	cfg, err := BuildConfig(spec, p, alice, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if want := BaseCmdline() + " lydiabox.ip=172.30.7.2/24 lydiabox.gw=172.30.7.1"; cfg.Payload.Cmdline != want {
		t.Fatalf("cmdline %q, want %q", cfg.Payload.Cmdline, want)
	}
	if cfg.Net[0].IP != "172.30.7.1" || cfg.Net[0].Mask != "255.255.255.0" {
		t.Fatalf("net %+v", cfg.Net)
	}
	script := NftScript(alice, 50001)
	for _, w := range []string{"ip saddr 172.30.7.0/24 oifname != \"" + alice.Tap + "\" masquerade", "dnat to 172.30.7.2"} {
		if !strings.Contains(script, w) {
			t.Fatalf("missing %q in\n%s", w, script)
		}
	}
}

func TestLifecycleAgainstMockCloudHypervisor(t *testing.T) {
	dir := shortDir(t)
	if err := os.WriteFile(filepath.Join(dir, "lydiabox-guest"), []byte("\x7fELF guest"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	d, err := New(Options{
		RunDir: dir, CloudHypervisor: "/opt/bin/cloud-hypervisor", Virtiofsd: "/opt/bin/virtiofsd", Runner: runner,
		LookPath: func(n string) (string, error) { return "/usr/sbin/" + n, nil }, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if st, err := d.State(ctx, "default"); err != nil || st != vmm.StateStopped {
		t.Fatalf("initial state %s %v", st, err)
	}
	if err := d.Create(ctx, "default", testSpec(dir)); err != nil {
		t.Fatal(err)
	}
	p := PathsFor(dir, "default")
	var vfsArgs []string
	for _, s := range runner.starts {
		if filepath.Base(s.Path) == "virtiofsd" {
			vfsArgs = append(vfsArgs, strings.Join(s.Args, " "))
		}
	}
	wantVFS := []string{
		"--socket-path=" + p.VFSHome + " --shared-dir=/jfs/home/agent --cache=auto",
		"--socket-path=" + p.VFSWorkspace + " --shared-dir=/jfs/workspace --cache=auto",
	}
	if !reflect.DeepEqual(vfsArgs, wantVFS) {
		t.Fatalf("virtiofsd args %v", vfsArgs)
	}
	if got := strings.Join(runner.starts[2].Args, " "); got != "--api-socket path="+p.APISocket {
		t.Fatalf("cloud-hypervisor args %q", got)
	}
	runs := runner.Runs()
	for _, w := range []string{"mkfs.ext4 -F -q -L lydiaguest -d ", "sysctl -q -w net.ipv4.ip_forward=1", "nft -f -"} {
		if !strings.Contains(runs, w) {
			t.Fatalf("missing %q in runs:\n%s", w, runs)
		}
	}
	var created VMConfig
	if err := json.Unmarshal(runner.chv.created, &created); err != nil || !created.Memory.Shared || created.Vsock.CID != 3 {
		t.Fatalf("vm.create body %s %v", runner.chv.created, err)
	}

	for _, step := range []struct {
		fn   func(context.Context, string) error
		want vmm.State
	}{{d.Boot, vmm.StateRunning}, {d.Pause, vmm.StatePaused}, {d.Resume, vmm.StateRunning}} {
		if err := step.fn(ctx, "default"); err != nil {
			t.Fatal(err)
		}
		if st, _ := d.State(ctx, "default"); st != step.want {
			t.Fatalf("state %s, want %s", st, step.want)
		}
	}
	mock := runner.chv
	if err := d.Shutdown(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	if got := mock.Requests(); got != "PUT vm.create,PUT vm.boot,PUT vm.pause,PUT vm.resume,PUT vm.shutdown,PUT vmm.shutdown" {
		t.Fatalf("requests %s", got)
	}
	if st, _ := d.State(ctx, "default"); st != vmm.StateStopped {
		t.Fatalf("state after shutdown %s", st)
	}
	if !strings.Contains(runner.Runs(), "nft delete table ip lydiabox_"+TapName("default")) {
		t.Fatalf("network not torn down:\n%s", runner.Runs())
	}
	if _, err := os.Stat(p.APISocket); !os.IsNotExist(err) {
		t.Fatal("API socket left behind")
	}
	// The guest image is reused while the binary is unchanged.
	n := strings.Count(runner.Runs(), "mkfs.ext4")
	if err := EnsureGuestImage(ctx, runner, "mkfs.ext4", filepath.Join(dir, "lydiabox-guest"), p.GuestImage); err != nil {
		t.Fatal(err)
	}
	if strings.Count(runner.Runs(), "mkfs.ext4") != n {
		t.Fatal("guest image rebuilt for an unchanged binary")
	}
	if err := d.Boot(ctx, "../evil"); err == nil {
		t.Fatal("invalid tenant accepted")
	}
}

func TestVsockHandshake(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "vsock.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				line, _ := r.ReadString('\n')
				switch line {
				case "CONNECT 9000\n":
					_, _ = io.WriteString(c, "OK 1073741824\nhello-from-guest")
					b := make([]byte, 5)
					_, _ = io.ReadFull(r, b)
					_, _ = c.Write(b)
				default:
					_, _ = io.WriteString(c, "ERR no listener\n")
				}
			}()
		}
	}()
	conn, err := DialVsock(context.Background(), sock, 9000)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, len("hello-from-guest"))
	if _, err := io.ReadFull(conn, b); err != nil || string(b) != "hello-from-guest" {
		t.Fatalf("data after handshake %q %v", b, err)
	}
	_, _ = conn.Write([]byte("abcde"))
	echo := make([]byte, 5)
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "abcde" {
		t.Fatalf("echo %q %v", echo, err)
	}
	conn.Close()
	if _, err := DialVsock(context.Background(), sock, 9001); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refused handshake: %v", err)
	}

	// Guest-to-host: the driver listens on <vsock.sock>_<port>.
	d, _ := New(Options{RunDir: dir, CloudHypervisor: "x", Virtiofsd: "y", Runner: &fakeRunner{}})
	hl, err := d.ListenGuestPort(context.Background(), "default", 9001)
	if err != nil {
		t.Fatal(err)
	}
	defer hl.Close()
	if want := filepath.Join(dir, "vm", "default", "vsock.sock_9001"); hl.Addr().String() != want {
		t.Fatalf("listener %s, want %s", hl.Addr(), want)
	}
}
