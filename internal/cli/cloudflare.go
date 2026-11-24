package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/portwatcher/lydiabox/internal/bootstrap"
	"github.com/portwatcher/lydiabox/internal/cloudflare"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/store"
)

const cloudflareUsage = `Usage: lydiabox cloudflare <subcommand> [flags]

Remote access to the web UI through Cloudflare Zero Trust: a Cloudflare
Tunnel run by 'lydiabox up' and an Access application that allows only the
given emails (one-time PIN login).

Subcommands:
  setup      Create or update the tunnel, DNS record, and Access application
  status     Show the stored setup
  teardown   Delete the Cloudflare resources and the stored setup

setup and teardown need LYDIABOX_CLOUDFLARE_API_TOKEN and
LYDIABOX_CLOUDFLARE_ACCOUNT_ID (or CLOUDFLARE_API_TOKEN and
CLOUDFLARE_ACCOUNT_ID). See docs/remote-access-manual-test.md.
`

// cloudflareAPIURL is the API base URL; tests point it at a fake.
var cloudflareAPIURL = cloudflare.DefaultBaseURL

func runCloudflare(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(env.Stderr, cloudflareUsage)
		return errUsage
	}
	switch args[0] {
	case "setup":
		return cloudflareSetup(ctx, env, args[1:])
	case "status":
		return cloudflareStatus(ctx, env, args[1:])
	case "teardown":
		return cloudflareTeardown(ctx, env, args[1:])
	default:
		fmt.Fprintf(env.Stderr, "lydiabox cloudflare: unknown subcommand %q\n\n%s", args[0], cloudflareUsage)
		return errUsage
	}
}

// listFlag collects a repeatable, comma-separated flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

// cloudflareEnv loads the configuration and opens the state database.
func cloudflareEnv(ctx context.Context, env Env, configFile string) (*config.Config, *store.Store, error) {
	cfg, err := config.Load(config.LoadOptions{ConfigFile: configFile})
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	st, err := openStateStore(ctx, env, cfg, newLogger(stderrOrDiscard(env), cfg.LogLevel))
	if err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

func cloudflareClient(cfg *config.Config) (*cloudflare.Client, error) {
	r := cfg.Remote
	if r.CloudflareAPIToken == "" || r.CloudflareAccountID == "" {
		return nil, errors.New("set LYDIABOX_CLOUDFLARE_API_TOKEN and LYDIABOX_CLOUDFLARE_ACCOUNT_ID " +
			"(the token needs the permissions listed in docs/remote-access-manual-test.md)")
	}
	return cloudflare.NewClient(cloudflare.Options{BaseURL: cloudflareAPIURL, Token: r.CloudflareAPIToken, AccountID: r.CloudflareAccountID})
}

func cloudflareSetup(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet("cloudflare setup", env)
	configFile := fs.String("config", "", "path to lydiabox.yaml")
	var p cloudflare.SetupParams
	var emails, bypass listFlag
	fs.StringVar(&p.Hostname, "hostname", "", "public hostname of the web UI, for example agent.example.com (required)")
	fs.Var(&emails, "email", "email allowed to sign in; repeat or separate with commas; @example.com allows a domain (required)")
	fs.StringVar(&p.Zone, "zone", "", "DNS zone of the hostname (default: found from the hostname)")
	fs.StringVar(&p.OriginURL, "origin", "", "URL that cloudflared sends requests to (default: http://127.0.0.1:<listen port>)")
	fs.StringVar(&p.TunnelName, "tunnel-name", "", "tunnel name (default: lydiabox-<hostname with dashes>)")
	fs.BoolVar(&p.ServiceToken, "service-token", true, "create an Access service token for API, WebDAV, and S3 clients")
	fs.BoolVar(&p.TURN, "turn", true, "create a Cloudflare Realtime TURN key so the desktop stream works through the tunnel")
	fs.Var(&bypass, "bypass", "paths that skip the Access login and keep only the admin token, for example /webdav,/s3")
	fs.StringVar(&p.SessionDuration, "session", "24h", "Access login session duration")
	install := fs.Bool("install", true, "install cloudflared now if it is missing")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if p.Hostname == "" || len(emails) == 0 || fs.NArg() > 0 {
		fmt.Fprintln(env.Stderr, "Usage: lydiabox cloudflare setup -hostname agent.example.com -email me@example.com [flags]")
		fs.PrintDefaults()
		return errUsage
	}
	p.Emails, p.BypassPaths = emails, bypass

	cfg, st, err := cloudflareEnv(ctx, env, *configFile)
	if err != nil {
		return err
	}
	defer st.Close()
	client, err := cloudflareClient(cfg)
	if err != nil {
		return err
	}
	if p.OriginURL == "" {
		p.OriginURL = defaultOrigin(cfg)
	}
	prev, prevSec, _, err := loadCloudflare(ctx, st, cfg.TenantID)
	if err != nil {
		return err
	}
	p.Progress = func(format string, a ...any) { fmt.Fprintf(env.Stdout, "  "+format+"\n", a...) }
	fmt.Fprintf(env.Stdout, "Setting up remote access for %s:\n", p.Hostname)
	state, sec, err := cloudflare.Setup(ctx, client, p, prev, prevSec)
	if err != nil {
		return err
	}
	if err := saveCloudflare(ctx, st, cfg.TenantID, state, sec); err != nil {
		return err
	}
	if *install {
		installCloudflared(ctx, env, cfg)
	}
	printSetupResult(env, cfg, state, sec, prevSec)
	return nil
}

func installCloudflared(ctx context.Context, env Env, cfg *config.Config) {
	boot, err := bootstrap.New(bootstrap.Options{BinDir: cfg.BinDir(), Driver: cfg.EffectiveVMDriver(), Skip: skipBinaries(cfg), Mirrors: cfg.Mirrors,
		Logger: newLogger(stderrOrDiscard(env), cfg.LogLevel)})
	if err == nil {
		if _, err = boot.Resolve("cloudflared"); err != nil {
			err = boot.Install(ctx, "cloudflared")
		}
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "warning: cloudflared is not installed (%v); 'lydiabox up' tries again\n", err)
	}
}

