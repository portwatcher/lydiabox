package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// OwnerAssertionConfig is the owner login of a cell whose control plane
// signs the users in itself (LydiaBox Cloud in its OpenID Connect mode):
// the control plane verifies the owner and sends a short-lived assertion
// (header X-Lydiabox-Owner-Assertion, HMAC-SHA256 with the key of this
// cell). The settings come only from the environment of the cell.
type OwnerAssertionConfig struct {
	// Key is the 32-byte HMAC key of this cell
	// (LYDIABOX_OWNER_ASSERTION_KEY or _KEY_FILE, 64 hex characters).
	Key []byte
	// Email is the owner email that an assertion must name
	// (LYDIABOX_OWNER_EMAIL), in lower case.
	Email string
	// LogoutURL is the sign-out address that the web workstation opens
	// (LYDIABOX_OWNER_LOGOUT_URL, https).
	LogoutURL string
}

// Enabled reports whether the owner assertion login is on.
func (o OwnerAssertionConfig) Enabled() bool { return len(o.Key) > 0 }

// String redacts the key.
func (o OwnerAssertionConfig) String() string {
	key := ""
	if len(o.Key) > 0 {
		key = "<redacted>"
	}
	return fmt.Sprintf("{Key:%s Email:%s LogoutURL:%s}", key, o.Email, o.LogoutURL)
}

func applyOwnerEnv(cfg *Config, env map[string]string) []error {
	var errs []error
	key, err := secretEnv(env, "LYDIABOX_OWNER_ASSERTION_KEY")
	if err != nil {
		errs = append(errs, err)
	} else if key != "" {
		b, err := hex.DecodeString(key)
		if err != nil || len(b) != 32 {
			errs = append(errs, errors.New("LYDIABOX_OWNER_ASSERTION_KEY must be 64 hex characters (32 bytes)"))
		} else {
			cfg.Owner.Key = b
		}
	}
	cfg.Owner.Email = strings.ToLower(strings.TrimSpace(env["LYDIABOX_OWNER_EMAIL"]))
	cfg.Owner.LogoutURL = strings.TrimSpace(env["LYDIABOX_OWNER_LOGOUT_URL"])
	return errs
}

// validateOwner fails closed: the owner settings need cell mode, the key
// and the email are set together, and the logout address is https.
func (c *Config) validateOwner() []error {
	o := c.Owner
	if !o.Enabled() && o.Email == "" && o.LogoutURL == "" {
		return nil
	}
	var errs []error
	if !c.CellMode {
		errs = append(errs, errors.New("the LYDIABOX_OWNER_* settings need LYDIABOX_CELL_MODE: only a control plane sends owner assertions"))
	}
	if !o.Enabled() {
		errs = append(errs, errors.New("LYDIABOX_OWNER_EMAIL and LYDIABOX_OWNER_LOGOUT_URL need LYDIABOX_OWNER_ASSERTION_KEY_FILE"))
	}
	if at := strings.LastIndexByte(o.Email, '@'); at <= 0 || at == len(o.Email)-1 || strings.ContainsAny(o.Email, " <>,") {
		errs = append(errs, fmt.Errorf("LYDIABOX_OWNER_EMAIL %q must be one email address", o.Email))
	}
	if o.LogoutURL != "" {
		u, err := url.Parse(o.LogoutURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_OWNER_LOGOUT_URL %q must be an https URL", o.LogoutURL))
		}
	}
	return errs
}
