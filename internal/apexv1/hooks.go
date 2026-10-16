package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// HookPayload is the subset of Claude Code's hook input ape reads.
type HookPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	StopHookActive bool            `json:"stop_hook_active"`
	AgentID        string          `json:"agent_id"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
}

// ParseHookPayload decodes a hook's stdin. Empty input is an empty payload.
func ParseHookPayload(data []byte) (HookPayload, error) {
	var p HookPayload
	if strings.TrimSpace(string(data)) == "" {
		return p, nil
	}
	err := json.Unmarshal(data, &p)
	return p, err
}

// SessionStartOutput is the SessionStart hook's JSON answer.
func SessionStartOutput(text string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	})
}

// --- pre-tool ---------------------------------------------------------------

// editTools are the tools the pre-tool hook guards.
var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// PreToolDecision is the pre-tool hook's verdict; Deny false allows.
type PreToolDecision struct {
	Deny   bool
	Path   string
	Reason string
}

// Output renders the decision as PreToolUse hook JSON; nil when allowed.
func (d PreToolDecision) Output() ([]byte, error) {
	if !d.Deny {
		return nil, nil
	}
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": d.Reason,
		},
	})
}

// PreTool decides whether an edit tool call may touch its target.
func PreTool(payload HookPayload) PreToolDecision {
	if !editTools[payload.ToolName] {
		return PreToolDecision{}
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	_ = json.Unmarshal(payload.ToolInput, &in)
	target := in.FilePath
	if target == "" {
		target = in.NotebookPath
	}
	if target == "" {
		return PreToolDecision{}
	}
	cwd := payload.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(cwd, target)
	}
	target = filepath.Clean(target)
	p, err := Open(FindRoot(cwd))
	if err != nil || !p.IsV1() {
		return PreToolDecision{}
	}
	if reason := p.protectedReason(target); reason != "" {
		return PreToolDecision{Deny: true, Path: p.Rel(target), Reason: reason}
	}
	if id := os.Getenv(EnvReview); id != "" {
		if reason := p.reviewConfinement(id, target); reason != "" {
			return PreToolDecision{Deny: true, Path: p.Rel(target), Reason: reason}
		}
	}
	return PreToolDecision{}
}

// reviewConfinement is the read-only rule of a review session (D18):
// inside the project, only the change's review.md may be written. Outside
// it — a throwaway copy the reviewer breaks to prove a check would fail —
// anything goes.
func (p *Project) reviewConfinement(id, abs string) string {
	root := p.Root
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(resolved, filepath.Base(abs))
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if filepath.ToSlash(rel) == p.DevRel+"/changes/"+id+"/review.md" {
		return ""
	}
	return "this is a read-only review session for change " + id + ": the only file it may write in the project is " +
		p.DevRel + "/changes/" + id + "/review.md (break code in a throwaway copy outside the repository instead)"
}

var stateJSONRe = regexp.MustCompile(`^changes/[^/]+/state\.json$`)

// protectedReason names why ape alone may write path, or "" when a model
// may edit it.
func (p *Project) protectedReason(abs string) string {
	dev := p.Dev()
	// Resolve symlinked parents so a link into specs/ is still specs/.
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		if resolvedDev, err := filepath.EvalSymlinks(dev); err == nil {
			dev = resolvedDev
		}
		abs = filepath.Join(resolved, filepath.Base(abs))
	}
	rel, err := filepath.Rel(dev, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	rel = filepath.ToSlash(rel)
	devRel := p.DevRel
	switch {
	case rel == "specs" || strings.HasPrefix(rel, "specs/"):
		return devRel + "/" + rel + " is a living spec: specs change only through `ape change archive` — put the requirement in your change's ## Delta (ADDED/MODIFIED/REMOVED)"
	case rel == "changes/archive" || strings.HasPrefix(rel, "changes/archive/"):
		return devRel + "/" + rel + " is an archived change: the archive is written only by `ape change archive` — specs change only through ape change archive"
	case stateJSONRe.MatchString(rel):
		return devRel + "/" + rel + " is written only by ape (`ape change start|verify|review|accept|block|archive`) — specs change only through ape change archive"
	case rel == ".apex" || strings.HasPrefix(rel, ".apex/"):
		return devRel + "/" + rel + " is ape's own record (gate runs, logs) — use the ape command that records it; specs change only through ape change archive"
	}
	return ""
}

// --- stop -------------------------------------------------------------------

// MaxConsecutiveStopBlocks is how many times in a row the stop hook blocks
// before it lets a session stop anyway.
const MaxConsecutiveStopBlocks = 3

// Stop-gate verdicts.
const (
	StopPending = "pending"
	StopAllow   = "allow"
	StopBlock   = "block"
)

// StopRecord is development/.apex/stop-gate/<key>.json: the consecutive
// block count and the latest verdict. `ape task` reads the main session's
// record to learn whether a Stop it saw actually ended the turn.
type StopRecord struct {
	SessionID         string `json:"session_id"`
	AgentID           string `json:"agent_id,omitempty"`
	Event             string `json:"event"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at,omitempty"`
	Verdict           string `json:"verdict"`
	Change            string `json:"change,omitempty"`
	ConsecutiveBlocks int    `json:"consecutive_blocks"`
	Reason            string `json:"reason,omitempty"`
}

