// Package sessiondriver holds the reusable slice of the interactive
// Claude-session machinery: hook fan-out to a runlog, transcript
// binding + telemetry-baseline tracking, the transcript-derived
// telemetry scan (main session delta + sub-agent sessions, with the
// v0.0.34 double-count guard and the dropped-SubagentStop robustness
// sweep), and Stop-hook step-done signalling with an idle-timeout
// backstop.
//
// It is consumed by two callers:
//
//   - the pipeline interactive runner wiring (internal/apecmd's
//     interactiveCore), which shares the ScanStep telemetry algorithm;
//   - `ape prompt` (internal/apecmd's prompt runner), which drives a
//     single standalone session end-to-end via Driver.
//
// The pipeline runner keeps its own step-contract verifier and
// progress-event publisher (both pipeline-specific); sessiondriver owns
// only the parts that are identical for a pipeline step and a one-shot
// prompt session.
package sessiondriver

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// DefaultFlushGrace is the wait between Stop-hook receipt and the
// transcript scan. Claude buffers writes to its per-session JSONL; the
// Stop hook can fire before the last assistant turn is flushed. 500ms
// is far above the observed flush latency without meaningfully slowing
// a run. Callers pass their own via ScanParams.FlushGrace (the pipeline
// runner threads a test-shortened value through); a zero falls back to
// this default.
const DefaultFlushGrace = 500 * time.Millisecond

// SubCapture is one sub-agent (Agent tool) session's transcript,
// identified by agentID — the only stable per-sub identifier (a sub's
// internal sessionId equals its parent's, so session_id alone collapses
// every sub into one phantom pointing at the parent transcript).
type SubCapture struct {
	AgentID         string
	ParentSessionID string
	Transcript      string // agent_transcript_path (a distinct agent-<id>.jsonl)
}

// ScanParams bundles the inputs ScanStep needs. The pipeline runner
// assembles it from interactiveCore's per-step state; Driver assembles
// it from its own single-session state.
type ScanParams struct {
	// Source is the main session transcript path captured from a
	// UserPromptSubmit (or Stop) hook.
	Source string
	// ParentSessionID is the main claude session id, used as the
	// per-session record id and as the fallback parent for subs.
	ParentSessionID string
	// PrevPath is the transcript the PrevTotals/PrevByModel baseline was
	// computed against. When Source differs the baseline resets to zero
	// (a `/clear` between steps rotates the session_id → a new file).
	PrevPath    string
	PrevTotals  cost.Totals
	PrevByModel map[string]cost.Totals
	// StepStart anchors the sub-agent robustness sweep's mtime window.
	StepStart time.Time
	// Subs are the hook-captured sub-agent sessions for this step.
	Subs []SubCapture
	// GetRunLog returns the run's runlog writer (or nil) for durable
	// transcript snapshots. May be nil.
	GetRunLog func() *runlog.Writer
	// FlushGrace overrides DefaultFlushGrace when non-zero.
	FlushGrace time.Duration
}

// SessionUsage is one claude session's usage within a step: the main
// session (delta vs the stage baseline) or a sub-agent session (whole).
type SessionUsage struct {
	SessionID       string
	ParentSessionID string // empty for the main session
	Totals          cost.Totals
	ByModel         map[string]cost.Totals
}

// MainScan is the absolute scan of the main transcript. The pipeline
// runner stores it as the next step's baseline; Driver ignores it.
type MainScan struct {
	Totals  cost.Totals
	ByModel map[string]cost.Totals
	Path    string
}

// Telemetry is the transcript-derived outcome of one step/session: the
// aggregate (main delta + subs), the aggregate per-model breakdown, the
// per-session records, and a diagnosability Note when telemetry could
// not be derived. Advance is non-nil only when the main transcript
// scanned OK — callers that track a rolling baseline store it then.
type Telemetry struct {
	Totals   cost.Totals
	ByModel  map[string]cost.Totals
	Sessions []SessionUsage
	Note     string
	Advance  *MainScan
}

