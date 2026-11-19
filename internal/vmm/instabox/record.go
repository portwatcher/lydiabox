package instabox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lydiabox/lydiabox/internal/store"
)

// Store keeps the machine record of each tenant in the state database
// (*store.Store implements it). The secrets are encrypted at rest.
type Store interface {
	GetVMMachine(ctx context.Context, tenantID, driver string) (store.VMMachine, error)
	PutVMMachine(ctx context.Context, tenantID string, m store.VMMachine) error
	DeleteVMMachine(ctx context.Context, tenantID, driver string) error
}

// Record is the stored state of a tenant's machine.
type Record struct {
	// MachineID is the InstaBox machine (m_...); empty before the create.
	MachineID string `json:"machineId,omitempty"`
	// Name is the machine name: "lbx-" and a hash of the tenant ID.
	Name string `json:"name"`
	// CreateKey is the Idempotency-Key of a create that has no machine ID
	// yet. It is saved before the call, so that a restart after a crash
	// replays the same create instead of making a second machine.
	CreateKey string `json:"createKey,omitempty"`
	// LinkURL is the WebSocket URL of the guest link (the private port).
	LinkURL string `json:"linkUrl,omitempty"`
	// EnvSHA256 is the hash of the guest.env that the machine has.
	EnvSHA256 string `json:"envSha256,omitempty"`
	// GuestSHA256 is a hash of the machine ID and the host's guest binary
	// when the machine runs that binary (upgrade.go).
	GuestSHA256 string `json:"guestSha256,omitempty"`
	// SystemSHA256 is a hash of the machine ID, the system bundle, and the
	// host's guest binary when the machine has that bundle and its image
	// path has that guest binary (system.go).
	SystemSHA256 string `json:"systemSha256,omitempty"`
}

// recordSecrets are the encrypted part of a record.
type recordSecrets struct {
	LinkSecret string `json:"linkSecret"`
	PortToken  string `json:"portToken,omitempty"`
}

// MachineName returns the machine name of a tenant. Tenant IDs allow
// names that InstaBox refuses or that collide after a cut, so the name is
// a hash.
func MachineName(tenantID string) string {
	sum := sha256.Sum256([]byte(tenantID))
	return "lbx-" + hex.EncodeToString(sum[:8])
}

// MetadataTenant is the machine metadata key that holds the tenant ID.
const MetadataTenant = "lydiabox_tenant"

func newLinkSecret() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// loadRecord reads the record of a tenant; ok is false when there is none.
func loadRecord(ctx context.Context, st Store, tid string) (Record, recordSecrets, bool, error) {
	var (
		rec Record
		sec recordSecrets
	)
	m, err := st.GetVMMachine(ctx, tid, DriverName)
	if errors.Is(err, store.ErrNotFound) {
		return rec, sec, false, nil
	}
	if err != nil {
		return rec, sec, false, err
	}
	if err := json.Unmarshal(m.State, &rec); err != nil {
		return rec, sec, false, fmt.Errorf("instabox: machine record: %w", err)
	}
	if len(m.Secrets) > 0 {
		if err := json.Unmarshal(m.Secrets, &sec); err != nil {
			return rec, sec, false, fmt.Errorf("instabox: machine record secrets: %w", err)
		}
	}
	return rec, sec, true, nil
}

func saveRecord(ctx context.Context, st Store, tid string, rec Record, sec recordSecrets) error {
	sj, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	secj, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	return st.PutVMMachine(ctx, tid, store.VMMachine{Driver: DriverName, State: sj, Secrets: secj})
}

// LoadRecord returns the record of a tenant for the operator command.
func LoadRecord(ctx context.Context, st Store, tid string) (Record, bool, error) {
	rec, _, ok, err := loadRecord(ctx, st, tid)
	return rec, ok, err
}

// ForgetRecord removes the record of a tenant; the next start makes or
// adopts a machine again.
func ForgetRecord(ctx context.Context, st Store, tid string) error {
	err := st.DeleteVMMachine(ctx, tid, DriverName)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}
