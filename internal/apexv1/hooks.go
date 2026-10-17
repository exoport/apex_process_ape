package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// PreTool decides whether an edit tool call may touch its target, and
// whether a Bash call may write a git tag (TagGuard), push (PushGuard,
// D45), stop processes by name (KillGuard, D46) or rebase or merge on a
// change branch (RebaseGuard, D40).
func PreTool(payload HookPayload) PreToolDecision {
	if payload.ToolName == "Bash" {
		cwd := payload.Cwd
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		command := bashCommand(payload.ToolInput)
		for _, guard := range []func(command, cwd string) PreToolDecision{TagGuard, PushGuard, KillGuard} {
			if d := guard(command, cwd); d.Deny {
				return d
			}
		}
		return RebaseGuard(command, cwd)
	}
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
	dev, state := p.Dev(), p.StateDir()
	// Resolve symlinked parents so a link into specs/ is still specs/.
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		if resolvedDev, err := filepath.EvalSymlinks(dev); err == nil {
			dev = resolvedDev
		}
		if resolvedRoot, err := filepath.EvalSymlinks(p.Root); err == nil {
			state = filepath.Join(resolvedRoot, filepath.FromSlash(p.StateRel()))
		}
		abs = filepath.Join(resolved, filepath.Base(abs))
	}
	// The hooks and the session environment (where an APE_ALLOW_* override
	// would take effect) are a person's: `ape project init` writes the hooks.
	root := p.Root
	if resolvedRoot, err := filepath.EvalSymlinks(p.Root); err == nil {
		root = resolvedRoot
	}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		if abs == filepath.Join(root, ".claude", name) || abs == filepath.Join(p.Root, ".claude", name) {
			return ".claude/" + name + " holds ape's hooks and the session's environment: a person edits it, and `ape project init` restores the hooks"
		}
	}
	if rel, err := filepath.Rel(state, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p.StateRel() + "/" + strings.TrimPrefix(filepath.ToSlash(rel), ".") +
			" is ape's own record (gate runs, logs, hook verdicts) — use the ape command that records it; specs change only through ape change archive"
	}
	rel, err := filepath.Rel(dev, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p.briefReason(abs) // a link from outside into development/briefs/
	}
	rel = filepath.ToSlash(rel)
	devRel := p.DevRel
	switch {
	case rel == "specs" || strings.HasPrefix(rel, "specs/"):
		return devRel + "/" + rel + " is a living spec: specs change only through `ape change archive` — put the requirement in your change's ## Delta (ADDED/MODIFIED/REMOVED)"
	case rel == "archive" || strings.HasPrefix(rel, "archive/"):
		return devRel + "/" + rel + " is an archived change: the archive is written only by `ape change archive` — specs change only through ape change archive"
	case stateJSONRe.MatchString(rel):
		return devRel + "/" + rel + " is written only by ape (`ape change start|verify|review|accept|block|archive`) — specs change only through ape change archive"
	}
	return p.briefReason(abs)
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

// StopRecord is _apex/state/hooks/<key>.json: the consecutive
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
	// Notice is shown to the person when the stop is allowed but a check
	// was red: outside an ape session the hook informs and never blocks
	// (H13).
	Notice string
	Record StopRecord
}

// Output renders the decision as Stop/SubagentStop hook JSON; nil allows
// silently, a Notice allows with a systemMessage.
func (d StopDecision) Output() ([]byte, error) {
	if !d.Block {
		if d.Notice == "" {
			return nil, nil
		}
		return json.Marshal(map[string]any{"systemMessage": d.Notice})
	}
	return json.Marshal(map[string]any{"decision": "block", "reason": d.Reason})
}

// StopNotApeSession is the allow reason outside a session ape launched.
const StopNotApeSession = "not a session ape launched (no APE_SESSION): the stop hook informs, never blocks"

func (p *Project) stopRecordPath(sessionID, agentID string) string {
	key := sanitizeKey(sessionID)
	if key == "" {
		key = "no-session"
	}
	if agentID != "" {
		key += "-" + sanitizeKey(agentID)
	}
	return filepath.Join(p.HooksDir(), key+".json")
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
	p.ensureState()
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
	p.ensureState()
	_ = writeFileAtomic(path, string(data)+"\n")
}

