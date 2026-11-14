package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WebRTCConfig holds the ICE settings of the desktop stream. When nothing is
// set, the browser gets the default Google STUN server and the guest gets
// no ICE server (the direct path through LYDIABOX_WEBRTC_UDP_PORT).
type WebRTCConfig struct {
	// STUNURLs are stun: or stuns: URLs. nil keeps the default STUN server;
	// an empty, non-nil list ("none") disables STUN.
	STUNURLs []string
	// TURNURLs are turn: or turns: URLs, for example
	// turn:turn.example.com:3478?transport=tcp.
	TURNURLs []string
	// TURNUsername and TURNPassword are static TURN credentials.
	TURNUsername string
	TURNPassword string
	// TURNSecret is a TURN REST shared secret (coturn static-auth-secret);
	// the host mints short-lived credentials from it.
	TURNSecret string
	// TURNTTL is the lifetime of minted TURN credentials (0: 1h).
	TURNTTL time.Duration
	// CloudflareTURNKeyID and CloudflareTURNAPIToken mint Cloudflare
	// Realtime TURN credentials for each viewer.
	CloudflareTURNKeyID    string
	CloudflareTURNAPIToken string
	// RelayOnly sets the ICE transport policy "relay" on both peers.
	RelayOnly bool
}

// MaxTURNTTL is the longest accepted TURN credential lifetime (the
// Cloudflare limit).
const MaxTURNTTL = 48 * time.Hour

// Configured reports whether any ICE setting differs from the default.
func (w WebRTCConfig) Configured() bool {
	return w.STUNURLs != nil || len(w.TURNURLs) > 0 || w.TURNSecret != "" || w.TURNUsername != "" ||
		w.TURNPassword != "" || w.CloudflareTURNKeyID != "" || w.CloudflareTURNAPIToken != "" || w.RelayOnly
}

// webrtcFile mirrors the webrtc section of lydiabox.yaml.
type webrtcFile struct {
	STUNURLs               *[]string `yaml:"stun_urls"`
	TURNURLs               *[]string `yaml:"turn_urls"`
	TURNUsername           *string   `yaml:"turn_username"`
	TURNPassword           *string   `yaml:"turn_password"`
	TURNSecret             *string   `yaml:"turn_secret"`
	TURNTTL                *string   `yaml:"turn_ttl"`
	CloudflareTURNKeyID    *string   `yaml:"cloudflare_turn_key_id"`
	CloudflareTURNAPIToken *string   `yaml:"cloudflare_turn_api_token"`
	RelayOnly              *bool     `yaml:"relay_only"`
}

func applyWebRTCFile(w *WebRTCConfig, f *webrtcFile) error {
	if f.STUNURLs != nil {
		w.STUNURLs = cleanURLs(*f.STUNURLs)
	}
	if f.TURNURLs != nil {
		w.TURNURLs = cleanURLs(*f.TURNURLs)
	}
	setStr(&w.TURNUsername, f.TURNUsername)
	setStr(&w.TURNPassword, f.TURNPassword)
	setStr(&w.TURNSecret, f.TURNSecret)
	setStr(&w.CloudflareTURNKeyID, f.CloudflareTURNKeyID)
	setStr(&w.CloudflareTURNAPIToken, f.CloudflareTURNAPIToken)
	if f.RelayOnly != nil {
		w.RelayOnly = *f.RelayOnly
	}
	return setDuration(&w.TURNTTL, f.TURNTTL, "webrtc.turn_ttl")
}

