package rpc

import (
	"encoding/json"
	"time"
)

// Guest daemon methods. The host opens one stream per call.
const (
	// MethodPing (unary) returns PingResult.
	MethodPing = "ping"
	// MethodSync (unary) flushes guest filesystems (sync(2)).
	MethodSync = "fs.sync"

	// MethodPTYStart (unary) starts a PTY: PTYStartParams -> PTYInfo.
	MethodPTYStart = "pty.start"
	// MethodPTYList (unary) returns PTYListResult.
	MethodPTYList = "pty.list"
	// MethodPTYAttach (streaming) attaches to a PTY: PTYIDParams ->
	// PTYAttachResult, then one FrameReplay with the ring buffer, then live
	// FrameData output and PTYEvent JSON frames. The client sends FrameData
	// input and PTYControl JSON frames. Closing the stream detaches.
	MethodPTYAttach = "pty.attach"
	// MethodPTYWait (unary) blocks until the PTY exits: PTYIDParams -> PTYExit.
	MethodPTYWait = "pty.wait"
	// MethodPTYResize (unary): PTYResizeParams.
	MethodPTYResize = "pty.resize"
	// MethodPTYSignal (unary): PTYSignalParams.
	MethodPTYSignal = "pty.signal"
	// MethodPTYKill (unary) terminates (SIGTERM, then SIGKILL after 3 s) and
	// forgets the PTY: PTYIDParams.
	MethodPTYKill = "pty.kill"

	// MethodToolsList (unary) returns the MCP tools/list result.
	MethodToolsList = "mcp.tools"
	// MethodToolCall (unary) relays an MCP tools/call: ToolCallParams -> the
	// MCP CallToolResult object.
	MethodToolCall = "mcp.call"

	// MethodModeGet (unary) returns ModeState.
	MethodModeGet = "mode.get"
	// MethodModeSet (unary): ModeSetParams -> ModeState.
	MethodModeSet = "mode.set"

	// MethodDesktopLaunch (unary): LaunchParams.
	MethodDesktopLaunch = "desktop.launch"
	// MethodDesktopSignal (streaming) adds one WebRTC viewer:
	// SignalParams -> SignalHello, then signaling messages (docs/api.md
	// section 9: offer and ice from the host; answer, ice, and error from the
	// guest) as JSON frames. Closing the stream removes the viewer.
	MethodDesktopSignal = "desktop.signal"

	// MethodEventsSubscribe (streaming) returns {} and then streams desktop
	// events (agent_action and mode messages of docs/api.md section 9) as
	// JSON frames.
	MethodEventsSubscribe = "events.subscribe"
)

// Environment variables shared by the guest daemon and the VMM drivers.
const (
	// EnvHostAddr is the guest-to-host broker address (port 9001).
	EnvHostAddr = "LYDIABOX_HOST_ADDR"
	// EnvGatewayAddr is the guest-to-host LLM gateway tunnel (port 9002).
	EnvGatewayAddr = "LYDIABOX_GATEWAY_ADDR"
	// EnvGuestListen is the address the guest daemon listens on.
	EnvGuestListen = "LYDIABOX_GUEST_LISTEN"
	// EnvWebRTCPort is the single UDP port used for WebRTC media.
	EnvWebRTCPort = "LYDIABOX_WEBRTC_UDP_PORT"
)

// Default addresses.
const (
	// HostAddrVsock is LYDIABOX_HOST_ADDR under the chv driver.
	HostAddrVsock = "vsock://2:9001"
	// GatewayAddrVsock is LYDIABOX_GATEWAY_ADDR under the chv driver.
	GatewayAddrVsock = "vsock://2:9002"
	// GuestListenVsock is the guest daemon address under the chv driver.
	GuestListenVsock = "vsock://:9000"
	// DockerSocketDir is the bind-mounted socket directory in the container.
	DockerSocketDir = "/run/lydiabox"
	// HostAddrDocker is LYDIABOX_HOST_ADDR under the docker driver.
	HostAddrDocker = "unix:///run/lydiabox/host-9001.sock"
	// GatewayAddrDocker is LYDIABOX_GATEWAY_ADDR under the docker driver.
	GatewayAddrDocker = "unix:///run/lydiabox/host-9002.sock"
	// GuestListenDocker is the guest daemon address under the docker driver.
	GuestListenDocker = "unix:///run/lydiabox/guest.sock"
	// DefaultWebRTCPort is the default WebRTC UDP port.
	DefaultWebRTCPort = 50000
)