// StopHook decides a Stop or SubagentStop. It blocks when HEAD is on a
// change's branch (D38), that change is in-progress, and one of its
// command checks is red, at most
// MaxConsecutiveStopBlocks times in a row (the count resets whenever
// Claude Code reports the stop is not itself a continuation from a block).
// The checks it runs are the ones red at the change's last verify, or its
// task checks before any verify (H10, stopChecks).
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

	// The change under work is the one whose branch is checked out (D38),
	// read from the working tree.
	id := p.HeadChange(ctx)
	if id == "" {
		rec.Verdict = StopAllow
		rec.ConsecutiveBlocks = 0
		rec.Reason = "HEAD is not on a change branch"
		return finish(StopDecision{Record: rec})
	}
	ci := p.loadChange(id)
	if ci.State == nil || ci.Doc == nil || ci.State.Status != StatusInProgress {
		status := "unreadable"
		if ci.State != nil {
			status = ci.State.Status
		}
		rec.Verdict = StopAllow
		rec.ConsecutiveBlocks = 0
		rec.Change = id
		rec.Reason = "change " + id + " on HEAD is " + status
		return finish(StopDecision{Record: rec})
	}
	rec.Change = ci.ID
	// H13: the settings.json copy of the hook also runs in a person's own
	// Claude Code session. There it never blocks — a person asking the
	// model a question on a change branch must not have it sent back to
	// work — and runs no check: it shows the person what was red at the
	// change's last verify. ape's sessions carry APE_SESSION, which Claude
	// Code hands to its hooks.
	if os.Getenv(envApeSession) == "" {
		rec.Verdict = StopAllow
		rec.ConsecutiveBlocks = 0
		rec.Reason = StopNotApeSession
		return finish(StopDecision{Record: rec, Notice: p.stopNotice(ci)})
	}
	if rec.ConsecutiveBlocks >= MaxConsecutiveStopBlocks {
		rec.Verdict = StopAllow
		rec.Reason = fmt.Sprintf("allowed after %d consecutive blocks", rec.ConsecutiveBlocks)
		rec.ConsecutiveBlocks = 0
		return finish(StopDecision{Record: rec})
	}
	checks, nothingRed := p.stopChecks(ci)
	if nothingRed {
		rec.Verdict = StopAllow
		rec.ConsecutiveBlocks = 0
		rec.Reason = "no check was red at the last verify"
		return finish(StopDecision{Record: rec})
	}
	results, judged, unknown := p.stopRun(ctx, ci, checks, opt)
	var red []CheckResult
	for i := range results {
		if results[i].Kind == KindRegression {
			judgeRegression(&results[i], ci.State.Regression)
		}
		if results[i].Red() {
			red = append(red, results[i])
		}
	}
	if len(red) == 0 && len(judged) == 0 && len(unknown) == 0 {
		rec.Verdict = StopAllow
		rec.ConsecutiveBlocks = 0
		rec.Reason = "all command checks green"
		return finish(StopDecision{Record: rec})
	}
	rec.ConsecutiveBlocks++
	rec.Verdict = StopBlock
	reason := stopReason(ci.ID, red, judged, unknown, rec.ConsecutiveBlocks)
	rec.Reason = truncate(reason, 300)
	return finish(StopDecision{Block: true, Reason: reason, Record: rec})
}

