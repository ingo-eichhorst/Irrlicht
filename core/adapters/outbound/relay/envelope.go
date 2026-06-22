// Package relay implements the daemon's outbound relay forwarder and the
// shared wire envelope spoken between irrlichd, the standalone irrlichtrelay
// server, and relay-connected clients. The envelope wraps the daemon's
// existing outbound.PushMessage so the load-bearing daemon → relay link
// reuses the same payloads the local WebSocket already serves; clients read
// Push.Msg and process it exactly as a raw daemon frame.
//
// See docs/relay-protocol.md for the on-the-wire documentation. Only the
// frames defined here are built; auth, seq, and resume remain reserved.
package relay

import (
	"encoding/json"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// ProtocolVersion is the relay wire-format version. Bumped only on a breaking
// change to the frames below.
const ProtocolVersion = 1

// Frame type tags carried in every envelope's "type" field.
const (
	MsgHello          = "hello"
	MsgHelloAck       = "hello_ack"
	MsgDaemonSnapshot = "daemon_snapshot"
	MsgSnapshot       = "snapshot"
	MsgDaemonStatus   = "daemon_status"
	MsgPush           = "push"
	// MsgControl is the first client→relay→daemon (upstream) frame in an
	// otherwise one-way protocol (issue #724): a client asks a specific daemon
	// to inject input / interrupt into one of its sessions. The relay routes it
	// to the addressed daemon within the client's own token-derived workspace;
	// the daemon re-gates it through the same consent path as a local request.
	MsgControl = "control"
)

// Control-frame actions (Control.Action).
const (
	ControlActionInput     = "input"
	ControlActionInterrupt = "interrupt"
)

// Peer roles announced in a hello.
const (
	RoleDaemon = "daemon"
	RoleClient = "client"
)

// Daemon connection states reported to clients.
const (
	StatusConnected    = "connected"
	StatusDisconnected = "disconnected"
)

// CloseRevoked is the WebSocket close code the relay sends when a peer's bearer
// token is missing/invalid at handshake, or revoked while connected. Chosen in
// the application-private 4000–4999 range; clients treat it as "auth failed,
// don't keep reconnecting" rather than a transient drop.
const CloseRevoked = 4401

// Hello is the first frame a peer sends after the socket opens. Daemons set
// the Daemon* fields; clients leave them empty. Token carries the bearer token
// when the relay runs with --auth; it is the only auth channel (works from a
// browser, which can't set request headers on a WebSocket) and is omitted on a
// no-auth (trusted-LAN) relay.
type Hello struct {
	Type            string `json:"type"`
	ProtocolVersion int    `json:"protocol_version"`
	Role            string `json:"role"`
	DaemonID        string `json:"daemon_id,omitempty"`
	DaemonLabel     string `json:"daemon_label,omitempty"`
	Token           string `json:"token,omitempty"`
}

// HelloAck is the relay's reply to a hello, echoing the negotiated version.
type HelloAck struct {
	Type            string `json:"type"`
	AcceptedVersion int    `json:"accepted_version"`
}

// DaemonSnapshot reconciles the relay's cache with a daemon's full current
// state. A daemon sends it once, immediately after its hello, then streams
// deltas as Push frames.
type DaemonSnapshot struct {
	Type     string                  `json:"type"`
	Sessions []*session.SessionState `json:"sessions"`
	Agents   []AgentInfo             `json:"agents"`
}

// DaemonInfo identifies a connected daemon in the client-facing snapshot and
// status frames (drives the connection-status tooltip).
type DaemonInfo struct {
	DaemonID    string `json:"daemon_id"`
	DaemonLabel string `json:"daemon_label"`
	Status      string `json:"status"`
}

// Snapshot tells a freshly-connected client which daemons the relay currently
// knows about.
type Snapshot struct {
	Type    string       `json:"type"`
	Daemons []DaemonInfo `json:"daemons"`
}

// DaemonStatus is a live delta: a daemon connected to or disconnected from the
// relay. Keeps the client tooltip current without a full re-snapshot.
type DaemonStatus struct {
	Type        string `json:"type"`
	DaemonID    string `json:"daemon_id"`
	DaemonLabel string `json:"daemon_label"`
	Status      string `json:"status"`
	Since       int64  `json:"since"`
}

// Push wraps one daemon outbound.PushMessage for relay → client fan-out.
// Source is the originating daemon_id; TS is unix seconds. Clients unwrap Msg
// and process it exactly as today's raw daemon frames.
type Push struct {
	Type   string               `json:"type"`
	Source string               `json:"source"`
	TS     int64                `json:"ts"`
	Msg    outbound.PushMessage `json:"msg"`
}

// Control is an upstream request from a client to drive one session on a
// specific daemon (issue #724). TargetDaemon selects the daemon within the
// client's workspace (the relay routes by it, then drops it); the daemon reads
// SessionID/Action/Data and forwards to its local InputService, which re-checks
// the backchannel toggle + per-agent consent + controllability. Action is one
// of the ControlAction* constants; Data carries the bytes for an input action.
type Control struct {
	Type         string `json:"type"`
	TargetDaemon string `json:"target_daemon"`
	SessionID    string `json:"session_id"`
	Action       string `json:"action"`
	Data         string `json:"data,omitempty"`
}

// AgentInfo is the adapter branding the relay re-serves at /api/v1/agents.
// Mirrors the daemon's agentEntry shape byte-for-byte so frontends key off it
// identically whether they fetched it from a daemon or a relay.
type AgentInfo struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	IconSVGLight string `json:"icon_svg_light"`
	IconSVGDark  string `json:"icon_svg_dark"`
}

// FrameType extracts the "type" tag from a raw relay frame without fully
// decoding it, so a reader can dispatch on the frame kind before committing to
// a typed unmarshal. Returns "" when the frame isn't valid JSON or omits type.
func FrameType(data []byte) string {
	var e struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &e)
	return e.Type
}