// PingResult answers MethodPing.
type PingResult struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

// PTYStartParams starts a PTY. An empty ID makes the guest choose one.
type PTYStartParams struct {
	ID      string            `json:"id,omitempty"`
	Command []string          `json:"command"`
	Dir     string            `json:"dir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cols    uint16            `json:"cols,omitempty"`
	Rows    uint16            `json:"rows,omitempty"`
}

// PTYInfo describes a PTY.
type PTYInfo struct {
	ID        string    `json:"id"`
	Command   []string  `json:"command"`
	Dir       string    `json:"dir"`
	Running   bool      `json:"running"`
	ExitCode  int       `json:"exitCode"`
	Signal    string    `json:"signal,omitempty"`
	Cols      uint16    `json:"cols"`
	Rows      uint16    `json:"rows"`
	Attached  int       `json:"attached"`
	StartedAt time.Time `json:"startedAt"`
	ExitedAt  time.Time `json:"exitedAt"`
}

// PTYListResult answers MethodPTYList.
type PTYListResult struct {
	PTYs []PTYInfo `json:"ptys"`
}

// PTYIDParams names a PTY.
type PTYIDParams struct {
	ID string `json:"id"`
}

// PTYAttachResult answers MethodPTYAttach.
type PTYAttachResult struct {
	Info        PTYInfo `json:"info"`
	ReplayBytes int     `json:"replayBytes"`
}

// PTYResizeParams resizes a PTY.
type PTYResizeParams struct {
	ID   string `json:"id"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// PTYSignalParams signals a PTY's foreground process group.
type PTYSignalParams struct {
	ID     string `json:"id"`
	Signal string `json:"signal"`
}

// PTYControl is a JSON frame sent by the client on an attach stream:
// {"type":"resize","cols":..,"rows":..} or {"type":"signal","signal":"SIGINT"}.
type PTYControl struct {
	Type   string `json:"type"`
	Cols   uint16 `json:"cols,omitempty"`
	Rows   uint16 `json:"rows,omitempty"`
	Signal string `json:"signal,omitempty"`
}

// PTYEvent is a JSON frame sent by the guest on an attach stream:
// {"type":"exit","code":0,"signal":""} or {"type":"resized","cols":..,"rows":..}.
type PTYEvent struct {
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Signal  string `json:"signal,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
	Message string `json:"message,omitempty"`
}

// PTYExit answers MethodPTYWait.
type PTYExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}

// ToolCallParams relays an MCP tools/call.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Desktop modes (docs/api.md section 9).
const (
	ModeObserver = "OBSERVER"
	ModeTakeover = "TAKEOVER"
)

// ModeState is the desktop control mode.
type ModeState struct {
	Mode      string    `json:"mode"`
	ChangedAt time.Time `json:"changedAt"`
	ChangedBy string    `json:"changedBy"`
}

// ModeSetParams changes the mode. By is user, agent, or system.
type ModeSetParams struct {
	Mode string `json:"mode"`
	By   string `json:"by"`
}

// LaunchParams launches a desktop application: chrome, blender, or terminal.
type LaunchParams struct {
	App string `json:"app"`
}

// SignalParams opens a viewer. PublicIP, when set, is the address at which
// the browser reaches the WebRTC UDP port (the host the browser connected
// to); the guest advertises it as its ICE host candidate.
type SignalParams struct {
	PublicIP string `json:"publicIp,omitempty"`
}

// Screen is the desktop size in pixels.
type Screen struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// SignalHello answers MethodDesktopSignal.
type SignalHello struct {
	Mode   string `json:"mode"`
	Screen Screen `json:"screen"`
}
