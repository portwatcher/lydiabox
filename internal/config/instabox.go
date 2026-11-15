package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// DriverInstabox runs the agent VM on an InstaBox machine
// (internal/vmm/instabox). It needs the provider storage mode.
const DriverInstabox = "instabox"

// DefaultInstaboxIdlePause is the idle pause that the driver sets on the
// machine: a safety net for a cell that stopped without a pause.
const DefaultInstaboxIdlePause = 30 * time.Minute

// instaboxSizes maps the InstaBox vCPU counts to their memory in MiB.
var instaboxSizes = map[int]int{2: 4096, 4: 8192, 8: 16384, 16: 32768, 32: 65536}

// InstaboxConfig holds the settings of the instabox VM driver.
type InstaboxConfig struct {
	// APIURL is the API base URL (default https://api.instabox.sh); tests
	// and a later key broker set it.
	APIURL string
	// APIKey is the InstaBox API key (ibk_...). It comes only from the
	// environment: LYDIABOX_INSTABOX_API_KEY, or the file named by
	// LYDIABOX_INSTABOX_API_KEY_FILE. It is never logged or sent to the
	// guest.
	APIKey string
	// Snapshot is the ID or name of the guest image snapshot that new
	// machines are made from.
	Snapshot string
	// IdlePause is the idle time after which InstaBox pauses the machine
	// by itself when no cell holds it awake (0 turns it off).
	IdlePause time.Duration
}

// Configured reports whether any InstaBox setting that selects the driver
// is set.
func (c InstaboxConfig) Configured() bool { return c.APIKey != "" || c.Snapshot != "" }

// String redacts the API key.
func (c InstaboxConfig) String() string {
	key := ""
	if c.APIKey != "" {
		key = "<redacted>"
	}
	return fmt.Sprintf("{APIURL:%s APIKey:%s Snapshot:%s IdlePause:%s}", c.APIURL, key, c.Snapshot, c.IdlePause)
}

// instaboxFile mirrors the instabox section of lydiabox.yaml. The API key
// is not accepted there.
type instaboxFile struct {
	APIURL    *string `yaml:"api_url"`
	Snapshot  *string `yaml:"snapshot"`
	IdlePause *string `yaml:"idle_pause"`
}

func applyInstaboxFile(c *InstaboxConfig, f *instaboxFile) error {
	setStr(&c.APIURL, f.APIURL)
	setStr(&c.Snapshot, f.Snapshot)
	return setDuration(&c.IdlePause, f.IdlePause, "instabox.idle_pause")
}

func applyInstaboxEnv(c *InstaboxConfig, env map[string]string) []error {
	var errs []error
	if v := strings.TrimSpace(env["LYDIABOX_INSTABOX_API_URL"]); v != "" {
		c.APIURL = v
	}
	if v := strings.TrimSpace(env["LYDIABOX_INSTABOX_SNAPSHOT"]); v != "" {
		c.Snapshot = v
	}
	if v := strings.TrimSpace(env["LYDIABOX_INSTABOX_IDLE_PAUSE"]); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_INSTABOX_IDLE_PAUSE: %q is not a duration", v))
		} else {
			c.IdlePause = d
		}
	}
	key, err := secretEnv(env, "LYDIABOX_INSTABOX_API_KEY")
	if err != nil {
		errs = append(errs, err)
	} else if key != "" {
		c.APIKey = key
	}
	return errs
}

// secretEnv reads a secret from NAME, or from the file named by NAME_FILE
// (trimmed). Setting both is an error.
func secretEnv(env map[string]string, name string) (string, error) {
	v := strings.TrimSpace(env[name])
	file := strings.TrimSpace(env[name+"_FILE"])
	if file == "" {
		return v, nil
	}
	if v != "" {
		return "", fmt.Errorf("set %s or %s_FILE, not both", name, name)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("%s_FILE: %s is empty", name, file)
	}
	return s, nil
}

func (c InstaboxConfig) validate() []error {
	var errs []error
	if c.APIURL != "" {
		if err := checkHTTPURL(c.APIURL); err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_INSTABOX_API_URL: %w", err))
		}
	}
	if c.IdlePause != 0 && (c.IdlePause < 5*time.Minute || c.IdlePause > 30*time.Minute || c.IdlePause%time.Minute != 0) {
		errs = append(errs, fmt.Errorf("LYDIABOX_INSTABOX_IDLE_PAUSE %s must be 0 (off) or 5m to 30m in whole minutes", c.IdlePause))
	}
	return errs
}

// validateInstabox checks the settings that `lydiabox up` needs with the
// instabox driver.
func (c *Config) validateInstabox() []error {
	var errs []error
	if !c.ProviderStorage() {
		errs = append(errs, errors.New("LYDIABOX_VM_DRIVER=instabox needs LYDIABOX_STORAGE_MODE=provider: the agent files stay on the /data disk of the machine"))
	}
	if c.Instabox.APIKey == "" {
		errs = append(errs, errors.New("LYDIABOX_INSTABOX_API_KEY or LYDIABOX_INSTABOX_API_KEY_FILE is required with the instabox driver"))
	}
	if c.Instabox.Snapshot == "" {
		errs = append(errs, errors.New("LYDIABOX_INSTABOX_SNAPSHOT is required with the instabox driver (the snapshot of the imported guest image)"))
	}
	if mem, ok := instaboxSizes[c.VM.CPUs]; !ok || mem != c.VM.MemMB {
		errs = append(errs, fmt.Errorf("LYDIABOX_VM_CPUS=%d and LYDIABOX_VM_MEM_MB=%d are not an InstaBox size (2/4096, 4/8192, 8/16384, 16/32768, 32/65536)",
			c.VM.CPUs, c.VM.MemMB))
	}
	if c.VM.GPUPCIAddr != "" {
		errs = append(errs, errors.New("LYDIABOX_GPU_PCI_ADDR is not supported by the instabox driver"))
	}
	if c.VM.WebRTCPortSet && c.VM.WebRTCPort != 0 {
		errs = append(errs, fmt.Errorf("LYDIABOX_WEBRTC_UDP_PORT=%d cannot work with the instabox driver: a machine gets no inbound UDP; "+
			"leave it out or set 0, and give the desktop stream a TURN relay", c.VM.WebRTCPort))
	}
	return errs
}

// DesktopRelayConfigured reports whether the WebRTC settings give the
// desktop stream a TURN relay (static TURN or Cloudflare TURN). A TURN key
// that `lydiabox cloudflare setup` stored is not visible here.
func (c *Config) DesktopRelayConfigured() bool {
	return len(c.WebRTC.TURNURLs) > 0 || c.WebRTC.CloudflareTURNKeyID != ""
}
