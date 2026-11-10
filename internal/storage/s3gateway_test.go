package storage

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestGatewayCommand(t *testing.T) {
	j := JuiceFS{Binary: "/rt/juicefs", MetaURL: "sqlite3:///d/meta/jfs.db"}
	cmd := j.Gateway("127.0.0.1:8090", "/d/cache/jfs-gateway", "/d/logs/gw.log", "lydiabox", "lydiabox", "token-0123456789")
	want := []string{"gateway", "sqlite3:///d/meta/jfs.db", "127.0.0.1:8090",
		"--cache-dir=/d/cache/jfs-gateway", "--cache-size=1024", "--bucket-name=lydiabox", "--log=/d/logs/gw.log",
		"--no-banner", "--no-bgjob"}
	if cmd.Path != "/rt/juicefs" || !slices.Equal(cmd.Args, want) {
		t.Fatalf("args = %q", cmd.Args)
	}
	if !slices.Equal(cmd.Env, []string{"MINIO_ROOT_USER=lydiabox", "MINIO_ROOT_PASSWORD=token-0123456789"}) {
		t.Fatalf("env = %q", cmd.Env)
	}
	// The secret key must not appear on the command line (visible in ps).
	for _, a := range cmd.Args {
		if a == "token-0123456789" {
			t.Fatal("secret key on the command line")
		}
	}
}

func TestDialAddr(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8090":   "127.0.0.1:8090",
		":8090":          "127.0.0.1:8090",
		"[::]:8090":      "[::1]:8090",
		"127.0.0.1:8090": "127.0.0.1:8090",
		"box.local:8090": "box.local:8090",
	}
	for in, want := range cases {
		if got := dialAddr(in); got != want {
			t.Errorf("dialAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestS3GatewayDisabled(t *testing.T) {
	cfg := testConfig(t)
	cfg.S3.GatewayAddr = ""
	r := newFakeRunner(cfg)
	g, err := NewS3Gateway(S3GatewayOptions{Config: cfg, JuiceFSBinary: "/rt/juicefs", Runner: r, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Start(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if st := g.Status("default"); st != (S3GatewayStatus{}) || g.Enabled() {
		t.Fatalf("disabled status = %+v", st)
	}
	if len(r.starts) != 0 {
		t.Fatal("a disabled gateway must not start a process")
	}
	if err := g.Stop(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
}

func TestS3GatewaySupervisor(t *testing.T) {
	cfg := testConfig(t)
	cfg.S3.GatewayAddr = "0.0.0.0:8090"
	cfg.AuthToken = "admin-token-0123456789"
	r := newFakeRunner(cfg)
	var dialed []string
	g, err := NewS3Gateway(S3GatewayOptions{Config: cfg, JuiceFSBinary: "/rt/juicefs", Runner: r, Logger: discardLogger(),
		Dial: func(ctx context.Context, addr string) error { dialed = append(dialed, addr); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if st := g.Status("default"); !st.Enabled || st.Running || st.Addr != "0.0.0.0:8090" || st.Bucket != S3GatewayBucket {
		t.Fatalf("status before start = %+v", st)
	}
	if err := g.Start(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if st := g.Status("default"); !st.Running {
		t.Fatalf("status after start = %+v", st)
	}
	if len(dialed) == 0 || dialed[0] != "127.0.0.1:8090" {
		t.Fatalf("readiness dial = %v", dialed)
	}
	r.mu.Lock()
	cmd := r.starts[0]
	first := r.procs[0]
	r.mu.Unlock()
	if cmd.Args[0] != "gateway" || cmd.Args[2] != "0.0.0.0:8090" || !slices.Contains(cmd.Args, "--bucket-name=s3") ||
		!slices.Contains(cmd.Env, "MINIO_ROOT_PASSWORD=admin-token-0123456789") ||
		!slices.Contains(cmd.Args, "--log="+filepath.Join(cfg.DataDir, "logs", "juicefs-gateway.log")) {
		t.Fatalf("command = %+v", cmd)
	}

	// A crashed gateway is restarted on the same address.
	first.finish(nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.starts)
		r.mu.Unlock()
		if n == 2 && g.Status("default").Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("gateway was not restarted")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := g.Stop(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if st := g.Status("default"); st.Running {
		t.Fatalf("status after stop = %+v", st)
	}
	if st := g.Status("other"); st.Enabled {
		t.Fatal("unknown tenant must report a disabled gateway")
	}
	if err := g.Start(context.Background(), "other"); err == nil {
		t.Fatal("unknown tenant must fail")
	}

	cfg.AuthToken = "short"
	if _, err := NewS3Gateway(S3GatewayOptions{Config: cfg, JuiceFSBinary: "/rt/juicefs", Runner: r}); err == nil {
		t.Fatal("a secret key under 8 characters must be rejected")
	}
}
