// Package memory checks the size of an APEX v0 project's
// `{development_folder}/team-memory.md` — the append-only lessons file
// every retrospective writes — against two budgets, for `ape doctor`'s
// memory.size row. The index and entry reads went with `ape memory`,
// retired with APEX v0.
//
// The size field is called `bytes`: it is what os.Stat returns and what
// the 256 KB Read cap is measured in.
package memory

import (
	"os"
)

// Default size budgets, in bytes.
//
// The soft budget says compaction is due; the retrospective gates
// spawning apex-distillator on it. The hard ceiling says compaction is
// overdue.
//
// Neither is a Read limit, and the 256 KiB byte cap is not the one that
// binds first. Claude Code also caps one Read at ~25,000 tokens, which is
// about 72 KB of markdown at the ~2.9 bytes/token measured on APEX files
// (Claude Code's own count: 62,488 tokens for a 181,187-byte file). Past
// that a Read returns a PARTIAL view with a paging banner; the byte cap
// only makes a whole-file Read fail outright, much later. So the 40 KiB
// soft budget is ~14k tokens, and the 200 KiB hard ceiling is already well
// past what one Read shows. That is acceptable because team memory is read
// bounded, through `ape memory show` (an index, then named entries), never
// whole: these numbers are about keeping compaction on schedule, not about
// what fits in a Read.
//
// Defaults live here rather than in `_apex/config.yaml` so this
// deliverable does not wait on a framework template change; the flags
// exist mainly so the thresholds are testable without writing 200 KiB
// fixtures.
const (
	DefaultSoftBudget  = 40 << 10
	DefaultHardCeiling = 200 << 10
	// ReadCap is Claude Code's Read byte cap, past which a whole-file Read
	// fails outright; kept for context in messages. Not a threshold, and
	// not the limit that binds first: the ~25,000-token per-Read cap
	// (about 72 KB of markdown) truncates the view long before it.
	ReadCap = 256 << 10
	// bytesPerTokenTenths is the bytes-per-token ratio behind the
	// "~N tokens" hint, in tenths: 2.9, measured on APEX markdown (Claude
	// Code counted 62,488 tokens for a 181,187-byte file). It used to be
	// 4, which read low by about a quarter. Still only a hint: never gated
	// on.
	bytesPerTokenTenths = 29
)

// State is the verdict of a size check.
type State string

const (
	// StateAbsent — no team-memory.md yet. A fresh project, not a problem.
	StateAbsent State = "absent"
	StateOK     State = "ok"
	// StateOverSoft — compaction is due at the next epic close.
	StateOverSoft State = "over-soft"
	// StateOverHard — compaction is overdue: the file is far past what one
	// Read shows and nearing the byte cap at which a Read fails outright.
	// The soft gate should have caught this several runs earlier, so this
	// is a bug report, not a routine compaction.
	StateOverHard State = "over-hard"
)

// Check is the payload of a size check. It is produced by an os.Stat, so
// it stays cheap enough to run on every retrospective — which is what
// makes the compaction gate reliable rather than aspirational.
type Check struct {
	Path        string `json:"path"         yaml:"path"`
	Exists      bool   `json:"exists"       yaml:"exists"`
	Bytes       int64  `json:"bytes"        yaml:"bytes"`
	SoftBudget  int64  `json:"soft_budget"  yaml:"soft_budget"`
	HardCeiling int64  `json:"hard_ceiling" yaml:"hard_ceiling"`
	State       State  `json:"state"        yaml:"state"`
	// EstimatedTokens is bytes/2.9 and is labelled an estimate everywhere
	// it is shown. Nothing gates on it.
	EstimatedTokens int64 `json:"estimated_tokens" yaml:"estimated_tokens"`
}

// CheckSize stats the file and classifies it. soft and hard may be 0 to
// take the defaults.
func CheckSize(path string, soft, hard int64) Check {
	if soft <= 0 {
		soft = DefaultSoftBudget
	}
	if hard <= 0 {
		hard = DefaultHardCeiling
	}
	c := Check{Path: path, SoftBudget: soft, HardCeiling: hard, State: StateAbsent}
	info, err := os.Stat(path)
	if err != nil {
		return c
	}
	c.Exists = true
	c.Bytes = info.Size()
	c.EstimatedTokens = c.Bytes * 10 / bytesPerTokenTenths
	switch {
	case c.Bytes > hard:
		c.State = StateOverHard
	case c.Bytes > soft:
		c.State = StateOverSoft
	default:
		c.State = StateOK
	}
	return c
}
