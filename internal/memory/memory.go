// Package memory reads `{development_folder}/team-memory.md` — the
// append-only lessons file every retrospective writes and thirteen skills
// read — without loading it into an LLM context window.
//
// The file outgrew whole-file reading: at 431,950 bytes on the reference
// project a `Read` fails outright ("exceeds maximum allowed size
// (256KB)"), including for the retrospective that is instructed to
// re-read it before editing it. So this package offers three operations:
// an index of what is in there (ordinal, section, date, bytes, title), the
// verbatim body of a named entry, and a size check against two budgets.
//
// The size field is called `bytes`, in both the index and the check, and
// the name is deliberate: it is what os.Stat returns and what the 256 KB
// Read cap is measured in. Prose that calls it `size` is describing the
// same field loosely — the JSON key is `bytes` everywhere.
//
// The index is structurally lossless and carries no filter, ranking or
// predicate — deliberately. Most call sites sit inside `## On Activation`,
// which runs BEFORE the story is identified, so no predicate keyed on
// "this story's domain" could work there. Selection has to be possible
// from ordinal, section, date, bytes and title alone.
package memory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Default size budgets, in bytes.
//
// The soft budget says compaction is due; the retrospective gates
// spawning apex-distillator on it. The hard ceiling is the one that
// matters: it sits below Claude Code's 256 KiB Read cap, past which the
// file becomes unreadable by its own writer. 40 KiB is roughly 10k
// tokens.
//
// Defaults live here rather than in `_apex/config.yaml` so this
// deliverable does not wait on a framework template change; the flags
// exist mainly so the thresholds are testable without writing 200 KiB
// fixtures.
const (
	DefaultSoftBudget  = 40 << 10
	DefaultHardCeiling = 200 << 10
	// ReadCap is Claude Code's whole-file Read limit, for context in
	// messages. Not a threshold — the hard ceiling is what gets enforced.
	ReadCap = 256 << 10
	// bytesPerTokenEstimate is the crude chars-per-token ratio used only
	// for the human-readable "~N tokens" hint. Never gated on.
	bytesPerTokenEstimate = 4
)

// State is the verdict of a size check.
type State string

const (
	// StateAbsent — no team-memory.md yet. A fresh project, not a problem.
	StateAbsent State = "absent"
	StateOK     State = "ok"
	// StateOverSoft — compaction is due at the next epic close.
	StateOverSoft State = "over-soft"
	// StateOverHard — the file is approaching the Read cap. The soft gate
	// should have caught this several runs earlier, so this is a bug
	// report, not a routine compaction.
	StateOverHard State = "over-hard"
)

// Entry is one `### ` record in the file.
type Entry struct {
	Ordinal int    `json:"ordinal"           yaml:"ordinal"`
	Section string `json:"section,omitempty" yaml:"section,omitempty"`
	Date    string `json:"date,omitempty"    yaml:"date,omitempty"`
	Bytes   int    `json:"bytes"             yaml:"bytes"`
	Title   string `json:"title"             yaml:"title"`
	// Line is the 1-based line number of the entry's heading, so an
	// operator can jump straight to it.
	Line int `json:"line" yaml:"line"`

	start, end int // byte span of the whole entry, heading included
}

// Index is the structurally-lossless listing of the file.
type Index struct {
	Path    string  `json:"path"    yaml:"path"`
	Entries []Entry `json:"entries" yaml:"entries"`
	// The three byte counts partition the file exactly:
	// EntryBytes + HeadingBytes + OtherBytes == TotalBytes.
	// That conservation law is what proves the scan is lossless — no
	// region of the file is silently unaccounted for.
	EntryBytes   int `json:"entry_bytes"   yaml:"entry_bytes"`
	HeadingBytes int `json:"heading_bytes" yaml:"heading_bytes"`
	OtherBytes   int `json:"other_bytes"   yaml:"other_bytes"`
	TotalBytes   int `json:"total_bytes"   yaml:"total_bytes"`
	Count        int `json:"count"         yaml:"count"`
}

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
	// EstimatedTokens is bytes/4 and is labelled an estimate everywhere it
	// is shown. Nothing gates on it.
	EstimatedTokens int64 `json:"estimated_tokens" yaml:"estimated_tokens"`
}

// ErrOutOfRange is returned by Bodies for an ordinal the file does not
// have.
var ErrOutOfRange = errors.New("entry ordinal out of range")

// entryDateRe pulls the trailing ISO date out of a heading written to the
// framework's template, `### {{area}} — {{YYYY-MM-DD}}`. A heading
// without one keeps its whole text as the title and an empty date, rather
// than being rejected: the index is lossless, so a hand-written heading
// still gets an ordinal.
var entryDateRe = regexp.MustCompile(`^(.*?)\s*[—–-]\s*(\d{4}-\d{2}-\d{2})\s*$`)

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
	c.EstimatedTokens = c.Bytes / bytesPerTokenEstimate
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