func applyWebRTCEnv(w *WebRTCConfig, env map[string]string) []error {
	var errs []error
	if v, ok := env["LYDIABOX_STUN_URLS"]; ok && strings.TrimSpace(v) != "" {
		w.STUNURLs = splitURLs(v)
	}
	if v, ok := env["LYDIABOX_TURN_URLS"]; ok && strings.TrimSpace(v) != "" {
		w.TURNURLs = splitURLs(v)
	}
	for key, dst := range map[string]*string{
		"LYDIABOX_TURN_USERNAME":             &w.TURNUsername,
		"LYDIABOX_TURN_PASSWORD":             &w.TURNPassword,
		"LYDIABOX_TURN_SECRET":               &w.TURNSecret,
		"LYDIABOX_CLOUDFLARE_TURN_KEY_ID":    &w.CloudflareTURNKeyID,
		"LYDIABOX_CLOUDFLARE_TURN_API_TOKEN": &w.CloudflareTURNAPIToken,
	} {
		if v := strings.TrimSpace(env[key]); v != "" {
			*dst = v
		}
	}
	if v := strings.TrimSpace(env["LYDIABOX_TURN_TTL"]); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_TURN_TTL: %q is not a duration", v))
		} else {
			w.TURNTTL = d
		}
	}
	if v := strings.TrimSpace(env["LYDIABOX_WEBRTC_RELAY_ONLY"]); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_WEBRTC_RELAY_ONLY: %q is not a boolean", v))
		} else {
			w.RelayOnly = b
		}
	}
	return errs
}

// splitURLs splits a comma or space separated list; "none" gives an empty,
// non-nil list.
func splitURLs(v string) []string {
	return cleanURLs(strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }))
}

func cleanURLs(in []string) []string {
	out := []string{}
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u != "" && !strings.EqualFold(u, "none") {
			out = append(out, u)
		}
	}
	return out
}

func (w WebRTCConfig) validate() []error {
	var errs []error
	for _, u := range w.STUNURLs {
		if err := checkICEURL(u, "stun", "stuns"); err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_STUN_URLS: %w", err))
		}
	}
	for _, u := range w.TURNURLs {
		if err := checkICEURL(u, "turn", "turns"); err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_TURN_URLS: %w", err))
		}
	}
	static := w.TURNUsername != "" || w.TURNPassword != ""
	switch {
	case w.TURNSecret != "" && static:
		errs = append(errs, errors.New("set LYDIABOX_TURN_SECRET or LYDIABOX_TURN_USERNAME and LYDIABOX_TURN_PASSWORD, not both"))
	case len(w.TURNURLs) > 0 && w.TURNSecret == "" && (w.TURNUsername == "" || w.TURNPassword == ""):
		errs = append(errs, errors.New("LYDIABOX_TURN_URLS needs LYDIABOX_TURN_USERNAME and LYDIABOX_TURN_PASSWORD, or LYDIABOX_TURN_SECRET"))
	case len(w.TURNURLs) == 0 && (static || w.TURNSecret != ""):
		errs = append(errs, errors.New("TURN credentials are set but LYDIABOX_TURN_URLS is empty"))
	}
	if (w.CloudflareTURNKeyID == "") != (w.CloudflareTURNAPIToken == "") {
		errs = append(errs, errors.New("set both LYDIABOX_CLOUDFLARE_TURN_KEY_ID and LYDIABOX_CLOUDFLARE_TURN_API_TOKEN"))
	}
	if w.TURNTTL != 0 && (w.TURNTTL < time.Minute || w.TURNTTL > MaxTURNTTL) {
		errs = append(errs, fmt.Errorf("LYDIABOX_TURN_TTL %s must be between 1m and 48h", w.TURNTTL))
	}
	if w.RelayOnly && len(w.TURNURLs) == 0 && w.CloudflareTURNKeyID == "" {
		errs = append(errs, errors.New("LYDIABOX_WEBRTC_RELAY_ONLY needs a TURN server (LYDIABOX_TURN_URLS or Cloudflare TURN)"))
	}
	return errs
}

// checkICEURL checks the scheme and host of a STUN or TURN URL
// (scheme:host[:port][?transport=udp|tcp]).
func checkICEURL(u string, schemes ...string) error {
	scheme, rest, ok := strings.Cut(u, ":")
	valid := false
	for _, s := range schemes {
		valid = valid || strings.EqualFold(scheme, s)
	}
	if !ok || !valid {
		return fmt.Errorf("%q must use the %s scheme", u, strings.Join(schemes, " or "))
	}
	hostport, query, _ := strings.Cut(rest, "?")
	if hostport == "" || strings.HasPrefix(hostport, "//") {
		return fmt.Errorf("%q must be %s:host[:port]", u, scheme)
	}
	if query != "" && query != "transport=udp" && query != "transport=tcp" {
		return fmt.Errorf("%q: the only query is transport=udp or transport=tcp", u)
	}
	return nil
}
