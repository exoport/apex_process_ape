// Package vmmstream is the interactive exec/attach transport scaffold for the
// ape.vmm control plane (PLAN-18 D2). Bulk stdio must NOT ride NATS
// request/reply — core NATS drops a slow consumer by closing its connection — so
// an exec session gets its own per-channel subjects
// (ape.vmm.<node>.exec.<sid>.{stdin,stdout,stderr,resize,control,exit}) with
// ≤32 KiB data frames and credit-based flow control.
//
// This package provides the framing + flow-control primitives and a Sender/
// Receiver pair over NATS, all Tier-1 testable against a loopback server.
// Binding the server end to a real containerd task PTY is the live-validated
// follow-on (entangled with the exec-through-daemon gap — PLAN-18 Risks).
package vmmstream

import (
	"encoding/json"
	"fmt"
)

// MaxFrameData bounds a single data frame at 32 KiB (PLAN-18 D2) — well under the
// NATS max payload, so a fast producer is chunked, not dropped.
const MaxFrameData = 32 * 1024

// Channel is a per-session stream sub-subject.
type Channel string

const (
	ChannelStdin   Channel = "stdin"
	ChannelStdout  Channel = "stdout"
	ChannelStderr  Channel = "stderr"
	ChannelResize  Channel = "resize"
	ChannelControl Channel = "control"
	ChannelExit    Channel = "exit"
)

// SessionSubject renders ape.vmm.<node>.exec.<sid>.<channel>, matching the frozen
// contract (docs/reference/events.md). node and sid are assumed already slugged.
func SessionSubject(node, sid string, ch Channel) string {
	return fmt.Sprintf("ape.vmm.%s.exec.%s.%s", node, sid, ch)
}

// ChannelSubject renders <prefix>.<channel>, where prefix is the AttachOpenReply
// SubjectPrefix (ape.vmm.<node>.exec.<sid>). It is the client-side companion to
// SessionSubject: a client that only holds the opaque prefix appends the channel
// with this helper rather than re-deriving node+sid.
func ChannelSubject(prefix string, ch Channel) string {
	return prefix + "." + string(ch)
}

// Control frame types (the JSON `type` on the control/resize/exit channels).
const (
	ControlCredit = "credit" // grant N more data frames to the peer (flow control)
	ControlResize = "resize" // terminal size change
	ControlExit   = "exit"   // final process exit status
	ControlPing   = "ping"   // client keepalive — feeds the server's idle watchdog
)

// ControlFrame is the JSON payload on the control/resize/exit channels: credit
// grants, terminal resizes, and the exit code. Data channels (stdin/stdout/
// stderr) carry raw bytes, never a ControlFrame.
//
// A full interactive session multiplexes three independent data streams
// (stdin/stdout/stderr), but the frozen contract gives it a single reverse-
// direction control channel. Ch tags a credit grant with the data channel it
// refills so both peers can publish/subscribe the one shared control subject and
// route grants to the right stream (Sender filters on it). Ch is unset on resize/
// exit frames (their subject already identifies them).
type ControlFrame struct {
	Type   string  `json:"type"`
	Ch     Channel `json:"ch,omitempty"`
	Credit int     `json:"credit,omitempty"`
	Cols   uint16  `json:"cols,omitempty"`
	Rows   uint16  `json:"rows,omitempty"`
	Code   int     `json:"code,omitempty"`
}

// Encode marshals a control frame to JSON.
func (f ControlFrame) Encode() ([]byte, error) { return json.Marshal(f) }

// DecodeControl parses a control-channel payload.
func DecodeControl(b []byte) (ControlFrame, error) {
	var f ControlFrame
	err := json.Unmarshal(b, &f)
	return f, err
}

// Chunks splits p into ≤MaxFrameData views (no copy). An empty p yields no
// chunks — the caller frames end-of-stream separately (a zero-length data
// message is the EOF sentinel; see Sender.CloseSend).
func Chunks(p []byte) [][]byte {
	if len(p) == 0 {
		return nil
	}
	out := make([][]byte, 0, (len(p)+MaxFrameData-1)/MaxFrameData)
	for len(p) > MaxFrameData {
		out = append(out, p[:MaxFrameData])
		p = p[MaxFrameData:]
	}
	return append(out, p)
}
