package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/tabwriter"

	"github.com/lydiabox/lydiabox/internal/bootstrap"
	"github.com/lydiabox/lydiabox/internal/config"
)

const runtimeUsage = `Usage: lydiabox runtime <subcommand> [flags]

Subcommands:
  status         Show runtime binaries and guest images
  install        Download missing (or, with -force, all) required binaries
  fetch-images   Download guest images from LYDIABOX_ROOTFS_URL
  build-rootfs   Build guest images locally with Docker (deploy/rootfs/build-artifacts.sh)
`

func runRuntime(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(env.Stderr, runtimeUsage)
		return errUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "status":
		return runtimeStatus(env, rest)
	case "install":
		return runtimeInstall(ctx, env, rest)
	case "fetch-images":
		return runtimeFetchImages(ctx, env, rest)
	case "build-rootfs":
		return runtimeBuildRootfs(ctx, env, rest)
	default:
		fmt.Fprintf(env.Stderr, "lydiabox runtime: unknown subcommand %q\n\n%s", sub, runtimeUsage)
		return errUsage
	}
}

func runtimeSetup(env Env, name string, args []string, extra func(*flag.FlagSet)) (*config.Config, *bootstrap.Bootstrapper, error) {
	fs := newFlagSet("runtime "+name, env)
	configFile := fs.String("config", "", "path to lydiabox.yaml")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, errUsage
	}
	cfg, err := config.Load(config.LoadOptions{ConfigFile: *configFile})
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	boot, err := bootstrap.New(bootstrap.Options{
		BinDir: cfg.BinDir(), Driver: cfg.EffectiveVMDriver(), Mirrors: cfg.Mirrors,
		Logger: newLogger(stderrOrDiscard(env), cfg.LogLevel),
	})
	if err != nil {
		return nil, nil, err
	}
	return cfg, boot, nil
}

func runtimeStatus(env Env, args []string) error {
	cfg, boot, err := runtimeSetup(env, "status", args, nil)
	if err != nil {
		return err
	}
	statuses, err := boot.Check()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Platform: %s, VM driver: %s\n\n", boot.Platform(), cfg.EffectiveVMDriver())
	fmt.Fprintln(tw, "BINARY\tVERSION\tREQUIRED\tSOURCE\tPATH")
	for _, s := range statuses {
		src := s.Source
		if s.Outdated {
			src += " (outdated)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\n", s.Name, s.Version, s.Required, src, s.Path)
	}
	images := "missing"
	if bootstrap.ImagesPresent(cfg.ImagesDir()) {
		images = "present"
	}
	fmt.Fprintf(tw, "\nGuest images (%s): %s\n", cfg.ImagesDir(), images)
	return tw.Flush()
}

func runtimeInstall(ctx context.Context, env Env, args []string) error {
	var force *bool
	_, boot, err := runtimeSetup(env, "install", args, func(fs *flag.FlagSet) {
		force = fs.Bool("force", false, "reinstall every required binary")
	})
	if err != nil {
		return err
	}
	if !*force {
		_, err := boot.Ensure(ctx)
		return err
	}
	statuses, err := boot.Check()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range statuses {
		if s.Required {
			errs = append(errs, boot.Install(ctx, s.Name))
		}
	}
	return errors.Join(errs...)
}

func runtimeFetchImages(ctx context.Context, env Env, args []string) error {
	cfg, boot, err := runtimeSetup(env, "fetch-images", args, nil)
	if err != nil {
		return err
	}
	if cfg.RootfsURL == "" {
		return errors.New("LYDIABOX_ROOTFS_URL is not set; set it or run 'lydiabox runtime build-rootfs'")
	}
	if err := boot.FetchImages(ctx, cfg.RootfsURL, cfg.ImagesDir()); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Guest images installed in %s\n", cfg.ImagesDir())
	return nil
}

// BuildRootfsScript is the script path relative to the source checkout.
const BuildRootfsScript = "deploy/rootfs/build-artifacts.sh"

func runtimeBuildRootfs(ctx context.Context, env Env, args []string) error {
	var source, arch *string
	cfg, _, err := runtimeSetup(env, "build-rootfs", args, func(fs *flag.FlagSet) {
		source = fs.String("source", ".", "LydiaBox source checkout that contains deploy/rootfs")
		arch = fs.String("arch", runtime.GOARCH, "guest architecture: amd64 or arm64")
	})
	if err != nil {
		return err
	}
	if *arch != "amd64" && *arch != "arm64" {
		return fmt.Errorf("-arch must be amd64 or arm64, got %q", *arch)
	}
	script, err := filepath.Abs(filepath.Join(*source, BuildRootfsScript))
	if err != nil {
		return err
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("%s not found; pass -source with a LydiaBox source checkout: %w", script, err)
	}
	cmd := exec.CommandContext(ctx, script, "--out", cfg.ImagesDir(), "--arch", *arch)
	cmd.Dir = filepath.Dir(filepath.Dir(filepath.Dir(script)))
	cmd.Stdout, cmd.Stderr = env.Stdout, env.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build-rootfs failed: %w", err)
	}
	if !bootstrap.ImagesPresent(cfg.ImagesDir()) {
		return fmt.Errorf("build finished but %s does not contain %v", cfg.ImagesDir(), bootstrap.GuestImages)
	}
	fmt.Fprintf(env.Stdout, "Guest images installed in %s\n", cfg.ImagesDir())
	return nil
}