// StopDecision is the stop hook's verdict.
type StopDecision struct {
	Block  bool
	Reason string
	Record StopRecord
}

// Output renders the decision as Stop/SubagentStop hook JSON; nil allows.
func (d StopDecision) Output() ([]byte, error) {
	if !d.Block {
		return nil, nil
	}
	return json.Marshal(map[string]any{"decision": "block", "reason": d.Reason})
}

func (p *Project) stopRecordPath(sessionID, agentID string) string {
	key := sanitizeKey(sessionID)
	if key == "" {
		key = "no-session"
	}
	if agentID != "" {
		key += "-" + sanitizeKey(agentID)
	}
	return filepath.Join(p.StopGateDir(), key+".json")
}

// stopLogPath is the main session's append-only verdict log.
func (p *Project) stopLogPath(sessionID string) string {
	return strings.TrimSuffix(p.stopRecordPath(sessionID, ""), ".json") + ".jsonl"
}

// appendStopLog appends one record as a JSON line. A line is far below
// PIPE_BUF-sized writes, so concurrent appends do not interleave.
func (p *Project) appendStopLog(path string, r StopRecord) {
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}

// readStopLog folds the log into one record per hook run (keyed by
// StartedAt), in the order the runs started; a later line for the same
// run replaces its pending line.
func (p *Project) readStopLog(path string) []StopRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []StopRecord
	index := map[string]int{}
	for line := range strings.SplitSeq(string(data), "\n") {
		var r StopRecord
		if line == "" || json.Unmarshal([]byte(line), &r) != nil || r.StartedAt == "" {
			continue
		}
		if i, ok := index[r.StartedAt]; ok {
			out[i] = r
			continue
		}
		index[r.StartedAt] = len(out)
		out = append(out, r)
	}
	return out
}

var keyRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func sanitizeKey(s string) string { return keyRe.ReplaceAllString(s, "_") }

func (p *Project) loadStopRecord(path string) StopRecord {
	var r StopRecord
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

func (p *Project) saveStopRecord(path string, r StopRecord) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(path, string(data)+"\n")
}

// StopHook decides a Stop or SubagentStop. It blocks when exactly one
// change is in-progress and one of its command checks is red, at most
// MaxConsecutiveStopBlocks times in a row (the count resets whenever
// Claude Code reports the stop is not itself a continuation from a block).
func StopHook(ctx context.Context, payload HookPayload, opt RunOptions) StopDecision {
	cwd := payload.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	p, err := Open(FindRoot(cwd))
	if err != nil || !p.IsV1() {
		return StopDecision{}
	}
	event := payload.HookEventName
	if event == "" {
		event = "Stop"
	}
	agent := ""
	if event == "SubagentStop" {
		agent = payload.AgentID
		if agent == "" {
			agent = "subagent"
		}
	}
	path := p.stopRecordPath(payload.SessionID, agent)
	prev := p.loadStopRecord(path)
	rec := StopRecord{
		SessionID: payload.SessionID, AgentID: payload.AgentID, Event: event,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Verdict: StopPending,
		ConsecutiveBlocks: prev.ConsecutiveBlocks,
	}
	if !payload.StopHookActive {
		rec.ConsecutiveBlocks = 0
	}
	p.saveStopRecord(path, rec)
	// The main session's verdicts are also appended to a log, one line
	// when the hook starts and one when it decides: `ape task` matches its
	// k-th Stop to the k-th verdict, which a single overwritten record
	// cannot support when a blocked session stops again quickly.
	logPath := ""
	if agent == "" {
		logPath = p.stopLogPath(payload.SessionID)
		p.appendStopLog(logPath, rec)
	}
	finish := func(d StopDecision) StopDecision {
		d.Record.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		p.saveStopRecord(path, d.Record)
		if logPath != "" {
			p.appendStopLog(logPath, d.Record)
		}
		return d
	}

	var inProgress []ChangeInfo
	for _, c := range p.Changes() {
		if c.State != nil && c.State.Status == StatusInProgress && c.Doc != nil {
			inProgress = append(inProgress, c)
		}
	}
	if len(inProgress) != 1 {
		rec.Verdict = StopAllow
		rec.ConsecutiveBlocks = 0
		rec.Reason = fmt.Sprintf("%d changes in progress", len(inProgress))
		return finish(StopDecision{Record: rec})
	}
	ci := inProgress[0]
	rec.Change = ci.ID
	if rec.ConsecutiveBlocks >= MaxConsecutiveStopBlocks {
		rec.Verdict = StopAllow
		rec.Reason = fmt.Sprintf("allowed after %d consecutive blocks", rec.ConsecutiveBlocks)
		rec.ConsecutiveBlocks = 0
		return finish(StopDecision{Record: rec})
	}
	results := p.runTasks(ctx, ci.Doc, ci.ID, "stop-", opt)
	var red []CheckResult
	for i := range results {
		if results[i].Exit != 0 {
			red = append(red, results[i])
		}
	}
	if len(red) == 0 {
		rec.Verdict = StopAllow
		rec.ConsecutiveBlocks = 0
		rec.Reason = "all command checks green"
		return finish(StopDecision{Record: rec})
	}
	rec.ConsecutiveBlocks++
	rec.Verdict = StopBlock
	reason := stopReason(ci.ID, red, rec.ConsecutiveBlocks)
	rec.Reason = truncate(reason, 300)
	return finish(StopDecision{Block: true, Reason: reason, Record: rec})
}