// Load reads and scans the file. An absent file is an empty index, not an
// error — the same reason CheckSize reports `absent`.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Index{Path: path}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	idx := scan(data)
	idx.Path = path
	return idx, nil
}

// scan walks the file line by line, tracking fenced code blocks.
//
// The fence tracking is the whole point of not using a regexp sweep.
// Retrospectives paste commands and diffs into entries, so a `### ` line
// inside a fence is common — and counting it would shift every ordinal
// after it, handing `show <n>` the wrong entry with nothing to signal the
// mistake.
func scan(data []byte) *Index {
	idx := &Index{TotalBytes: len(data)}
	var (
		section    string
		inFence    bool
		fenceMark  string
		lineNo     int
		openEntry  = -1 // index into idx.Entries of the entry being accumulated
		offset     int
		headingSum int
	)
	for offset < len(data) {
		lineNo++
		lineEnd := bytes.IndexByte(data[offset:], '\n')
		lineLen := lineEnd + 1
		if lineEnd < 0 {
			lineLen = len(data) - offset
		}
		line := data[offset : offset+lineLen]
		trimmed := strings.TrimRight(string(line), "\r\n")

		if mark, ok := fenceDelimiter(trimmed); ok {
			switch {
			case !inFence:
				inFence, fenceMark = true, mark
			case strings.HasPrefix(mark, fenceMark[:1]):
				// A closing fence must use the same character as the
				// opener; a ``` inside a ~~~ block is content.
				inFence, fenceMark = false, ""
			}
			offset += lineLen
			continue
		}

		if !inFence {
			switch {
			case isH3(trimmed):
				if openEntry >= 0 {
					idx.Entries[openEntry].end = offset
				}
				title, date := splitHeading(trimmed[len("### "):])
				idx.Entries = append(idx.Entries, Entry{
					Ordinal: len(idx.Entries) + 1,
					Section: section,
					Date:    date,
					Title:   title,
					Line:    lineNo,
					start:   offset,
					end:     len(data),
				})
				openEntry = len(idx.Entries) - 1
				offset += lineLen
				continue
			case isH2(trimmed):
				if openEntry >= 0 {
					idx.Entries[openEntry].end = offset
					openEntry = -1
				}
				section = strings.TrimSpace(trimmed[len("## "):])
				headingSum += lineLen
				offset += lineLen
				continue
			}
		}
		offset += lineLen
	}

	idx.HeadingBytes = headingSum
	for i := range idx.Entries {
		idx.Entries[i].Bytes = idx.Entries[i].end - idx.Entries[i].start
		idx.EntryBytes += idx.Entries[i].Bytes
	}
	idx.Count = len(idx.Entries)
	idx.OtherBytes = idx.TotalBytes - idx.EntryBytes - idx.HeadingBytes
	return idx
}

// fenceDelimiter reports whether a line opens or closes a fenced code
// block, and with which marker. Indented fences count: a fence inside a
// list item is still a fence.
func fenceDelimiter(line string) (string, bool) {
	t := strings.TrimLeft(line, " \t")
	for _, mark := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, mark) {
			return mark, true
		}
	}
	return "", false
}

func isH3(line string) bool { return strings.HasPrefix(line, "### ") }

// isH2 excludes H3+ because "## " is a prefix of neither "### " nor
// "#### " — checked explicitly so a deeper heading cannot reset the
// section.
func isH2(line string) bool {
	return strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "###")
}

// splitHeading separates `{area} — {YYYY-MM-DD}` into title and date.
func splitHeading(text string) (title, date string) {
	text = strings.TrimSpace(text)
	if m := entryDateRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1]), m[2]
	}
	return text, ""
}

// Bodies returns the verbatim text of the named ordinals, in the order
// asked for. Out-of-range ordinals are reported rather than skipped: a
// skill that asked for entry 200 must not silently receive four entries
// and believe it got five.
func Bodies(path string, ordinals []int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	idx := scan(data)
	out := make([]string, 0, len(ordinals))
	for _, n := range ordinals {
		if n < 1 || n > len(idx.Entries) {
			return nil, fmt.Errorf("%w: %d (the file has %d entr%s)",
				ErrOutOfRange, n, len(idx.Entries), pluralY(len(idx.Entries)))
		}
		e := idx.Entries[n-1]
		out = append(out, string(data[e.start:e.end]))
	}
	return out, nil
}

// ParseOrdinals parses the `3,7,12` argument form.
func ParseOrdinals(arg string) ([]int, error) {
	fields := strings.FieldsFunc(arg, func(r rune) bool { return r == ',' || r == ' ' })
	if len(fields) == 0 {
		return nil, errors.New("no entry ordinals given")
	}
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(f), "%d", &n); err != nil {
			return nil, fmt.Errorf("%q is not an entry ordinal", f)
		}
		out = append(out, n)
	}
	return out, nil
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