// note returns a zeroed Telemetry carrying the diagnosability
// breadcrumb. A zeroed step must be explainable, never silent — the
// caller stamps Note onto the manifest / session record.
func note(msg string) *Telemetry {
	return &Telemetry{Note: msg}
}

// ScanStep derives a step's telemetry from its captured transcripts:
// the main-session delta against the supplied baseline plus every
// sub-agent session scanned whole, aggregated with the double-count
// guard and the dropped-SubagentStop robustness sweep. Each scanned
// transcript is snapshotted into the runlog (best-effort). It performs
// no I/O beyond reading the transcripts and copying them; it never
// writes to stderr — the caller decides how to surface Note.
func ScanStep(p ScanParams) *Telemetry {
	if p.Source == "" {
		return note("no transcript captured for step (no hook carried a transcript_path)")
	}
	prev := p.PrevTotals
	prevByModel := p.PrevByModel
	// A `/clear` between steps rotates the session_id → a new
	// transcript_path. The previous cumulative was computed against a
	// different file — useless as a baseline — so reset to zero. The
	// step's delta then equals its absolute usage in the new transcript.
	if p.Source != p.PrevPath {
		prev = cost.Totals{}
		prevByModel = nil
	}

	grace := p.FlushGrace
	if grace <= 0 {
		grace = DefaultFlushGrace
	}
	time.Sleep(grace)

	if !fileExists(p.Source) {
		return note(fmt.Sprintf("transcript missing at scan time (path %q)", p.Source))
	}
	res, err := cost.ScanSession(p.Source)
	if err != nil {
		return note(fmt.Sprintf("transcript scan failed: %v (path %q)", err, p.Source))
	}
	snapshot(p.GetRunLog, p.Source)

	mainDelta := subTotals(res.Totals, prev)
	mainByModel := byModelDelta(res.ByModel, prevByModel)

	tele := &Telemetry{
		Totals:  mainDelta,
		ByModel: mainByModel,
		Sessions: []SessionUsage{{
			SessionID: p.ParentSessionID,
			Totals:    mainDelta,
			ByModel:   mainByModel,
		}},
		Advance: &MainScan{Totals: res.Totals, ByModel: res.ByModel, Path: p.Source},
	}

	// Sub-agent sessions: separate transcripts (agent-<id>.jsonl),
	// scanned whole, folded into the aggregate + per-model breakdown.
	// Merge the hook-captured subs with a robustness sweep of the main
	// session's subagents/ dir (so a dropped SubagentStop doesn't lose a
	// sub), dedup by resolved path, and apply the double-count guard.
	cleanMain := filepath.Clean(p.Source)
	type subCand struct{ agentID, parent, path string }
	var cands []subCand
	seenPath := map[string]bool{}
	addCand := func(agentID, parent, path string) {
		if path == "" {
			return
		}
		cp := filepath.Clean(path)
		// Double-count guard (regression lock): a sub whose resolved
		// transcript equals the main/active transcript is the exact
		// 2×-main signature — never fold it.
		if cp == cleanMain || seenPath[cp] {
			return
		}
		seenPath[cp] = true
		cands = append(cands, subCand{agentID: agentID, parent: parent, path: cp})
	}
	for _, sub := range p.Subs {
		addCand(sub.AgentID, sub.ParentSessionID, sub.Transcript)
	}
	for _, sf := range cost.SessionFiles(p.Source, p.StepStart) {
		if sf.Kind != cost.SessionSubagent {
			continue
		}
		addCand(sf.SessionID, p.ParentSessionID, sf.Path)
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].agentID != cands[j].agentID {
			return cands[i].agentID < cands[j].agentID
		}
		return cands[i].path < cands[j].path
	})
	for _, cd := range cands {
		if !fileExists(cd.path) {
			continue
		}
		subRes, subErr := cost.ScanSession(cd.path)
		if subErr != nil {
			continue
		}
		parent := cd.parent
		if parent == "" {
			parent = p.ParentSessionID
		}
		subByModel := byModelDelta(subRes.ByModel, nil)
		// SessionID = agent_id: the sub's internal sessionId equals the
		// parent's, so agent_id is the only distinct per-sub identifier.
		tele.Sessions = append(tele.Sessions, SessionUsage{
			SessionID:       cd.agentID,
			ParentSessionID: parent,
			Totals:          subRes.Totals,
			ByModel:         subByModel,
		})
		tele.Totals = sumTotals(tele.Totals, subRes.Totals)
		if tele.ByModel == nil {
			tele.ByModel = map[string]cost.Totals{}
		}
		for model, u := range subByModel {
			tele.ByModel[model] = sumTotals(tele.ByModel[model], u)
		}
		snapshot(p.GetRunLog, cd.path)
	}

	if tele.Totals.NumTurns == 0 {
		// Distinguish a partial file (lines but no complete assistant
		// turn) from an empty one.
		tele.Note = fmt.Sprintf(
			"transcript scan processed zero assistant turns (path %q, %d line(s))",
			p.Source, countLines(p.Source),
		)
	}
	return tele
}

