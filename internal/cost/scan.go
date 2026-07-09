package cost

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// TurnRecord is one deduped assistant turn (PLAN-10 D1). It carries the
// dimensions needed to reprice against Claude Code API rates at any later
// moment — per-turn timestamp + normalized model + full cache split — plus
// the identifiers used to attribute and dedupe it. AgentID is populated by
// higher-level callers that know a transcript is a sub-agent file (the
// filename is the only stable sub identifier); ScanSession leaves it empty.
type TurnRecord struct {
	Timestamp  time.Time
	Model      string
	SessionID  string
	MessageID  string
	RequestID  string
	StopReason string
	Sidechain  bool
	AgentID    string
	Version    string // Claude Code version that wrote the turn
	Usage      UsageBlock
	CostUSD    float64
}

// ScanResult is the full outcome of scanning one session transcript:
// the aggregate totals, the per-model breakdown (keyed by normalized
// model id — see NormalizeModel), the last model seen, and (PLAN-10 D1)
// the per-turn records plus the session's first/last turn timestamps and
// the Claude Code version that wrote it.
type ScanResult struct {
	Totals    Totals
	ByModel   map[string]Totals
	LastModel string

	// PLAN-10 D1 additions (all additive — existing callers use the three
	// fields above unchanged).
	Turns         []TurnRecord
	FirstTurnAt   time.Time
	LastTurnAt    time.Time
	ClaudeVersion string
}

// ScanSession reads a Claude Code session JSONL file once and returns
// the aggregated cost / token totals plus a per-model breakdown.
//
// Unlike Tailer (which polls a live file), this is a one-shot reader
// for files that are already complete. Malformed lines are skipped.
// Tokens and NumTurns accumulate price-independently — an unpriced
// model still yields non-zero tokens/turns with CostUSD 0.
func ScanSession(path string) (ScanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return ScanResult{}, fmt.Errorf("cost.ScanSession: %w", err)
	}
	defer f.Close()

	turns, err := scanTurns(f)
	if err != nil {
		return ScanResult{ByModel: map[string]Totals{}}, err
	}
	return aggregateTurns(turns), nil
}

// scanTurns reads every assistant turn from a transcript and applies the
// H6 dedup (PLAN-10 D1): claude logs the same assistant message multiple
// times — streaming snapshots and tool-turn re-renders — under one
// message.id. We keep exactly one turn per message.id, preferring the
// entry that carries a stop_reason (the final, complete snapshot) over any
// earlier partial. Entries with an empty message.id can't be deduped, so
// each is kept (matching the prior behaviour). Non-assistant and isMeta
// rows are skipped. Malformed lines are skipped, never fatal.
func scanTurns(r io.Reader) ([]TurnRecord, error) {
	byID := map[string]int{} // message.id → index into turns
	turns := make([]TurnRecord, 0, 64)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var al AssistantLine
		if err := json.Unmarshal(sc.Bytes(), &al); err != nil {
			continue
		}
		if al.Type != "assistant" || al.IsMeta {
			continue
		}
		model := NormalizeModel(al.Message.Model)
		ts := parseTurnTime(al.Timestamp)
		price, _ := LookupAt(model, ts)
		tr := TurnRecord{
			Timestamp:  ts,
			Model:      model,
			SessionID:  al.SessionID,
			MessageID:  al.Message.ID,
			RequestID:  al.RequestID,
			StopReason: al.Message.StopReason,
			Sidechain:  al.IsSidechain,
			Version:    al.Version,
			Usage:      al.Message.Usage,
			CostUSD:    TurnCost(al.Message.Usage, price),
		}
		if al.Message.ID == "" {
			turns = append(turns, tr)
			continue
		}
		if idx, seen := byID[al.Message.ID]; seen {
			// Replace the kept turn only when the new one is at least as
			// authoritative: it carries a stop_reason, or the kept one
			// never did (last-seen wins among equally-partial snapshots).
			if tr.StopReason != "" || turns[idx].StopReason == "" {
				turns[idx] = tr
			}
			continue
		}
		byID[al.Message.ID] = len(turns)
		turns = append(turns, tr)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("cost.ScanSession: scan: %w", err)
	}
	return turns, nil
}

