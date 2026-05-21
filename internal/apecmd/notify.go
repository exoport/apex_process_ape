package apecmd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
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
			runNotify(event, os.Stdin, os.Getenv("APE_BRIDGE_PORT"))
			return nil
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "Hook event name (PreToolUse, PostToolUse, etc.).")
	return cmd
}

// runNotify is the testable core. Reads up to 1 MB from stdin (hook
// envelopes are small) and forwards. All error paths drop silently —
// the hook loop must never stall on bridge unavailability.
func runNotify(event string, stdin io.Reader, port string) {
	if port == "" || event == "" {
		return
	}
	envelope, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
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