// stopNotice tells a person outside an ape session which checks of the
// in-progress change were red at its last verify of the current plan; ""
// when none were, or there is no such verify.
func (p *Project) stopNotice(ci ChangeInfo) string {
	lv := p.lastVerify(ci.ID)
	if lv == nil || lv.PlanDigest != ci.State.PlanDigest || len(lv.Red) == 0 {
		return ""
	}
	refs := make([]string, 0, len(lv.Red))
	for ref := range lv.Red {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	lines := make([]string, 0, len(refs))
	for _, ref := range limit(refs, 10) {
		if cmd, ok := lv.Red[ref]; ok {
			lines = append(lines, ref+" `"+cmd+"`")
		} else {
			lines = append(lines, ref)
		}
	}
	return fmt.Sprintf("APEX: change %s is in progress, and at its last verify (%s, %s) %d check(s) were red: %s. "+
		"Not blocking: this session was not launched by ape. `ape change verify %s` re-runs them.",
		ci.ID, lv.At, short(lv.SHA), len(refs), strings.Join(lines, ", "), ci.ID)
}

func stopReason(id string, ran, judged []CheckResult, unknown []PlannedCheck, n int) string {
	var b strings.Builder
	red := append(append([]CheckResult(nil), ran...), judged...)
	if len(red) > 0 {
		fmt.Fprintf(&b, "Change %s is in progress and %d command check(s) are red (stop blocked %d/%d):\n",
			id, len(red), n, MaxConsecutiveStopBlocks)
	} else {
		fmt.Fprintf(&b, "Change %s is in progress and %d command check(s) could not be judged here (stop blocked %d/%d).\n",
			id, len(unknown), n, MaxConsecutiveStopBlocks)
	}
	budget := 3000
	for i := range red {
		r := &red[i]
		from := ""
		if i >= len(ran) {
			from = " — from the last verify at HEAD, not re-run: too long for the stop hook"
		}
		fmt.Fprintf(&b, "\n%s `%s` %s (full output: %s)%s\n", r.Task, r.Cmd, r.Outcome(), r.Log, from)
		tail := lastN(r.Tail, 15)
		for _, l := range tail {
			if budget <= 0 {
				break
			}
			b.WriteString("  " + l + "\n")
			budget -= len(l) + 3
		}
	}
	if len(unknown) > 0 {
		refs := make([]string, 0, len(unknown))
		for i := range unknown {
			refs = append(refs, unknown[i].Ref)
		}
		fmt.Fprintf(&b, "\nNot re-run here, and no verify at HEAD judged them: %s — longer than the stop hook re-runs (%s a check, %s in all). "+
			"Run `ape change verify %s --wait 9m` (exit 75 means still running: run it again, never end your turn on it), then stop.\n",
			strings.Join(limit(refs, 10), ", "), StopCheckBudget, StopRunBudget, id)
	}
	fmt.Fprintf(&b, "\nMake the checks pass and run `ape change verify %s --wait 9m`. If a task cannot be met, run `ape change block %s --reason \"<why>\"` — never edit a check to make it pass.", id, id)
	return b.String()
}

// The stop hook's re-run budget (D58 2.5): a check whose last recorded run
// took longer than StopCheckBudget (or timeouts.check.max, if lower) is
// never re-run by the hook, nor any once StopRunBudget is spent in one
// hook run. Such a check is judged from the last verify at HEAD — red
// stays red, with its tail — or, with no verify at HEAD, the model is
// told to run the verify. The hook's own timeout stays StopHookTimeout:
// the hook never runs a long check, so it needs no more.
var (
	StopCheckBudget = 60 * time.Second
	StopRunBudget   = 600 * time.Second
)

// stopRun re-runs the stop hook's checks within its budget. ran are the
// results of the checks it ran; judged the red ones it took from the last
// verify at HEAD instead (too long to re-run); unknown the ones too long
// to re-run with no verify at HEAD to judge them by.
func (p *Project) stopRun(ctx context.Context, ci ChangeInfo, checks []PlannedCheck, opt RunOptions) (ran, judged []CheckResult, unknown []PlannedCheck) {
	cfg, _ := LoadConfig(p.Root)
	perCheck := min(StopCheckBudget, cfg.Timeouts.For(KindTask, 0))
	remaining := StopRunBudget
	lv := p.lastVerify(ci.ID)
	if lv != nil && lv.PlanDigest != ci.State.PlanDigest {
		lv = nil
	}
	atHead := lv != nil && lv.SHA == HeadSHA(ctx, p.Root)
	over := func(pc PlannedCheck) {
		if atHead {
			if c, red := lv.RedChecks[pc.Ref]; red {
				judged = append(judged, CheckResult{
					Task: pc.Ref, Kind: pc.Kind, Cmd: pc.Cmd, Exit: c.Exit, Reason: c.Reason,
					Detail: c.Detail, Tail: c.Tail, Log: c.Log, Secs: lv.Secs[pc.Ref],
				})
				return
			}
		}
		unknown = append(unknown, pc)
	}
	done := map[string]CheckResult{}
	for _, pc := range checks {
		if r, ok := done[pc.Cmd]; ok {
			r.Task, r.Kind = pc.Ref, pc.Kind
			ran = append(ran, r)
			continue
		}
		if lv != nil {
			if secs, ok := lv.Secs[pc.Ref]; ok && time.Duration(secs*float64(time.Second)) > min(perCheck, remaining) {
				over(pc)
				continue
			}
		}
		limit := min(perCheck, remaining)
		if limit < time.Second {
			over(pc)
			continue
		}
		o := opt
		o.Timeout = limit
		r := p.RunChecks(ctx, p.LogDir(ci.ID), "stop-", []PlannedCheck{pc}, o)[0]
		remaining -= time.Duration(r.Secs * float64(time.Second))
		// Stopped at the hook's own limit, below the project's: too long
		// for the hook, not red.
		if r.Reason == ReasonCeiling && limit < cfg.Timeouts.For(pc.Kind, 0) {
			over(pc)
			continue
		}
		done[pc.Cmd] = r
		ran = append(ran, r)
	}
	return ran, judged, unknown
}

// --- the ape task side of the stop gate --------------------------------------

// StopGateConfigured reports whether a project's stop hook runs on a
// main Stop: a v1 project whose settings register `ape hook stop`. Every
// such Stop then has a verdict in the session's log — an allow, quickly,
// when HEAD is on no in-progress change's branch — so `ape task` consumes one
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
