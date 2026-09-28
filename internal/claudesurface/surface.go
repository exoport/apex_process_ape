// Package claudesurface keeps a reviewed record of the installed Claude
// Code's surface — its tool list and the environment variables its binary
// reads — and of which of its CHANGELOG entries someone has read, so a
// Claude Code release that moves any of it is noticed before a run finds
// out.
//
// # Why
//
// ape's other live gates assert couplings ape already knows it has (the
// ready signals, the hook fields, the effort table, the reap switch,
// foreground sub-agents). They cannot see a change nobody knew to assert.
// Two did real damage before this existed: claude 2.1.277 removed the
// TaskOutput tool that skills polled background agents through, and
// interactive sessions had long forced every Agent call async regardless of
// run_in_background. The CHANGELOG mentioned the area of both
// ("Removed the deprecated TaskOutput tool"; "Subagent forking is now on by
// default … agent spawns in interactive sessions now run in the background
// by default") — nothing read it.
//
// # The three checks
//
//   - tools: the init event's tool list, diffed against the baseline. Any
//     change fails until reviewed: a tool list moves rarely, and a removal
//     breaks every skill naming the tool.
//   - env: the CLAUDE_* names in the binary, diffed against the baseline.
//     Only a variable ape SETS disappearing fails (it would silently stop
//     applying); other additions and removals are listed for the review.
//   - changelog: the entries matching Relevant, in every version newer than
//     the baseline's ReviewedThrough up to the installed one. Any fails
//     until someone reads them and records the review.
//   - models: the claude-<family>-<generation> ids in the binary, diffed
//     against the baseline. An added id fails until reviewed: it is a model
//     Claude Code can now select, and ape's price table, family aliases and
//     effort keys all have to learn it. Sonnet 5.5 shipped with no
//     CHANGELOG entry and no tool change, and ape kept pinning `sonnet` to
//     Sonnet 5 until a person asked.
//
// Recording a review is `make update-claude-surface`, which rewrites the
// baseline from the installed claude. The baseline is committed, so a
// review is a diff someone approved.
package claudesurface

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// Baseline is the reviewed surface, committed as JSON.
//
//nolint:tagliatelle // snake_case matches the repo's other committed records
type Baseline struct {
	// ReviewedThrough is the newest Claude Code version whose CHANGELOG
	// entries have been read, and whose tools and env vars these are.
	ReviewedThrough string   `json:"reviewed_through"`
	Tools           []string `json:"tools"`
	EnvVars         []string `json:"env_vars"`
	Models          []string `json:"models"`
}

// Load reads a baseline file.
func Load(path string) (*Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &b, nil
}

// Save writes a baseline file, sorted, with a trailing newline.
func (b *Baseline) Save(path string) error {
	b.Tools = sortedUnique(b.Tools)
	b.EnvVars = sortedUnique(b.EnvVars)
	b.Models = sortedUnique(b.Models)
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644) //nolint:gosec // a committed, reviewed record
}

