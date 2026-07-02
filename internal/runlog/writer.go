// Package runlog writes the four PLAN-5 / C6 streams that surround
// the pipeline manifest:
//
//	hook-events.jsonl    one JSON per ape-notify forward
//	bridge-calls.jsonl   one JSON per MCP tool call seen by the bridge
//	checkpoints.jsonl    ape stage events + skill reply() calls
//	transcripts/         symlinks into ~/.claude/projects/<hash>/<sid>.jsonl
//
// Pipeline runs use the existing PLAN-3 layout
// (<project>/_output/pipelines/<name>/<run_id>/) — runlog does not
// move the directory, it adds files alongside manifest.yaml.
//
// `ape chat` writes to a separate convention
// (<project>/_output/ape/chats/<chat-id>/) with session.yaml in place
// of the PLAN-3 manifest.
package runlog

import (
	"crypto/sha1" //nolint:gosec // sha1 is used here for a short identifier hash, not for cryptographic security
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Writer is one open run-dir. Methods are safe for concurrent use.
type Writer struct {
	dir string

	hooksMu sync.Mutex
	hooks   *os.File

	callsMu sync.Mutex
	calls   *os.File

	chkMu sync.Mutex
	chk   *os.File
}

// New opens (or creates) the four streams under dir. Fails loud if
// the dir already exists and contains a non-empty manifest — that is
// the PLAN-5 / C6 run-id collision contract.
func New(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("runlog: mkdir %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "transcripts"), 0o755); err != nil {
		return nil, fmt.Errorf("runlog: mkdir transcripts: %w", err)
	}
	w := &Writer{dir: dir}
	var err error
	if w.hooks, err = openAppend(filepath.Join(dir, "hook-events.jsonl")); err != nil {
		return nil, err
	}
	if w.calls, err = openAppend(filepath.Join(dir, "bridge-calls.jsonl")); err != nil {
		return nil, err
	}
	if w.chk, err = openAppend(filepath.Join(dir, "checkpoints.jsonl")); err != nil {
		return nil, err
	}
	return w, nil
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// Dir returns the run-dir path.
func (w *Writer) Dir() string { return w.dir }

// Close flushes and closes every stream. Safe to call multiple times.
func (w *Writer) Close() error {
	var first error
	for _, c := range []io.Closer{w.hooks, w.calls, w.chk} {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	w.hooks, w.calls, w.chk = nil, nil, nil
	return first
}

// Hook writes one hook-events.jsonl entry. ts defaults to time.Now().UTC() if zero.
//
// Schema (PLAN-5 / C6): {"ts","event","step","session_id","agent_id","payload"}.
// `step` is null for events whose session_id is not yet bound.
func (w *Writer) Hook(entry HookEntry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	w.hooksMu.Lock()
	defer w.hooksMu.Unlock()
	if w.hooks == nil {
		return errors.New("runlog: writer closed")
	}
	return writeJSONLine(w.hooks, hookOnWire(entry))
}

// Call writes one bridge-calls.jsonl entry. Captures every MCP tool
// call seen at the bridge stdio layer (including tools/list, ping,
// initialize, and await_message's deferred-entry + flush pair).
func (w *Writer) Call(entry CallEntry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	w.callsMu.Lock()
	defer w.callsMu.Unlock()
	if w.calls == nil {
		return errors.New("runlog: writer closed")
	}
	return writeJSONLine(w.calls, callOnWire(entry))
}

// CheckpointKindStep is the pipeline.RunLogger adapter shape:
// (kind, step, payload, at) → CheckpointEntry. Defined here so the
// runlog package owns its own wire shape; pipeline calls this via
// its narrow RunLogger interface to avoid a runlog import.
func (w *Writer) CheckpointKindStep(kind, step string, payload any, at time.Time) {
	_ = w.Checkpoint(CheckpointEntry{Timestamp: at, Kind: kind, Step: step, Payload: payload})
}

// Checkpoint writes one checkpoints.jsonl entry. Kinds: stage-start,
// stage-end, commit-made, pipeline-end, reply, stopped. PLAN-5 / C6.
func (w *Writer) Checkpoint(entry CheckpointEntry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	w.chkMu.Lock()
	defer w.chkMu.Unlock()
	if w.chk == nil {
		return errors.New("runlog: writer closed")
	}
	return writeJSONLine(w.chk, checkpointOnWire(entry))
}

// LinkTranscript creates the symlink <dir>/transcripts/<name> → target.
// Best-effort: returns nil if the link already exists at the same
// target, errors otherwise so the caller can decide. PLAN-5 / C6 —
// symlinks (not copies) keep the canonical ~/.claude/projects/ path
// authoritative.
func (w *Writer) LinkTranscript(name, target string) error {
	link := filepath.Join(w.dir, "transcripts", name)
	existing, err := os.Readlink(link)
	if err == nil {
		if existing == target {
			return nil
		}
		return fmt.Errorf("runlog: transcript symlink %s points to %s, want %s", link, existing, target)
	}
	// errors.Is(err, os.ErrNotExist) is the happy "no symlink yet"
	// branch; os.ErrInvalid is reported by some platforms for
	// non-symlink targets. Either way, fall through to create.
	return os.Symlink(target, link)
}

// SnapshotTranscript copies src's bytes to <dir>/transcripts/<name>
// and returns the destination path. Unlike LinkTranscript, the result
// survives the source session file being rotated or removed — claude
// session .jsonl files under ~/.claude/projects/ have been observed
// gone by post-Stop scan time, which zeroed interactive telemetry.
// Idempotent: repeat calls refresh the copy (the session file is
// append-only, so a later copy strictly supersedes an earlier one).
// Replaces a pre-existing symlink of the same name.
func (w *Writer) SnapshotTranscript(name, src string) (string, error) {
	dst := filepath.Join(w.dir, "transcripts", name)
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("runlog: snapshot transcript %s: %w", name, err)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("runlog: snapshot transcript %s: %w", name, err)
	}
	// A symlink from an earlier LinkTranscript-era run dir would make
	// os.Rename land the copy at the link target; remove it first.
	_ = os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return "", fmt.Errorf("runlog: snapshot transcript %s: %w", name, err)
	}
	return dst, nil
}

// AppendTranscript incrementally copies the bytes of src beyond
// fromOffset onto <dir>/transcripts/<name>, returning the destination
// path and the new byte offset (bytes copied so far). Because the
// snapshot ACCUMULATES across calls, it survives the source being
// deleted mid-turn — which is why interactive telemetry must snapshot
// on Pre/PostToolUse hooks (the only window the session file reliably
// exists) rather than only at Stop.
//
// fromOffset == 0 (re)creates the snapshot from scratch. When src is
// shorter than fromOffset (truncated or rotated) it also rewrites from
// scratch. Otherwise it appends exactly src[fromOffset:EOF]; the copy
// is bounded by the live EOF, so the returned offset never overshoots
// and successive calls neither gap nor overlap. Claude session JSONL
// is append-only, so this reconstructs the byte stream faithfully; a
// final partial line (source deleted mid-write) is skipped by the
// JSONL scanner.
func (w *Writer) AppendTranscript(name, src string, fromOffset int64) (dst string, newOffset int64, err error) {
	dst = filepath.Join(w.dir, "transcripts", name)
	sf, err := os.Open(src)
	if err != nil {
		return dst, fromOffset, fmt.Errorf("runlog: append transcript %s: %w", name, err)
	}
	defer sf.Close()
	info, err := sf.Stat()
	if err != nil {
		return dst, fromOffset, fmt.Errorf("runlog: append transcript %s: %w", name, err)
	}

	full := fromOffset <= 0 || info.Size() < fromOffset
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	start := fromOffset
	if full {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		start = 0
		// A leftover symlink (legacy LinkTranscript) would redirect the
		// open onto the link target; drop it so we write a real file.
		_ = os.Remove(dst)
	}
	df, err := os.OpenFile(dst, flags, 0o600)
	if err != nil {
		return dst, fromOffset, fmt.Errorf("runlog: append transcript %s: %w", name, err)
	}
	defer df.Close()
	if start > 0 {
		if _, err := sf.Seek(start, io.SeekStart); err != nil {
			return dst, fromOffset, fmt.Errorf("runlog: append transcript %s: seek: %w", name, err)
		}
	}
	n, err := io.Copy(df, sf)
	if err != nil {
		return dst, start + n, fmt.Errorf("runlog: append transcript %s: copy: %w", name, err)
	}
	return dst, start + n, nil
}

// HookEntry is the typed input to Writer.Hook. The on-wire shape is
// stable but assembled by hookOnWire to keep nil pointers from
// producing `null` for fields PLAN-5 specifies as empty strings.
type HookEntry struct {
	Timestamp time.Time
	Event     string
	Step      string // empty → "step":null on the wire
	SessionID string
	AgentID   string
	Payload   json.RawMessage
}

func hookOnWire(e HookEntry) map[string]any {
	out := map[string]any{
		"ts":         e.Timestamp.Format(time.RFC3339Nano),
		"event":      e.Event,
		"session_id": e.SessionID,
		"agent_id":   e.AgentID,
	}
	if e.Step == "" {
		out["step"] = nil
	} else {
		out["step"] = e.Step
	}
	if len(e.Payload) > 0 {
		out["payload"] = e.Payload
	} else {
		out["payload"] = nil
	}
	return out
}

// CallEntry mirrors an MCP tool call.
type CallEntry struct {
	Timestamp time.Time
	Method    string // "tools/call", "tools/list", "ping", "initialize"
	Tool      string
	Params    json.RawMessage
	Result    json.RawMessage
	SessionID string
	ID        string
}

func callOnWire(e CallEntry) map[string]any {
	out := map[string]any{
		"ts":     e.Timestamp.Format(time.RFC3339Nano),
		"method": e.Method,
		"tool":   e.Tool,
	}
	if e.SessionID != "" {
		out["session_id"] = e.SessionID
	}
	if e.ID != "" {
		out["id"] = e.ID
	}
	if len(e.Params) > 0 {
		out["params"] = e.Params
	}
	if len(e.Result) > 0 {
		out["result"] = e.Result
	}
	return out
}

// CheckpointEntry is the typed input to Writer.Checkpoint.
type CheckpointEntry struct {
	Timestamp time.Time
	Kind      string // stage-start, stage-end, commit-made, pipeline-end, reply, stopped, chat-start, chat-end
	Step      string
	Payload   any
}

func checkpointOnWire(e CheckpointEntry) map[string]any {
	out := map[string]any{
		"ts":   e.Timestamp.Format(time.RFC3339Nano),
		"kind": e.Kind,
	}
	if e.Step != "" {
		out["step"] = e.Step
	}
	if e.Payload != nil {
		out["payload"] = e.Payload
	}
	return out
}

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// --- Run-id / chat-id helpers ---

// PipelineRunDir returns <project>/_output/pipelines/<name>/<run_id>/.
// PLAN-3's path, extended in place by PLAN-5.
func PipelineRunDir(projectRoot, pipelineName, runID string) string {
	return filepath.Join(projectRoot, "_output", "pipelines", pipelineName, runID)
}

// ChatDir returns <project>/_output/ape/chats/<chat-id>/. PLAN-5 / C6.
func ChatDir(projectRoot, chatID string) string {
	return filepath.Join(projectRoot, "_output", "ape", "chats", chatID)
}

// NewChatID generates a chat-id of the shape YYYYMMDD-HHMMSS-<7-char hash>.
// Hash mixes timestamp + cwd + pid for cross-process uniqueness. PLAN-5 / C6.
func NewChatID(now time.Time, cwd string, pid int) string {
	h := sha1.New() //nolint:gosec // see import comment — identifier hash, not cryptographic
	h.Write([]byte(now.Format(time.RFC3339Nano)))
	h.Write([]byte{0})
	h.Write([]byte(cwd))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(pid)))
	hash := hex.EncodeToString(h.Sum(nil))[:7]
	return now.UTC().Format("20060102-150405") + "-" + hash
}

// EnsureNoCollision returns an error if dir already exists. PLAN-5 / C6:
// "fail loud" — no auto-disambiguate, no overwrite.
func EnsureNoCollision(dir string) error {
	_, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("run id %s already exists at %s; investigate or remove", filepath.Base(dir), dir)
}
