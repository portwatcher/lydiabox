package instabox_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
)

func TestPorts(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "ports")

	p, err := r.client.PublishPrivatePort(ctx, m.ID, 7700, "guest link")
	if err != nil || p.Access != instabox.AccessPrivate || !strings.HasPrefix(p.Token, "ibp_") || !strings.Contains(p.TokenURL, p.Token) {
		t.Fatalf("private publish: %+v %v", p, err)
	}
	if p.Token != r.fake.PortToken(m.ID, 7700) {
		t.Fatal("token differs from the fake's")
	}
	// Publishing again keeps the URL; the token is rotated because the
	// second answer has none.
	again, err := r.client.PublishPrivatePort(ctx, m.ID, 7700, "guest link")
	if err != nil || again.URL != p.URL || again.Token == "" || again.Token == p.Token {
		t.Fatalf("republish: %+v %v", again, err)
	}
	pub, err := r.client.PublishPort(ctx, m.ID, instabox.PublishPortRequest{Port: 8080})
	if err != nil || pub.Access != instabox.AccessPublic || pub.Token != "" {
		t.Fatalf("public publish: %+v %v", pub, err)
	}
	list, err := r.client.ListPorts(ctx, m.ID)
	if err != nil || !list.Enabled || len(list.Ports) != 2 || list.Ports[0].Token != "" {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, bad := range []int{80, 5901, 6768, 70000} {
		if _, err := r.client.PublishPort(ctx, m.ID, instabox.PublishPortRequest{Port: bad}); err == nil {
			t.Fatalf("port %d accepted", bad)
		}
	}
	if err := r.client.UnpublishPort(ctx, m.ID, 8080); err != nil {
		t.Fatal(err)
	}
	if err := r.client.UnpublishPort(ctx, m.ID, 8080); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("second unpublish: %v", err)
	}
}

func TestSnapshots(t *testing.T) {
	r := newRig(t, instaboxtest.Options{MaxSnapshots: 2})
	ctx := ctxT(t)
	m := r.running(t, "snap")

	// A running machine gives a pending snapshot.
	s, err := r.client.CreateSnapshot(ctx, m.ID, instabox.CreateSnapshotRequest{Name: "live"})
	if err != nil || s.Status != instabox.SnapshotPending {
		t.Fatalf("live snapshot: %+v %v", s, err)
	}
	if s, err = r.client.WaitSnapshot(ctx, s.ID); err != nil || s.Status != instabox.SnapshotReady {
		t.Fatalf("wait: %+v %v", s, err)
	}
	// A stopped machine gives a ready one.
	r.done(t)(r.client.StopMachine(ctx, m.ID, instabox.StopOptions{}))
	s2, err := r.client.CreateSnapshot(ctx, m.ID, instabox.CreateSnapshotRequest{Name: "cold", Description: "stopped"})
	if err != nil || s2.Status != instabox.SnapshotReady || s2.SourceMachineID != m.ID {
		t.Fatalf("stopped snapshot: %+v %v", s2, err)
	}
	if _, err := r.client.CreateSnapshot(ctx, m.ID, instabox.CreateSnapshotRequest{Name: "third"}); !errors.Is(err, instabox.ErrQuota) {
		t.Fatalf("over snapshot limit: %v", err)
	}
	list, err := r.client.ListSnapshots(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "live" {
		t.Fatalf("list: %+v %v", list, err)
	}
	found, err := r.client.FindSnapshot(ctx, "cold")
	if err != nil || found.ID != s2.ID {
		t.Fatalf("find: %+v %v", found, err)
	}
	if _, err := r.client.FindSnapshot(ctx, "nope"); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("find missing: %v", err)
	}

	// A machine created from the snapshot.
	op, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "fromsnap", SnapshotID: s2.ID})
	if err != nil {
		t.Fatal(err)
	}
	done := r.done(t)(op, nil)
	if done.Machine.Snapshot == nil || done.Machine.Snapshot.ID != s2.ID {
		t.Fatalf("machine from snapshot: %+v", done.Machine)
	}

	if err := r.client.DeleteSnapshot(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.client.GetSnapshot(ctx, s.ID); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("get deleted snapshot: %v", err)
	}
	if err := r.client.DeleteSnapshot(ctx, s.ID); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
