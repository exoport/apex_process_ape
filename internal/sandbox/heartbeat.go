package sandbox

import (
	"fmt"
	"strings"
	"time"
)

// The in-guest agent's heartbeat (PLAN-24 D6), and the vocabulary the idle
// reaper (D7) reads it in.
//
// The signal is deliberately NOT `last_used_at`. That records the last exec,
// attach or start — someone reaching IN — so a workspace running a three-hour
// build with nobody attached looks idle, which is the guess PLAN-22 refused to
// ship a reaper on. It is also deliberately not host CPU, which was viable
// (task.Metrics() is right there) and was rejected: a number on the host tells
// you a VM is burning cycles, not what it is doing, so it cannot distinguish a
// build from a busy-looping crash and cannot say so in the log line that stops
// a workspace.
//
// The heartbeat is measured INSIDE the guest and carries both: a busy verdict,
// and the processes that produced it.

// HeartbeatSubjectSuffix is the last token of the heartbeat subject. The full
// subject is ape.metrics.<vm-token>.heartbeat — inside the per-VM credential's
// existing publish grant (ape.metrics.<tok>.>), so the agent needs no new
// authority to send one.
const HeartbeatSubjectSuffix = "heartbeat"

// HeartbeatSubjectRoot is the metrics root heartbeats are published under.
const HeartbeatSubjectRoot = "ape.metrics"

// DefaultHeartbeatInterval is how often the agent samples and publishes.
//
// 30s is chosen against the reaper's default 2h threshold: frequent enough that
// a threshold crossing is accurate to within a rounding error, rare enough that
// an idle workspace costs a message a minute rather than a stream.
const DefaultHeartbeatInterval = 30 * time.Second

// DefaultBusyPercent is the guest CPU utilisation at or above which a workspace
// counts as WORKING.
//
// 5% is above the noise floor of an idle Linux guest (timers, the agent's own
// sampling) and far below anything a real build, test run, or agent session
// produces. It is a threshold on a continuous quantity, so the raw CPUPercent
// travels with the verdict — a reader who disagrees can re-derive it.
const DefaultBusyPercent = 5.0

// Heartbeat is one liveness sample from inside a workspace.
//
//nolint:tagliatelle // snake_case matches the ape.metrics.* payload convention
type Heartbeat struct {
	V int `json:"v"`
	// Workspace is the VM token the agent published under (vm-<slug>), taken from
	// its own credential rather than from anything it was told — so a heartbeat
	// cannot claim to be another workspace's even if the guest is compromised.
	// The server enforces the same thing at the subject level.
	Workspace string `json:"workspace"`
	// TS is when the sample was taken, RFC3339.
	TS string `json:"ts"`
	// UptimeSeconds is how long the AGENT has been running — not the guest. It
	// resets on every stop/start, which is exactly the boundary the reaper cares
	// about.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Busy is the verdict: was this workspace doing work over the sample window?
	Busy bool `json:"busy"`
	// CPUPercent is guest CPU utilisation over the window, 0..100 (across all
	// vCPUs, so a 4-vCPU guest with one saturated core reads ~25).
	CPUPercent float64 `json:"cpu_percent"`
	// Load1 is the guest's 1-minute load average.
	Load1 float64 `json:"load1"`
	// Top names the processes that consumed the most CPU over the window,
	// busiest first. This is the "what is running" the host-CPU signal could not
	// give, and it is what a stop decision gets logged with.
	Top []string `json:"top,omitempty"`
	// Agent is the `ape` version the agent is running, so a heartbeat identifies
	// the build that produced it.
	Agent string `json:"agent,omitempty"`
}

// HeartbeatSubject renders the subject a VM token's heartbeats are published on.
func HeartbeatSubject(vmToken string) string {
	return HeartbeatSubjectRoot + "." + vmToken + "." + HeartbeatSubjectSuffix
}

// vmInboxRoot is the scoped reply-inbox root a per-VM credential uses in place
// of the default _INBOX. A distinct top-level token (not "_INBOX.<vm>") so a
// deny on the default "_INBOX.>" cannot reach it and one VM cannot name
// another's inbox.
const vmInboxRoot = "_INBOX_vm"

// VMInboxPrefix returns the reply-inbox prefix for a per-VM credential, given
// its VM token (vm-<slug>).
//
// It lives HERE, in the package both ends already import, because it is a value
// two processes must agree on exactly: the daemon puts it in the credential's
// subscribe grant, and the in-guest agent hands it to nats.CustomInboxPrefix. A
// second definition on either side is a mismatch that only shows up as a
// permissions error on the first request/reply — which the agent does not
// currently make, so it would sit undetected until something added one.
func VMInboxPrefix(vmToken string) string { return vmInboxRoot + "-" + vmToken }

// WorkspaceFromHeartbeatSubject extracts the VM token from a heartbeat subject,
// reporting false for anything that is not one.
//
// The reaper reads the token from the SUBJECT rather than from the payload's
// Workspace field, because the subject is what the NATS server authorized: a
// per-VM credential may only publish under its own token, so the subject is
// attested and the payload is a claim.
func WorkspaceFromHeartbeatSubject(subject string) (token string, ok bool) {
	parts := strings.Split(subject, ".")
	// ape . metrics . <token> . heartbeat
	if len(parts) != 4 || parts[0] != "ape" || parts[1] != "metrics" || parts[3] != HeartbeatSubjectSuffix {
		return "", false
	}
	if parts[2] == "" {
		return "", false
	}
	return parts[2], true
}

// Summary renders the one-line evidence a reaper logs when it acts on a
// heartbeat — what was running and how hard, not just a timestamp.
func (h Heartbeat) Summary() string {
	state := "idle"
	if h.Busy {
		state = "busy"
	}
	s := fmt.Sprintf("%s at %.1f%% cpu, load %.2f", state, h.CPUPercent, h.Load1)
	if len(h.Top) > 0 {
		s += " (" + strings.Join(h.Top, ", ") + ")"
	}
	return s
}