// snapshot copies a scanned transcript into the run dir so the run's
// record survives ~/.claude/projects/ rotation. Best-effort.
func snapshot(getRunLog func() *runlog.Writer, path string) {
	if getRunLog == nil {
		return
	}
	if writer := getRunLog(); writer != nil {
		_, _ = writer.SnapshotTranscript(filepath.Base(path), path)
	}
}

// subTotals returns a-b field-wise.
func subTotals(a, b cost.Totals) cost.Totals {
	return cost.Totals{
		CostUSD:               a.CostUSD - b.CostUSD,
		InputTokens:           a.InputTokens - b.InputTokens,
		OutputTokens:          a.OutputTokens - b.OutputTokens,
		CacheReadTokens:       a.CacheReadTokens - b.CacheReadTokens,
		CacheCreationTokens:   a.CacheCreationTokens - b.CacheCreationTokens,
		CacheCreation5mTokens: a.CacheCreation5mTokens - b.CacheCreation5mTokens,
		CacheCreation1hTokens: a.CacheCreation1hTokens - b.CacheCreation1hTokens,
		NumTurns:              a.NumTurns - b.NumTurns,
	}
}

// sumTotals returns a+b field-wise.
func sumTotals(a, b cost.Totals) cost.Totals {
	return cost.Totals{
		CostUSD:               a.CostUSD + b.CostUSD,
		InputTokens:           a.InputTokens + b.InputTokens,
		OutputTokens:          a.OutputTokens + b.OutputTokens,
		CacheReadTokens:       a.CacheReadTokens + b.CacheReadTokens,
		CacheCreationTokens:   a.CacheCreationTokens + b.CacheCreationTokens,
		CacheCreation5mTokens: a.CacheCreation5mTokens + b.CacheCreation5mTokens,
		CacheCreation1hTokens: a.CacheCreation1hTokens + b.CacheCreation1hTokens,
		NumTurns:              a.NumTurns + b.NumTurns,
	}
}

// byModelDelta subtracts the per-model baseline from the fresh scan and
// drops all-zero entries. baseline nil means "no baseline".
func byModelDelta(current, baseline map[string]cost.Totals) map[string]cost.Totals {
	if len(current) == 0 {
		return nil
	}
	out := map[string]cost.Totals{}
	for model, cur := range current {
		d := cur
		if base, ok := baseline[model]; ok {
			d = subTotals(cur, base)
		}
		if d == (cost.Totals{}) {
			continue
		}
		out[model] = d
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fileExists reports whether path exists and is a regular file (or a
// resolvable symlink to one).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// countLines returns the newline count of path, or -1 when it can't be
// read. Used only to enrich a zero-turn telemetry note.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		n++
	}
	if sc.Err() != nil {
		return -1
	}
	return n
}
