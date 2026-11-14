package config

import (
	"strings"
	"testing"
	"time"
)

func TestWebRTCDefaults(t *testing.T) {
	cfg, err := load(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebRTC.Configured() || cfg.WebRTC.STUNURLs != nil || cfg.WebRTC.RelayOnly {
		t.Fatalf("webrtc defaults %+v", cfg.WebRTC)
	}
}

func TestWebRTCEnv(t *testing.T) {
	cfg, err := load(t, map[string]string{
		"LYDIABOX_STUN_URLS":                 "none",
		"LYDIABOX_TURN_URLS":                 "turn:turn.example.com:3478?transport=udp, turns:turn.example.com:443?transport=tcp",
		"LYDIABOX_TURN_SECRET":               "s",
		"LYDIABOX_TURN_TTL":                  "30m",
		"LYDIABOX_CLOUDFLARE_TURN_KEY_ID":    "key",
		"LYDIABOX_CLOUDFLARE_TURN_API_TOKEN": "tok",
		"LYDIABOX_WEBRTC_RELAY_ONLY":         "true",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.WebRTC
	if w.STUNURLs == nil || len(w.STUNURLs) != 0 || len(w.TURNURLs) != 2 || w.TURNURLs[1] != "turns:turn.example.com:443?transport=tcp" ||
		w.TURNSecret != "s" || w.TURNTTL != 30*time.Minute || w.CloudflareTURNKeyID != "key" || w.CloudflareTURNAPIToken != "tok" || !w.RelayOnly {
		t.Fatalf("webrtc %+v", w)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := load(t, map[string]string{"LYDIABOX_WEBRTC_RELAY_ONLY": "maybe", "LYDIABOX_TURN_TTL": "soon"}, ""); err == nil ||
		!strings.Contains(err.Error(), "LYDIABOX_WEBRTC_RELAY_ONLY") || !strings.Contains(err.Error(), "LYDIABOX_TURN_TTL") {
		t.Fatalf("err %v", err)
	}
}

func TestWebRTCYAML(t *testing.T) {
	cfg, err := load(t, nil, `
webrtc:
  stun_urls: [stun:stun.example.com:3478]
  turn_urls:
    - turn:turn.example.com:3478?transport=tcp
  turn_username: user
  turn_password: pass
  turn_ttl: 2h
  relay_only: true
`)
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.WebRTC
	if len(w.STUNURLs) != 1 || w.TURNUsername != "user" || w.TURNPassword != "pass" || w.TURNTTL != 2*time.Hour || !w.RelayOnly {
		t.Fatalf("webrtc %+v", w)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWebRTCValidate(t *testing.T) {
	for _, tc := range []struct {
		w    WebRTCConfig
		want string
	}{
		{WebRTCConfig{STUNURLs: []string{"turn:x:1"}}, "must use the stun or stuns scheme"},
		{WebRTCConfig{TURNURLs: []string{"https://x"}, TURNSecret: "s"}, "must use the turn or turns scheme"},
		{WebRTCConfig{TURNURLs: []string{"turn:x:3478?transport=sctp"}, TURNSecret: "s"}, "transport=udp or transport=tcp"},
		{WebRTCConfig{TURNURLs: []string{"turn:x:3478"}}, "needs LYDIABOX_TURN_USERNAME"},
		{WebRTCConfig{TURNURLs: []string{"turn:x:3478"}, TURNSecret: "s", TURNUsername: "u"}, "not both"},
		{WebRTCConfig{TURNSecret: "s"}, "LYDIABOX_TURN_URLS is empty"},
		{WebRTCConfig{CloudflareTURNKeyID: "k"}, "set both"},
		{WebRTCConfig{CloudflareTURNKeyID: "k", CloudflareTURNAPIToken: "t", TURNTTL: 72 * time.Hour}, "between 1m and 48h"},
		{WebRTCConfig{RelayOnly: true}, "needs a TURN server"},
	} {
		errs := tc.w.validate()
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), tc.want) {
			t.Errorf("%+v: errs %v, want %q", tc.w, errs, tc.want)
		}
	}
	ok := WebRTCConfig{CloudflareTURNKeyID: "k", CloudflareTURNAPIToken: "t", RelayOnly: true, TURNTTL: time.Hour}
	if errs := ok.validate(); len(errs) != 0 {
		t.Fatalf("errs %v", errs)
	}
}
