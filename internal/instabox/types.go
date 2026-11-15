package instabox

import "time"

// State is a machine state (OpenAPI schema MachineState). The API calls a
// paused machine "suspended".
type State string

// Machine states.
const (
	StateCreating   State = "creating"
	StateStarting   State = "starting"
	StateRunning    State = "running"
	StateStopping   State = "stopping"
	StateStopped    State = "stopped"
	StateSuspending State = "suspending"
	StateSuspended  State = "suspended"
	StateResuming   State = "resuming"
	StateDeleting   State = "deleting"
	StateDeleted    State = "deleted"
	StateError      State = "error"
)

// Transitional reports whether the state changes by itself (an operation
// runs).
func (s State) Transitional() bool {
	switch s {
	case StateCreating, StateStarting, StateStopping, StateSuspending, StateResuming, StateDeleting:
		return true
	}
	return false
}

// OperationType is the kind of change an Operation makes.
type OperationType string

// Operation types.
const (
	OpCreate  OperationType = "create"
	OpStart   OperationType = "start"
	OpStop    OperationType = "stop"
	OpSuspend OperationType = "suspend"
	OpResume  OperationType = "resume"
	OpDelete  OperationType = "delete"
	OpRestore OperationType = "restore"
	OpResize  OperationType = "resize"
	OpMove    OperationType = "move"
)

// OperationState is the progress of an Operation.
type OperationState string

// Operation states; succeeded and failed are terminal.
const (
	OpQueued    OperationState = "queued"
	OpRunning   OperationState = "running"
	OpSucceeded OperationState = "succeeded"
	OpFailed    OperationState = "failed"
)

// Done reports whether the operation state is terminal.
func (s OperationState) Done() bool { return s == OpSucceeded || s == OpFailed }

// Operation is an asynchronous change to a machine (schema Operation).
type Operation struct {
	ID          string         `json:"id"`
	MachineID   string         `json:"machineId"`
	Type        OperationType  `json:"type"`
	State       OperationState `json:"state"`
	Phase       string         `json:"phase,omitempty"`
	Trigger     string         `json:"trigger,omitempty"`
	Error       *ErrorDetail   `json:"error"`
	Machine     *Machine       `json:"machine,omitempty"`
	ScheduledAt *time.Time     `json:"scheduledAt,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

// MachineSpec is the size of a machine.
type MachineSpec struct {
	VCPUs             int    `json:"vcpus"`
	MemMiB            int    `json:"memMiB"`
	PersistentDiskGiB int    `json:"persistentDiskGiB"`
	RootDiskGiB       int    `json:"rootDiskGiB,omitempty"`
	GPUType           string `json:"gpuType,omitempty"`
	GPUs              int    `json:"gpus,omitempty"`
}

// Storage is the persistent disk (/data) of a machine.
type Storage struct {
	SizeGiB     int    `json:"sizeGiB"`
	MountPath   string `json:"mountPath"`
	Persistent  bool   `json:"persistent"`
	Synced      bool   `json:"synced"`
	SnapshotSeq int64  `json:"snapshotSeq,omitempty"`
}

// Runtime reports whether a running machine accepts exec, files, and SSH.
type Runtime struct {
	Ready              bool  `json:"ready"`
	ProcessesPreserved *bool `json:"processesPreserved,omitempty"`
}

// TemplateRef names the template (and version) a machine or snapshot
// comes from.
type TemplateRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Kernel  string `json:"kernel,omitempty"`
}

// SnapshotRef names the snapshot a machine was created from.
type SnapshotRef struct {
	ID string `json:"id"`
}

// Machine is a sandbox (schema Machine). Fields the driver does not use
// are left out; unknown JSON fields are ignored.
type Machine struct {
	ID                      string            `json:"id"`
	Name                    string            `json:"name"`
	State                   State             `json:"state"`
	DesiredState            State             `json:"desiredState"`
	Revision                int64             `json:"revision"`
	OperationID             *string           `json:"operationId"`
	Template                TemplateRef       `json:"template"`
	Snapshot                *SnapshotRef      `json:"snapshot,omitempty"`
	Spec                    MachineSpec       `json:"spec"`
	Storage                 Storage           `json:"storage"`
	Runtime                 Runtime           `json:"runtime"`
	AutoSuspendAfterSeconds *int              `json:"autoSuspendAfterSeconds"`
	AutoSuspendedAt         *time.Time        `json:"autoSuspendedAt,omitempty"`
	MaxRunMinutes           *int              `json:"maxRunMinutes"`
	RunStartedAt            *time.Time        `json:"runStartedAt"`
	CooldownUntil           *time.Time        `json:"cooldownUntil"`
	Metadata                map[string]string `json:"metadata"`
	GuestUser               string            `json:"guestUser"`
	OrgID                   string            `json:"orgId"`
	Free                    bool              `json:"free,omitempty"`
	LastError               *ErrorDetail      `json:"lastError,omitempty"`
	ReadyAt                 *time.Time        `json:"readyAt,omitempty"`
	DeleteAt                *time.Time        `json:"deleteAt,omitempty"`
	ArchiveAt               *time.Time        `json:"archiveAt,omitempty"`
	CreatedAt               time.Time         `json:"createdAt"`
	UpdatedAt               time.Time         `json:"updatedAt"`
}

// IdlePaused reports whether the machine paused itself for idleness; a
// use request (exec, files, a lease, Wake) resumes it. A machine that was
// paused by a Suspend call needs Resume or Start.
func (m *Machine) IdlePaused() bool {
	return m.State == StateSuspended && m.AutoSuspendedAt != nil
}

// CreateMachineRequest creates a machine from a template or a snapshot
// (schema CreateMachineRequest; the server refuses unknown fields).
type CreateMachineRequest struct {
	Name         string `json:"name,omitempty"`
	TemplateID   string `json:"templateId,omitempty"`
	SnapshotID   string `json:"snapshotId,omitempty"`
	OrgID        string `json:"orgId,omitempty"`
	VCPUs        int    `json:"vcpus,omitempty"`
	MemMiB       int    `json:"memMiB,omitempty"`
	GPU          string `json:"gpu,omitempty"`
	DesiredState State  `json:"desiredState,omitempty"`
	// AutoSuspendAfterSeconds is the idle-pause time: nil uses the server
	// default (1800), 0 turns it off where the plan allows.
	AutoSuspendAfterSeconds *int `json:"autoSuspendAfterSeconds,omitempty"`
	// MaxRunMinutes caps one continuous run: nil is the plan default,
	// -1 is unlimited.
	MaxRunMinutes      *int              `json:"maxRunMinutes,omitempty"`
	DeleteAfterSeconds int               `json:"deleteAfterSeconds,omitempty"`
	ArchiveSeconds     int               `json:"archiveSeconds,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	GuestUser          string            `json:"guestUser,omitempty"`
	// FailFast refuses the create at once with 503 no_capacity when no
	// host has room, instead of a create operation that fails later.
	FailFast bool `json:"failFast,omitempty"`
}

