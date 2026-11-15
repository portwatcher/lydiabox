package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
	ib "github.com/lydiabox/lydiabox/internal/instabox"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

const instaboxUsage = `Usage: lydiabox instabox <subcommand> [flags]

Subcommands:
  status    Show the InstaBox machine of the tenant
  destroy   Delete the machine and its /data disk: destroy -confirm <machine ID>
            -forget removes only the local record of a machine that is gone

Stop 'lydiabox up' for the tenant before destroy.
`

func runInstabox(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(env.Stderr, instaboxUsage)
		return errUsage
	}
	switch args[0] {
	case "status":
		return instaboxStatus(ctx, env, args[1:])
	case "destroy":
		return instaboxDestroy(ctx, env, args[1:])
	default:
		fmt.Fprintf(env.Stderr, "lydiabox instabox: unknown subcommand %q\n\n%s", args[0], instaboxUsage)
		return errUsage
	}
}

// instaboxTools are the API client and the state database of a command.
type instaboxTools struct {
	cfg    *config.Config
	client *ib.Client
	store  vmmib.Store
	close  func()
}

func openInstabox(ctx context.Context, env Env, configFile string) (*instaboxTools, error) {
	cfg, err := config.Load(config.LoadOptions{ConfigFile: configFile})
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Instabox.APIKey == "" {
		return nil, errors.New("set LYDIABOX_INSTABOX_API_KEY or LYDIABOX_INSTABOX_API_KEY_FILE")
	}
	log := newLogger(stderrOrDiscard(env), cfg.LogLevel)
	client, err := newInstaboxClient(cfg, log.With("component", "instabox-api"))
	if err != nil {
		return nil, err
	}
	st, err := openStateStore(ctx, env, cfg, log)
	if err != nil {
		return nil, err
	}
	return &instaboxTools{cfg: cfg, client: client, store: st, close: func() { st.Close() }}, nil
}

func instaboxFlags(env Env, name string, args []string, extra func(*flag.FlagSet)) (string, error) {
	fs := newFlagSet("instabox "+name, env)
	configFile := fs.String("config", "", "path to lydiabox.yaml")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return "", errUsage
	}
	return *configFile, nil
}

func instaboxStatus(ctx context.Context, env Env, args []string) error {
	configFile, err := instaboxFlags(env, "status", args, nil)
	if err != nil {
		return err
	}
	t, err := openInstabox(ctx, env, configFile)
	if err != nil {
		return err
	}
	defer t.close()
	tid := t.cfg.TenantID
	rec, ok, err := vmmib.LoadRecord(ctx, t.store, tid)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	defer tw.Flush()
	fmt.Fprintf(tw, "Tenant:\t%s\n", tid)
	if !ok || rec.MachineID == "" {
		fmt.Fprintf(tw, "Machine:\tnone (the first use makes %s from the snapshot)\n", vmmib.MachineName(tid))
		if rec.CreateKey != "" {
			fmt.Fprintf(tw, "Pending create:\tyes (the next start replays it)\n")
		}
		return nil
	}
	m, err := t.client.GetMachine(ctx, rec.MachineID)
	if errors.Is(err, ib.ErrNotFound) {
		fmt.Fprintf(tw, "Machine:\t%s (does not exist: deleted; run 'lydiabox instabox destroy -forget')\n", rec.MachineID)
		return nil
	}
	if err != nil {
		return err
	}
	printMachine(tw, m, rec)
	return nil
}

func printMachine(w io.Writer, m *ib.Machine, rec vmmib.Record) {
	fmt.Fprintf(w, "Machine:\t%s (%s)\n", m.ID, m.Name)
	state := string(m.State)
	if m.IdlePaused() {
		state += " (idle pause at " + m.AutoSuspendedAt.Format(time.RFC3339) + ")"
	}
	fmt.Fprintf(w, "State:\t%s\n", state)
	fmt.Fprintf(w, "Size:\t%d vCPU, %d MiB, /data %d GiB\n", m.Spec.VCPUs, m.Spec.MemMiB, m.Storage.SizeGiB)
	idle := "off"
	if m.AutoSuspendAfterSeconds != nil && *m.AutoSuspendAfterSeconds > 0 {
		idle = (time.Duration(*m.AutoSuspendAfterSeconds) * time.Second).String()
	}
	fmt.Fprintf(w, "Idle pause:\t%s\n", idle)
	if m.Snapshot != nil {
		fmt.Fprintf(w, "Snapshot:\t%s\n", m.Snapshot.ID)
	}
	if rec.LinkURL != "" {
		fmt.Fprintf(w, "Guest link:\t%s\n", rec.LinkURL)
	}
	if m.LastError != nil {
		fmt.Fprintf(w, "Last error:\t%s: %s\n", m.LastError.Code, m.LastError.Message)
	}
	fmt.Fprintf(w, "Created:\t%s\n", m.CreatedAt.Format(time.RFC3339))
}

func instaboxDestroy(ctx context.Context, env Env, args []string) error {
	var confirm *string
	var forget *bool
	configFile, err := instaboxFlags(env, "destroy", args, func(fs *flag.FlagSet) {
		confirm = fs.String("confirm", "", "the machine ID (or name) to delete; required")
		forget = fs.Bool("forget", false, "remove only the local record of a machine that no longer exists")
	})
	if err != nil {
		return err
	}
	t, err := openInstabox(ctx, env, configFile)
	if err != nil {
		return err
	}
	defer t.close()
	tid := t.cfg.TenantID
	rec, _, err := vmmib.LoadRecord(ctx, t.store, tid)
	if err != nil {
		return err
	}
	if rec.MachineID == "" {
		fmt.Fprintf(env.Stdout, "Tenant %s has no machine.\n", tid)
		return nil
	}
	m, err := t.client.GetMachine(ctx, rec.MachineID)
	if errors.Is(err, ib.ErrNotFound) {
		if !*forget {
			return fmt.Errorf("machine %s does not exist; run again with -forget to remove the local record", rec.MachineID)
		}
		if err := vmmib.ForgetRecord(ctx, t.store, tid); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "Removed the record of machine %s. The next start makes a new machine.\n", rec.MachineID)
		return nil
	}
	if err != nil {
		return err
	}
	if *forget {
		return fmt.Errorf("machine %s exists; -forget is only for a machine that is gone (delete it with -confirm %s)", m.ID, m.ID)
	}
	if *confirm != m.ID && *confirm != m.Name {
		fmt.Fprintf(env.Stdout, "This deletes machine %s (%s, %s) and its /data disk with every agent file. It cannot be undone.\n",
			m.ID, m.Name, m.State)
		return fmt.Errorf("run again with -confirm %s", m.ID)
	}
	op, err := t.client.DeleteMachine(ctx, m.ID)
	if err == nil {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err = t.client.WaitOperation(wctx, op)
		cancel()
	}
	if err != nil {
		return fmt.Errorf("delete machine %s: %w", m.ID, err)
	}
	if err := vmmib.ForgetRecord(ctx, t.store, tid); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Deleted machine %s and its disk. Its snapshots stay.\n", m.ID)
	return nil
}
