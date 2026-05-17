package cost

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ScanSessionJSONL reads a Claude Code session JSONL file once and
// returns the aggregated cost / token totals plus the last model seen.
// Used by `ape chat` (post-run) to populate session.yaml. PLAN-5 / C7.
//
// Unlike Tailer (which polls a live file), this is a one-shot reader
// for files that are already complete. Malformed lines are skipped.
func ScanSessionJSONL(path string) (Totals, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return Totals{}, "", fmt.Errorf("cost.ScanSessionJSONL: %w", err)
	}
	defer f.Close()

	var totals Totals
	lastModel := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var al AssistantLine
		if err := json.Unmarshal(sc.Bytes(), &al); err != nil {
			continue
		}
		if al.Type != "assistant" {
			continue
		}
		lastModel = al.Message.Model
		price, _ := Lookup(al.Message.Model)
		totals.Add(al.Message.Usage, price)
	}
	if err := sc.Err(); err != nil {
		return totals, lastModel, fmt.Errorf("cost.ScanSessionJSONL: scan: %w", err)
	}
	return totals, lastModel, nil
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