// UpdateMachineRequest changes a machine (PATCH; absent fields stay).
type UpdateMachineRequest struct {
	Name                    string            `json:"name,omitempty"`
	AutoSuspendAfterSeconds *int              `json:"autoSuspendAfterSeconds,omitempty"`
	MaxRunMinutes           *int              `json:"maxRunMinutes,omitempty"`
	VCPUs                   int               `json:"vcpus,omitempty"`
	MemMiB                  int               `json:"memMiB,omitempty"`
	PersistentDiskGiB       int               `json:"persistentDiskGiB,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
	// Apply "restart" lets a change that needs a stop restart the machine.
	Apply string `json:"apply,omitempty"`
}

// MachineList is one page of ListMachines.
type MachineList struct {
	Machines   []Machine `json:"machines"`
	NextCursor *string   `json:"nextCursor"`
}

// ListMachinesOptions filters and pages ListMachines.
type ListMachinesOptions struct {
	Cursor string
	Limit  int
	// Metadata keeps machines whose metadata has every key with exactly
	// that value.
	Metadata map[string]string
}

// Snapshot is a saved machine (schema Snapshot).
type Snapshot struct {
	ID                string      `json:"id"`
	Name              string      `json:"name"`
	Description       string      `json:"description,omitempty"`
	OrgID             string      `json:"orgId"`
	SourceMachineID   string      `json:"sourceMachineId"`
	SourceSnapshotSeq int64       `json:"sourceSnapshotSeq"`
	Template          TemplateRef `json:"template"`
	RootSizeBytes     int64       `json:"rootSizeBytes"`
	DataSizeBytes     int64       `json:"dataSizeBytes,omitempty"`
	GuestUser         string      `json:"guestUser,omitempty"`
	Status            string      `json:"status"`
	Error             string      `json:"error,omitempty"`
	CreatedAt         time.Time   `json:"createdAt"`
}

// Snapshot status values.
const (
	SnapshotPending = "pending"
	SnapshotReady   = "ready"
	SnapshotFailed  = "failed"
)

// CreateSnapshotRequest saves a machine as a snapshot.
type CreateSnapshotRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	RootOnly    bool   `json:"rootOnly,omitempty"`
}

// Port access values.
const (
	AccessPublic  = "public"
	AccessPrivate = "private"
)

// PublishPortRequest gives a port of a machine an HTTPS URL.
type PublishPortRequest struct {
	Port        int    `json:"port"`
	Name        string `json:"name,omitempty"`
	Access      string `json:"access,omitempty"`
	RotateToken bool   `json:"rotateToken,omitempty"`
}

// PublicPort is a published port. Token and TokenURL are set only in the
// publish response that created or rotated a private port's token.
type PublicPort struct {
	ID        string    `json:"id"`
	MachineID string    `json:"machineId"`
	Port      int       `json:"port"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Access    string    `json:"access"`
	Protocol  string    `json:"protocol,omitempty"`
	Token     string    `json:"token,omitempty"`
	TokenURL  string    `json:"tokenUrl,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// PublicPortList is the answer of ListPorts.
type PublicPortList struct {
	Enabled bool         `json:"enabled"`
	Ports   []PublicPort `json:"ports"`
}