func stopReason(id string, red []CheckResult, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Change %s is in progress and %d command check(s) are red (stop blocked %d/%d):\n",
		id, len(red), n, MaxConsecutiveStopBlocks)
	budget := 3000
	for i := range red {
		r := &red[i]
		fmt.Fprintf(&b, "\n%s `%s` exit %d (full output: %s)\n", r.Task, r.Cmd, r.Exit, r.Log)
		tail := lastN(r.Tail, 15)
		for _, l := range tail {
			if budget <= 0 {
				break
			}
			b.WriteString("  " + l + "\n")
			budget -= len(l) + 3
		}
	}
	fmt.Fprintf(&b, "\nMake the checks pass and run `ape change verify %s`. If a task cannot be met, run `ape change block %s --reason \"<why>\"` — never edit a check to make it pass.", id, id)
	return b.String()
}

// --- the ape task side of the stop gate --------------------------------------

// StopGateConfigured reports whether a project's stop hook runs on a
// main Stop: a v1 project whose settings register `ape hook stop`. Every
// such Stop then has a verdict in the session's log — an allow, quickly,
// when no single change is in progress — so `ape task` consumes one
// verdict per Stop and the k-th Stop always meets the k-th verdict.
func StopGateConfigured(root string) bool {
	p, err := Open(FindRoot(root))
	if err != nil || !p.IsV1() {
		return false
	}
	data, err := os.ReadFile(p.SettingsJSON())
	return err == nil && strings.Contains(string(data), HookCmdStop)
}

// ErrNoStopVerdict means no stop-gate record for the Stop appeared in time:
// the hook is not running for this session.
var ErrNoStopVerdict = errors.New("no stop-gate verdict")

// WaitStopVerdict waits for the stop hook's verdict on the main Stop that
// arrived at stopAt, for session sessionID: the first hook run in the
// session's verdict log that is not in consumed and did not start more
// than a few seconds before stopAt. Callers pass every StartedAt they have
// already used, and decide their Stops one at a time, in order, so the
// k-th Stop gets the k-th verdict. It returns ErrNoStopVerdict when no
// run for this Stop appears within appear, and otherwise waits for that
// run's verdict as long as ctx allows.
func WaitStopVerdict(ctx context.Context, root, sessionID string, stopAt time.Time, consumed map[string]bool, appear time.Duration) (StopRecord, error) {
	p, err := Open(FindRoot(root))
	if err != nil {
		return StopRecord{}, err
	}
	path := p.stopLogPath(sessionID)
	const slack = 5 * time.Second
	deadline := time.Now().Add(appear)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		runs := p.readStopLog(path)
		for i := range runs {
			rec := &runs[i]
			started, err := time.Parse(time.RFC3339Nano, rec.StartedAt)
			if err != nil || consumed[rec.StartedAt] || started.Before(stopAt.Add(-slack)) {
				continue
			}
			if rec.Verdict == StopAllow || rec.Verdict == StopBlock {
				return *rec, nil
			}
			deadline = time.Time{} // this Stop's hook is running: wait for it
			break
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return StopRecord{}, ErrNoStopVerdict
		}
		select {
		case <-ctx.Done():
			return StopRecord{}, ctx.Err()
		case <-tick.C:
		}
	}
}