// aggregateTurns folds deduped turns into a ScanResult: aggregate + per-
// model totals, the last model, the per-turn records (chronological), and
// the first/last turn timestamps + Claude Code version (PLAN-10 D1).
func aggregateTurns(turns []TurnRecord) ScanResult {
	res := ScanResult{ByModel: map[string]Totals{}, Turns: turns}
	sort.SliceStable(res.Turns, func(i, j int) bool {
		return res.Turns[i].Timestamp.Before(res.Turns[j].Timestamp)
	})
	for i := range res.Turns {
		tr := &res.Turns[i]
		price, _ := LookupAt(tr.Model, tr.Timestamp)
		res.LastModel = tr.Model
		res.Totals.Add(tr.Usage, price)
		mt := res.ByModel[tr.Model]
		mt.Add(tr.Usage, price)
		res.ByModel[tr.Model] = mt
		if !tr.Timestamp.IsZero() {
			if res.FirstTurnAt.IsZero() || tr.Timestamp.Before(res.FirstTurnAt) {
				res.FirstTurnAt = tr.Timestamp
			}
			if tr.Timestamp.After(res.LastTurnAt) {
				res.LastTurnAt = tr.Timestamp
			}
		}
		if tr.Version != "" {
			res.ClaudeVersion = tr.Version
		}
	}
	return res
}

// parseTurnTime parses a transcript entry's `timestamp` leniently: an
// unparseable value yields the zero time (which prices at the conservative
// standard rate) rather than dropping the turn.
func parseTurnTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// ScanPaths scans and merges a set of transcripts — a session's main +
// sub-agent files (from SessionFiles) — into one ScanResult: summed
// aggregate + per-model totals, the union of turns (chronological), the
// earliest/latest turn timestamps, and the last non-empty Claude Code
// version. Unreadable files are skipped so one missing sub-agent transcript
// never zeroes the whole set. Used by PLAN-17 `ape metrics` / `ape
// transcript` for a session's full main+subagent usage.
func ScanPaths(paths []string) ScanResult {
	merged := ScanResult{ByModel: map[string]Totals{}}
	for _, p := range paths {
		res, err := ScanSession(p)
		if err != nil {
			continue
		}
		merged.Totals = sumTotals(merged.Totals, res.Totals)
		merged.ByModel = sumPerModel(merged.ByModel, res.ByModel)
		merged.Turns = append(merged.Turns, res.Turns...)
		if !res.FirstTurnAt.IsZero() && (merged.FirstTurnAt.IsZero() || res.FirstTurnAt.Before(merged.FirstTurnAt)) {
			merged.FirstTurnAt = res.FirstTurnAt
		}
		if res.LastTurnAt.After(merged.LastTurnAt) {
			merged.LastTurnAt = res.LastTurnAt
		}
		if res.ClaudeVersion != "" {
			merged.ClaudeVersion = res.ClaudeVersion
		}
		if res.LastModel != "" {
			merged.LastModel = res.LastModel
		}
	}
	sort.SliceStable(merged.Turns, func(i, j int) bool {
		return merged.Turns[i].Timestamp.Before(merged.Turns[j].Timestamp)
	})
	return merged
}

// ScanSessionJSONL is the aggregate-only wrapper around ScanSession,
// kept for callers that don't need the per-model breakdown.
// Used by `ape chat` (post-run) to populate session.yaml. PLAN-5 / C7.
func ScanSessionJSONL(path string) (Totals, string, error) {
	res, err := ScanSession(path)
	return res.Totals, res.LastModel, err
}

// FindSessionJSONL globs ~/.claude/projects/*/*.jsonl and returns the
// path of the file whose mtime is newest AND >= since. Returns empty
// string + nil error when nothing matches — the caller treats that as
// "no session file was written" (typical of `--mock` runs).
//
// Best-effort discovery. A future PR could pass `--session <id>` to
// claude on spawn and look up the exact file, but Claude Code does
// not document a stable --session flag today, so the mtime heuristic
// is what we have. PLAN-5 / C7.
func FindSessionJSONL(home string, since time.Time) (string, error) {
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = h
	}
	root := filepath.Join(home, ".claude", "projects")
	matches, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil {
		return "", err
	}
	type cand struct {
		path  string
		mtime time.Time
	}
	var cands []cand
	for _, p := range matches {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if since.IsZero() || info.ModTime().After(since) || info.ModTime().Equal(since) {
			cands = append(cands, cand{path: p, mtime: info.ModTime()})
		}
	}
	if len(cands) == 0 {
		return "", nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })
	return cands[0].path, nil
}

// ScanLatestSession is a convenience that runs FindSessionJSONL +
// ScanSessionJSONL in sequence. Used by `ape chat` exit. Returns
// (Totals{}, "", "", nil) when no file matches (no error — just
// nothing to fold in).
func ScanLatestSession(home string, since time.Time) (totals Totals, model, path string, err error) {
	path, err = FindSessionJSONL(home, since)
	if err != nil {
		return Totals{}, "", "", err
	}
	if path == "" {
		return Totals{}, "", "", nil
	}
	totals, model, err = ScanSessionJSONL(path)
	return totals, model, path, err
}

var _ = errors.New // keep errors import used if a future variant returns sentinels