// Diff returns what is in now but not in before, and what is in before but
// not in now, each sorted.
func Diff(before, now []string) (added, removed []string) {
	b, n := set(before), set(now)
	for k := range n {
		if !b[k] {
			added = append(added, k)
		}
	}
	for k := range b {
		if !n[k] {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// envName is a variable name as Claude Code's binary spells it. The two
// prefixes are the ones it reads its own configuration under; ANTHROPIC_*
// is API plumbing and too noisy to diff. A few matches carry a stray
// trailing character from adjacent binary data (CLAUDE_AI_ORIGINI beside
// CLAUDE_AI_ORIGIN); that noise is why only the variables ape sets can fail
// the env check, and the rest is listed for review.
var envName = regexp.MustCompile(`\bCLAUDE_(?:CODE_)?[A-Z0-9]+(?:_[A-Z0-9]+)*\b`)

// EnvVars returns every distinct CLAUDE_* name in r (the claude binary),
// sorted. The binary is read in windows that overlap by more than any name
// is long: a match starting inside the overlap is left to the next window,
// which holds it whole, so a name cut at a window's end is never recorded
// truncated.
func EnvVars(r io.Reader) ([]string, error) {
	return envVars(r, 1<<20)
}

func envVars(r io.Reader, chunk int) ([]string, error) {
	return scanBinary(r, envName, chunk, func(s string) string { return s })
}

// modelID is a model id as Claude Code's binary spells it: a family ape
// knows, then numeric generation segments. A dated snapshot
// (claude-haiku-4-5-20251001) is recorded as its base id, which is how it
// bills and how the price table keys it. Like envName, a few matches carry a
// stray digit from adjacent data (claude-haiku-3-55); the list is reviewed,
// so that noise is recorded once and stays quiet.
var modelID = regexp.MustCompile(`\bclaude-(?:fable|mythos|opus|sonnet|haiku)-[0-9]+(?:-[0-9]+)*\b`)

var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// ModelIDs returns every distinct claude model id in r (the claude binary),
// dated snapshots folded onto their base id, sorted.
func ModelIDs(r io.Reader) ([]string, error) {
	return modelIDs(r, 1<<20)
}

func modelIDs(r io.Reader, chunk int) ([]string, error) {
	return scanBinary(r, modelID, chunk, func(s string) string { return dateSuffix.ReplaceAllString(s, "") })
}

func scanBinary(r io.Reader, re *regexp.Regexp, chunk int, norm func(string) string) ([]string, error) {
	const overlap = 256
	found := map[string]bool{}
	var carry []byte
	buf := make([]byte, chunk)
	for {
		n, err := io.ReadFull(r, buf)
		final := err != nil
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		data := append(append([]byte(nil), carry...), buf[:n]...)
		cut := len(data) - overlap
		for _, loc := range re.FindAllIndex(data, -1) {
			if final || loc[0] < cut {
				found[norm(string(data[loc[0]:loc[1]]))] = true
			}
		}
		if final {
			break
		}
		carry = data[max(cut, 0):]
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// ToolsFromStreamJSON returns the tool list of the first `system`/`init`
// event in a `claude -p --output-format stream-json --verbose` stream, sorted,
// without MCP tools (they depend on the machine's configuration, not on the
// Claude Code release), and the version that event reports.
func ToolsFromStreamJSON(r io.Reader) (tools []string, version string, err error) {
	ev, err := InitFromStreamJSON(r)
	return ev.Tools, ev.Version, err
}

// Init is what ape reads from the first `system`/`init` event of a
// `claude -p --output-format stream-json --verbose` stream.
type Init struct {
	Tools   []string // sorted, MCP tools dropped
	Version string   // claude_code_version
	// Model is the id the session resolved --model to: `sonnet` comes back
	// as claude-sonnet-5-5. A word Claude Code has no alias for comes back
	// unchanged (`mythos` on an account without access), as does an unknown id.
	Model string
}

// InitFromStreamJSON reads the first `system`/`init` event of r.
func InitFromStreamJSON(r io.Reader) (Init, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type    string   `json:"type"`
			Subtype string   `json:"subtype"`
			Version string   `json:"claude_code_version"` //nolint:tagliatelle // Claude Code's field name
			Tools   []string `json:"tools"`
			Model   string   `json:"model"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type != "system" || ev.Subtype != "init" {
			continue
		}
		var tools []string
		for _, t := range ev.Tools {
			if !strings.HasPrefix(t, "mcp__") {
				tools = append(tools, t)
			}
		}
		return Init{Tools: sortedUnique(tools), Version: ev.Version, Model: ev.Model}, nil
	}
	if err := sc.Err(); err != nil {
		return Init{}, err
	}
	return Init{}, errors.New("no system/init event in the stream")
}

// Section is one `## <version>` block of Claude Code's CHANGELOG.
type Section struct {
	Version string
	Entries []string
}

var versionHeading = regexp.MustCompile(`^##\s+v?(\d+\.\d+\.\d+)\s*$`)

// ParseChangelog splits Claude Code's CHANGELOG.md into sections. Entries
// are the `- ` bullet lines, without the marker.
func ParseChangelog(text []byte) []Section {
	var out []Section
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := versionHeading.FindStringSubmatch(line); m != nil {
			out = append(out, Section{Version: m[1]})
			continue
		}
		if len(out) == 0 {
			continue
		}
		if entry, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
			out[len(out)-1].Entries = append(out[len(out)-1].Entries, entry)
		}
	}
	return out
}

// Relevant matches the CHANGELOG entries that touch what ape and the
// framework's skills drive Claude Code through: sub-agents and background
// work, tools, hooks, sessions and transcripts, the settings and flags ape
// passes, effort, output styles, and environment variables.
var Relevant = regexp.MustCompile(`(?i)\b(` +
	`sub-?agents?|agent tool|task tool|background|run_in_background|fork(ed|ing)?|teammates?|` +
	`task-?notification|notif(y|ied|ication)s?|` +
	`tool (is |was )?(removed|renamed|deprecated)|removed the|deprecated|` +
	`hooks?|Stop|SubagentStop|SessionStart|UserPromptSubmit|PreToolUse|PostToolUse|` +
	`transcripts?|--settings|settings\.json|modelSettings|effort(Level)?|output ?styles?|` +
	`--dangerously-skip-permissions|bypass permissions|permission mode|` +
	`--model|model alias|CLAUDE_[A-Z0-9_]+` +
	`)\b`)

// Entry is one relevant CHANGELOG line with the version it shipped in.
type Entry struct {
	Version string
	Text    string
}

// RelevantSince returns the entries matching Relevant in every section newer
// than after and no newer than upto, newest first. An empty after means no
// review has been recorded, and every section up to upto counts.
func RelevantSince(sections []Section, after, upto string) []Entry {
	var out []Entry
	for _, s := range sections {
		if after != "" && Compare(s.Version, after) <= 0 {
			continue
		}
		if upto != "" && Compare(s.Version, upto) > 0 {
			continue
		}
		for _, e := range s.Entries {
			if Relevant.MatchString(e) {
				out = append(out, Entry{Version: s.Version, Text: e})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int { return Compare(b.Version, a.Version) })
	return out
}

// Compare orders two Claude Code versions (`2.1.283`, with or without a
// leading v and a trailing ` (Claude Code)`).
func Compare(a, b string) int {
	return semver.Compare(canonical(a), canonical(b))
}

// Canonical returns the bare `X.Y.Z` of a `claude --version` line.
func Canonical(v string) string {
	return strings.TrimPrefix(canonical(v), "v")
}

func canonical(v string) string {
	v, _, _ = strings.Cut(strings.TrimSpace(v), " ")
	return "v" + strings.TrimPrefix(v, "v")
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func sortedUnique(xs []string) []string {
	out := slices.Clone(xs)
	sort.Strings(out)
	return slices.Compact(out)
}
