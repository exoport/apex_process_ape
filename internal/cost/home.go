package cost

import (
	"path/filepath"
	"sort"
	"time"
)

// Scanning a whole Claude HOME rather than one session (PLAN-24 D3).
//
// A sandbox workspace's $HOME is a host directory: the guest's /sandbox/home is
// a read-write bind from the per-workspace staging dir aped composed, so every
// transcript a Claude session inside a workspace writes lands on the host
// already. Nothing needs to ship them anywhere — the gap was that nothing
// collected them.
//
// The layout is the ordinary one. Claude Code writes
// <home>/.claude/projects/<slug>/<sid>.jsonl with sub-agent transcripts under
// <home>/.claude/projects/<slug>/<sid>/subagents/agent-<id>.jsonl, which is
// exactly the shape FindSessionJSONL already globs for a local run. So this is
// the same enumeration widened from "the newest session" to "every session",
// and the existing per-file scanner is reused unchanged.

// HomeResult is the aggregate of every session transcript under one Claude home.
// It embeds the ordinary ScanResult (totals, per-model breakdown, per-turn
// records, pricing health) and adds the two counts a per-home rollup needs but a
// per-file scan has no notion of.
type HomeResult struct {
	ScanResult
	// Sessions is the number of MAIN transcripts found — how many Claude
	// sessions ran in this home.
	Sessions int
	// Files is the total transcript count, main + sub-agent.
	Files int
}

// HomeSessions enumerates every transcript under home: each main session
// (<home>/.claude/projects/*/*.jsonl) followed by its sub-agent transcripts.
//
// Path-sorted and deduped, with no mtime floor — a home rollup is all-time by
// definition, unlike a per-step sweep which must not re-fold a previous step's
// sub-agents. A home with no .claude tree yields nothing and no error: a
// workspace nobody has run Claude in has no cost, which is a fact rather than a
// failure.
func HomeSessions(home string) []SessionFile {
	if home == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	out := make([]SessionFile, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, main := range matches {
		for _, sf := range SessionFiles(main, time.Time{}) {
			clean := filepath.Clean(sf.Path)
			if seen[clean] {
				continue
			}
			seen[clean] = true
			out = append(out, sf)
		}
	}
	return out
}

// ScanHome scans every transcript under home and returns the merged rollup.
//
// Unreadable files are skipped rather than fatal (ScanPaths' rule): one
// truncated transcript in a long-lived workspace must not zero the rollup for
// all the others. The pricing-health maps come through, so a caller can still
// tell a total that is a lower bound from one that is exact.
func ScanHome(home string) HomeResult {
	files := HomeSessions(home)
	paths := make([]string, 0, len(files))
	mains := 0
	for _, f := range files {
		paths = append(paths, f.Path)
		if f.Kind == SessionMain {
			mains++
		}
	}
	return HomeResult{
		ScanResult: ScanPaths(paths),
		Sessions:   mains,
		Files:      len(files),
	}
}
