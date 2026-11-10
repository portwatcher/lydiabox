package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MasterKeySize is the AES-256 key length in bytes.
const MasterKeySize = 32

// DecodeMasterKey parses the 64-character hex master key.
func DecodeMasterKey(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != MasterKeySize {
		return nil, errors.New("LYDIABOX_MASTER_KEY must be 64 hex characters (32 bytes)")
	}
	return b, nil
}

// MasterKeyBytes returns the decoded master key. Call EnsureSecrets first.
func (c *Config) MasterKeyBytes() ([]byte, error) {
	if c.MasterKey == "" {
		return nil, errors.New("config: master key is not provisioned")
	}
	return DecodeMasterKey(c.MasterKey)
}

// EnsureSecrets fills AuthToken and MasterKey. Values from the environment or
// YAML win; otherwise each secret is read from its file under StateDir, and
// generated (32 random bytes, hex) and written with mode 0600 when the file
// does not exist. The state directory is created with mode 0700.
func (c *Config) EnsureSecrets() error {
	if err := os.MkdirAll(c.StateDir(), 0o700); err != nil {
		return fmt.Errorf("config: create state dir: %w", err)
	}
	if err := os.Chmod(c.StateDir(), 0o700); err != nil {
		return fmt.Errorf("config: secure state dir: %w", err)
	}
	if c.MasterKey == "" {
		key, generated, err := loadOrCreateSecret(c.MasterKeyPath())
		if err != nil {
			return err
		}
		if _, err := DecodeMasterKey(key); err != nil {
			return fmt.Errorf("config: %s: %w", c.MasterKeyPath(), err)
		}
		c.MasterKey, c.MasterKeyGenerated = key, generated
	}
	if c.AuthToken == "" {
		tok, generated, err := loadOrCreateSecret(c.AuthTokenPath())
		if err != nil {
			return err
		}
		if len(tok) < MinAuthTokenLength {
			return fmt.Errorf("config: %s holds a token shorter than %d characters", c.AuthTokenPath(), MinAuthTokenLength)
		}
		c.AuthToken, c.AuthTokenGenerated = tok, generated
	}
	return nil
}

// loadOrCreateSecret reads a hex secret file, tightening its mode to 0600, or
// atomically creates it with fresh random content.
func loadOrCreateSecret(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			return "", false, fmt.Errorf("config: secure %s: %w", path, err)
		}
		return strings.TrimSpace(string(data)), false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("config: read %s: %w", path, err)
	}
	buf := make([]byte, MasterKeySize)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("config: generate secret: %w", err)
	}
	secret := hex.EncodeToString(buf)

	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return "", false, fmt.Errorf("config: create %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("config: chmod %s: %w", path, err)
	}
	if _, err := tmp.WriteString(secret + "\n"); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("config: sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return "", false, fmt.Errorf("config: close %s: %w", path, err)
	}
	// os.Link fails when path already exists, so a concurrent process that won
	// the race keeps its secret and this process reads it back.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return loadOrCreateSecret(path)
		}
		return "", false, fmt.Errorf("config: install %s: %w", path, err)
	}
	return secret, true, nil
}
