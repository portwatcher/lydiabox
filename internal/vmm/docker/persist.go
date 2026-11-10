package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lydiabox/lydiabox/internal/vmm"
)

// SpecLabel is the container label that stores the hash of the container
// configuration: the `docker create` arguments, the image ID, and the
// content of the injected guest binary. Create reuses a stopped container
// (and its writable layer, the persistent root) only when the hash matches.
const SpecLabel = "lydiabox.spec"

// containerInfo is the part of `docker inspect` that Create needs.
type containerInfo struct {
	exists bool
	status string
	spec   string
}

func (d *Driver) inspectContainer(ctx context.Context, name string) (containerInfo, error) {
	out, err := d.docker(ctx, "inspect", "--format", `{{.State.Status}} {{index .Config.Labels "`+SpecLabel+`"}}`, name)
	if err != nil {
		if notFound(err) {
			return containerInfo{}, nil
		}
		return containerInfo{}, fmt.Errorf("docker: inspect: %w", err)
	}
	status, spec, _ := strings.Cut(out, " ")
	return containerInfo{exists: true, status: status, spec: strings.TrimSpace(spec)}, nil
}

// imageID returns the local ID of image and pulls the image when it is
// missing, so the spec hash names the exact image the container runs.
func (d *Driver) imageID(ctx context.Context, image string) (string, error) {
	id, err := d.docker(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err == nil && id != "" {
		return id, nil
	}
	if _, perr := d.docker(ctx, "pull", image); perr != nil {
		return "", fmt.Errorf("docker: image %s is not available: %w", image, perr)
	}
	id, err = d.docker(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil || id == "" {
		return "", fmt.Errorf("docker: inspect image %s: %v", image, err)
	}
	return id, nil
}

// specHash hashes everything that a stopped container keeps from its
// creation: the create arguments, the image ID, and the guest binary. A
// bind-mounted file keeps the inode it had at creation, so a replaced guest
// binary also needs a new container.
func (d *Driver) specHash(ctx context.Context, spec vmm.Spec, args []string) (string, error) {
	image := args[len(args)-1]
	id, err := d.imageID(ctx, image)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, a := range args {
		fmt.Fprintf(h, "%d:%s\n", len(a), a)
	}
	fmt.Fprintf(h, "image:%s\n", id)
	if spec.GuestBinary != "" {
		f, err := os.Open(spec.GuestBinary)
		if err != nil {
			return "", fmt.Errorf("docker: guest binary: %w", err)
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", fmt.Errorf("docker: guest binary: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

// stopContainer stops a running or paused container and keeps it.
func (d *Driver) stopContainer(ctx context.Context, name, status string, force bool) error {
	if status == "paused" {
		_, _ = d.docker(ctx, "unpause", name)
	}
	if status != "running" && status != "paused" && status != "restarting" {
		return nil
	}
	var err error
	if force {
		_, err = d.docker(ctx, "kill", name)
	} else {
		_, err = d.docker(ctx, "stop", "--time", "30", name)
	}
	if err != nil && !notFound(err) {
		return fmt.Errorf("docker: stop container: %w", err)
	}
	return nil
}

// ResetRoot removes the stopped container of a tenant, so the next boot
// starts from a fresh copy of the image. Files under /home/agent and
// /workspace are on the JuiceFS volume and stay.
func (d *Driver) ResetRoot(ctx context.Context, tid string) error {
	if err := vmm.ValidTenantID(tid); err != nil {
		return err
	}
	if _, err := d.docker(ctx, "rm", "--force", ContainerName(tid)); err != nil && !notFound(err) {
		return fmt.Errorf("docker: remove container: %w", err)
	}
	d.opts.Logger.Info("removed the guest container; the next boot starts from the image", "tenant", tid)
	return nil
}
