package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerAssertionSettings(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "owner.key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := map[string]string{"LYDIABOX_OWNER_ASSERTION_KEY_FILE": keyFile, "LYDIABOX_OWNER_EMAIL": "Owner@Example.com",
		"LYDIABOX_OWNER_LOGOUT_URL": "https://lydiabox.com/auth/logout"}
	cfg := &Config{CellMode: true}
	if errs := applyOwnerEnv(cfg, good); len(errs) != 0 {
		t.Fatal(errs)
	}
	if !cfg.Owner.Enabled() || cfg.Owner.Email != "owner@example.com" || len(cfg.Owner.Key) != 32 {
		t.Fatalf("owner %v", cfg.Owner)
	}
	if errs := cfg.validateOwner(); len(errs) != 0 {
		t.Fatal(errs)
	}
	if s := cfg.Owner.String(); strings.Contains(s, "abab") || !strings.Contains(s, "<redacted>") {
		t.Fatalf("String shows the key: %s", s)
	}
	for name, mutate := range map[string]func(c *Config, env map[string]string){
		"not a cell":    func(c *Config, _ map[string]string) { c.CellMode = false },
		"no email":      func(_ *Config, env map[string]string) { delete(env, "LYDIABOX_OWNER_EMAIL") },
		"two emails":    func(_ *Config, env map[string]string) { env["LYDIABOX_OWNER_EMAIL"] = "a@b.c,d@e.f" },
		"http logout":   func(_ *Config, env map[string]string) { env["LYDIABOX_OWNER_LOGOUT_URL"] = "http://lydiabox.com/" },
		"email, no key": func(_ *Config, env map[string]string) { delete(env, "LYDIABOX_OWNER_ASSERTION_KEY_FILE") },
		"missing file":  func(_ *Config, env map[string]string) { env["LYDIABOX_OWNER_ASSERTION_KEY_FILE"] = keyFile + ".none" },
		"short key": func(_ *Config, env map[string]string) {
			delete(env, "LYDIABOX_OWNER_ASSERTION_KEY_FILE")
			env["LYDIABOX_OWNER_ASSERTION_KEY"] = "abcd"
		},
		"key and file": func(_ *Config, env map[string]string) { env["LYDIABOX_OWNER_ASSERTION_KEY"] = strings.Repeat("cd", 32) },
	} {
		env := map[string]string{}
		for k, v := range good {
			env[k] = v
		}
		c := &Config{CellMode: true}
		mutate(c, env)
		errs := applyOwnerEnv(c, env)
		errs = append(errs, c.validateOwner()...)
		if len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}