func printSetupResult(env Env, cfg *config.Config, state cloudflare.State, sec, prevSec cloudflare.Secrets) {
	w := env.Stdout
	fmt.Fprintf(w, "\nRemote access is set up. Restart 'lydiabox up' to start the tunnel, then open:\n\n    https://%s\n\n", state.Hostname)
	fmt.Fprintf(w, "Sign in with a one-time PIN sent to: %s\n", strings.Join(state.Emails, ", "))
	if state.ServiceTokenClientID != "" {
		fmt.Fprintf(w, "\nNon-browser clients (API, WebDAV, S3) send these headers with the admin token:\n")
		fmt.Fprintf(w, "    CF-Access-Client-Id: %s\n", state.ServiceTokenClientID)
		if sec.ServiceTokenSecret != prevSec.ServiceTokenSecret {
			fmt.Fprintf(w, "    CF-Access-Client-Secret: %s\n", sec.ServiceTokenSecret)
		} else {
			fmt.Fprintf(w, "    CF-Access-Client-Secret: unchanged ('lydiabox cloudflare status -show-secret' prints it)\n")
		}
	}
	for _, p := range state.BypassPaths {
		fmt.Fprintf(w, "\nhttps://%s%s skips the Access login; only the admin token protects it.\n", state.Hostname, p)
	}
	if state.TURNKeyID != "" {
		if cfg.WebRTC.Configured() {
			fmt.Fprintf(w, "\nThe desktop stream keeps your LYDIABOX_*TURN*/STUN settings; they override the stored TURN key.\n")
		} else {
			fmt.Fprintf(w, "\nThe desktop stream goes through Cloudflare TURN (relay only, no UDP port).\n")
		}
	}
	if cfg.Remote.ListenAddrSet && !config.IsLoopbackAddr(cfg.ListenAddr) {
		fmt.Fprintf(w, "\nwarning: LYDIABOX_LISTEN_ADDR=%s is reachable without Cloudflare Access; use 127.0.0.1 to allow only the tunnel.\n", cfg.ListenAddr)
	}
}

func cloudflareStatus(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet("cloudflare status", env)
	configFile := fs.String("config", "", "path to lydiabox.yaml")
	showSecret := fs.Bool("show-secret", false, "also print the service token secret and the tunnel token")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	cfg, st, err := cloudflareEnv(ctx, env, *configFile)
	if err != nil {
		return err
	}
	defer st.Close()
	state, sec, ok, err := loadCloudflare(ctx, st, cfg.TenantID)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(env.Stdout, "Remote access is not set up. Run 'lydiabox cloudflare setup -hostname <name> -email <you>'.")
		return nil
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	row("Hostname", "https://"+state.Hostname)
	row("Emails", strings.Join(state.Emails, ", "))
	row("Team domain", state.TeamDomain)
	row("Audience tag", state.AUD)
	row("Account", state.AccountID)
	row("Tunnel", fmt.Sprintf("%s (%s)", state.TunnelName, state.TunnelID))
	row("Origin", state.OriginURL)
	row("DNS zone", state.ZoneName)
	row("Service token ID", state.ServiceTokenClientID)
	row("Bypass paths", strings.Join(state.BypassPaths, ", "))
	row("TURN key", state.TURNKeyID)
	row("Tunnel mode", cfg.Remote.Tunnel)
	row("Updated", state.UpdatedAt.Format("2006-01-02 15:04:05 MST"))
	if *showSecret {
		row("Service token secret", sec.ServiceTokenSecret)
		row("Tunnel token", sec.TunnelToken)
		row("TURN key secret", sec.TURNKeySecret)
	}
	if cfg.Remote.AccessConfigured() {
		row("Overrides", "LYDIABOX_ACCESS_* values replace the stored Access settings")
	}
	return tw.Flush()
}

func cloudflareTeardown(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet("cloudflare teardown", env)
	configFile := fs.String("config", "", "path to lydiabox.yaml")
	hostname := fs.String("hostname", "", "hostname to tear down when no setup is stored")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	cfg, st, err := cloudflareEnv(ctx, env, *configFile)
	if err != nil {
		return err
	}
	defer st.Close()
	client, err := cloudflareClient(cfg)
	if err != nil {
		return err
	}
	state, _, ok, err := loadCloudflare(ctx, st, cfg.TenantID)
	if err != nil {
		return err
	}
	if !ok {
		if *hostname == "" {
			return errors.New("remote access is not set up; pass -hostname to delete the resources of a lost setup")
		}
		state = cloudflare.State{Hostname: strings.ToLower(*hostname)}
	}
	fmt.Fprintf(env.Stdout, "Tearing down remote access for %s (stop 'lydiabox up' first, or the tunnel delete fails):\n", state.Hostname)
	progress := func(format string, a ...any) { fmt.Fprintf(env.Stdout, "  "+format+"\n", a...) }
	if err := cloudflare.Teardown(ctx, client, state, progress); err != nil {
		return fmt.Errorf("%w (the stored setup is kept; run teardown again)", err)
	}
	if ok {
		if err := st.DeleteRemoteAccess(ctx, cfg.TenantID, remoteProvider); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	fmt.Fprintln(env.Stdout, "Remote access is removed.")
	return nil
}

var _ flag.Value = (*listFlag)(nil)
