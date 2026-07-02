package apecmd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
	"github.com/diegosz/apex_process_ape/internal/runlog"
	"github.com/spf13/cobra"
)

// newNotifyCmd implements `ape notify --event <EventName>`. Wired into
// Claude Code's hooks block by BuildSettings (web mode only). Reads
// the hook envelope from stdin, dials 127.0.0.1:$APE_BRIDGE_PORT, and
// NDJSON-forwards a TypeHook frame to the parent. PLAN-5 / C4.
//
// All failure modes exit 0 so the tool loop never stalls on a missing
// or unreachable bridge (the durable record is the JSONL stream under
// <run-dir>/hook-events.jsonl, which the parent populates on its end).
func newNotifyCmd() *cobra.Command {
	var event string
	cmd := &cobra.Command{
		Use:    "notify",
		Short:  "(internal) Forward a Claude Code hook envelope to the bridge.",
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			runNotify(event, os.Stdin, os.Getenv("APE_BRIDGE_PORT"), os.Getenv("APE_SNAPSHOT_DIR"))
			return nil
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "Hook event name (PreToolUse, PostToolUse, etc.).")
	return cmd
}

// runNotify is the testable core. Reads up to 1 MB from stdin (hook
// envelopes are small), captures the session transcript hook-side
// (see snapshotFromHook), and forwards the envelope to the bridge.
// All bridge error paths drop silently — the hook loop must never
// stall on bridge unavailability.
func runNotify(event string, stdin io.Reader, port, snapshotDir string) {
	if event == "" {
		return
	}
	envelope, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return
	}
	// Capture BEFORE dialing: the snapshot is the durable record and
	// must land even when the bridge is unreachable. This runs in
	// claude's turn — the only context where the transcript is
	// guaranteed resident (v0.0.32; five prior parent-side attempts
	// raced claude's turn-end deletion).
	snapshotFromHook(event, envelope, snapshotDir)
	if port == "" {
		return
	}
	sessionID, agentID := extractIDs(envelope)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer dialCancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", "127.0.0.1:"+port)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_ = ipc.Write(conn, ipc.Message{
		Type:      ipc.TypeHook,
		Event:     event,
		SessionID: sessionID,
		AgentID:   agentID,
		Payload:   json.RawMessage(envelope),
	})
}

// snapshotFromHook captures the session transcript into snapshotDir,
// keyed by session_id — executed HOOK-SIDE, inside `ape notify`, while
// claude blocks on the hook and the transcript file is live.
//
//   - Stop / SubagentStop (claude blocks until the sync Stop hook
//     returns): a FULL atomic copy — guaranteed complete, taken before
//     claude proceeds to turn-end deletion. The primary guarantee.
//   - Other events (UPS, Pre/PostToolUse, SubagentStart): incremental
//     append anchored on the destination's current size, so large
//     transcripts aren't re-copied per tool call and the accumulated
//     copy survives mid-turn source deletion. Concurrent async hooks
//     can in principle interleave appends; the Stop-time full copy
//     rewrites the file atomically, so the post-Stop scan always sees
//     a clean artifact.
//
// Best-effort by design: any failure leaves the parent-side capture
// (FeedHook) and diagnostics to report it. Never blocks the hook loop.
func snapshotFromHook(event string, envelope []byte, snapshotDir string) {
	if snapshotDir == "" {
		return
	}
	var v struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
	}
	if json.Unmarshal(envelope, &v) != nil || v.TranscriptPath == "" {
		return
	}
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		return
	}
	dst := filepath.Join(snapshotDir, hookSnapshotFileName(v.SessionID, v.TranscriptPath))
	switch event {
	case ipc.HookStop, ipc.HookSubagentStop:
		_ = runlog.CopyFileAtomic(dst, v.TranscriptPath)
	default:
		off := int64(0)
		if info, err := os.Stat(dst); err == nil {
			off = info.Size()
		}
		_, _ = runlog.AppendFile(dst, v.TranscriptPath, off)
	}
}

// hookSnapshotFileName is the snapshot filename for one claude
// session: "<session-id>.jsonl" (sanitized), falling back to the
// source basename when the envelope carries no session_id. Shared by
// the hook-side writer (snapshotFromHook) and the parent-side reader
// (StepTelemetry).
func hookSnapshotFileName(sessionID, transcriptPath string) string {
	name := sessionID
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	}
	b := make([]byte, 0, len(name))
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z',
			c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		b = []byte("session")
	}
	return string(b) + ".jsonl"
}

// extractIDs pulls session_id + agent_id out of the hook envelope JSON.
// Claude Code envelopes carry `session_id` at the top level and
// `agent_id` for Subagent* events; both are optional. We tolerate
// malformed JSON by returning empty strings — the parent's hook→step
// router handles null session_ids by tagging events `"step":null`.
// PLAN-5 / C4.
func extractIDs(envelope []byte) (sessionID, agentID string) {
	var v struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
	}
	_ = json.Unmarshal(envelope, &v)
	return v.SessionID, v.AgentID
}
