package harness

// The ACP bridge (internal/acp, `lydiabox-guest acp`) runs in a guest
// terminal in raw mode and exchanges one JSON object per line with the host:
// BridgeCommand from the host on its input, BridgeMessage to the host on its
// output.

// Engines of a harness session.
const (
	// EnginePTY runs the harness TUI in a terminal.
	EnginePTY = "pty"
	// EngineACP runs the harness's ACP agent through the bridge.
	EngineACP = "acp"
)

// Bridge command types (host → bridge).
const (
	BridgePrompt     = "prompt"
	BridgeCancel     = "cancel"
	BridgePermission = "permission"
	BridgeSync       = "sync"
	BridgeMode       = "mode"
)

// BridgeCommand is one line from the host.
type BridgeCommand struct {
	Type string `json:"type"`
	// Text is the prompt of BridgePrompt.
	Text string `json:"text,omitempty"`
	// RequestID and OptionID answer a permission request.
	RequestID string `json:"requestId,omitempty"`
	OptionID  string `json:"optionId,omitempty"`
	// ModeID is the mode of BridgeMode.
	ModeID string `json:"modeId,omitempty"`
}

// Bridge message types (bridge → host).
const (
	// BridgeEvent carries one new or changed event.
	BridgeEvent = "event"
	// BridgeReset tells the host to drop its events; the bridge sends all of
	// them again.
	BridgeReset = "reset"
	// BridgeStatus carries the state of the agent.
	BridgeStatus = "status"
)

// BridgeMessage is one line from the bridge.
type BridgeMessage struct {
	Type   string       `json:"type"`
	Event  *Event       `json:"event,omitempty"`
	Status *AgentStatus `json:"status,omitempty"`
}

// States of an ACP agent.
const (
	AgentStarting  = "starting"
	AgentIdle      = "idle"
	AgentWorking   = "working"
	AgentAttention = "attention"
	AgentEnded     = "ended"
)

// AgentMode is one session mode of an ACP agent.
type AgentMode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AgentStatus is the state of the ACP agent of a session.
type AgentStatus struct {
	State string `json:"state"`
	// NativeID is the session ID of the agent (the native session ID).
	NativeID string `json:"nativeId,omitempty"`
	// Agent names the ACP agent and its version.
	Agent    string      `json:"agent,omitempty"`
	Mode     string      `json:"mode,omitempty"`
	Modes    []AgentMode `json:"modes,omitempty"`
	Commands []string    `json:"commands,omitempty"`
	// Queued is the number of prompts that wait for the current turn.
	Queued int    `json:"queued,omitempty"`
	Error  string `json:"error,omitempty"`
}
