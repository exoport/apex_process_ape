package cost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxCoverageFiles bounds a coverage sweep. Transcripts accumulate without
// limit under ~/.claude/projects; the newest files answer "what is Claude
// Code emitting now", which is the question coverage asks. Older files are
// reported as skipped rather than silently dropped.
const maxCoverageFiles = 500

// DefaultCoverageWindow is how far back a coverage sweep looks by default.
// Long enough to survive a quiet fortnight, short enough that a model id
// retired months ago doesn't hold the release gate hostage.
const DefaultCoverageWindow = 30 * 24 * time.Hour

// ObservedModel is one model id seen in local Claude Code transcripts,
// with how this binary would price it.
//
//nolint:tagliatelle // snake_case matches the on-disk / wire contract
type ObservedModel struct {
	// Model is the normalized model id (NormalizeModel applied).
	Model string `json:"model" yaml:"model"`
	// Raw lists every distinct spelling the transcripts used for this model
	// that differs from the normalized id — a dated snapshot
	// (`claude-haiku-4-5-20251001`), an alias, a suffixed form. Sorted, and
	// empty when every occurrence was already canonical. Recording all of
	// them rather than the first one seen is what makes a normalization
	// surprise diagnosable: the interesting case is precisely when one model
	// arrives under two names.
	Raw []string `json:"raw,omitempty" yaml:"raw,omitempty"`
	// Turns is how many assistant lines named this model in the window.
	// NOT deduped by message.id — a duplicate snapshot is still evidence the
	// id is in use — so this reads higher than a billable turn count.
	Turns int `json:"turns" yaml:"turns"`
	// Source is how this binary resolves its price.
	Source PriceSource `json:"price_source" yaml:"price_source"`
	// BaseInput / Output are the resolved rate, zero when unpriced.
	BaseInput float64 `json:"base_input" yaml:"base_input"`
	Output    float64 `json:"output"     yaml:"output"`
	// LastSeen is the newest turn timestamp observed for this model.
	LastSeen time.Time `json:"last_seen,omitzero" yaml:"last_seen,omitzero"`
	// ClaudeVersion is the Claude Code version that wrote the newest turn.
	ClaudeVersion string `json:"claude_version,omitempty" yaml:"claude_version,omitempty"`
}

// AliasDrift is a family alias that names an older generation than the one
// the local Claude Code is running.
//
// Since a bare `sonnet` in a spec or `--model` resolves to a concrete id
// from the alias table, a stale entry does not merely misprice — it selects
// the wrong model. Drift is therefore a correctness signal, not a reporting
// nicety, and it fails the strict gate.
type AliasDrift struct {
	// Alias is the bare family word ("opus").
	Alias string `json:"alias" yaml:"alias"`
	// Target is the concrete id the table resolves it to.
	Target string `json:"target" yaml:"target"`
	// Newer lists the ids of that family observed locally that parse as a
	// strictly newer generation than Target. Non-empty by construction — it
	// is the evidence, not merely context.
	Newer []string `json:"newer" yaml:"newer"`
}

// CoverageReport answers "does this binary's price table cover what the
// locally-installed Claude Code is actually emitting?".
//
// This is the detector the pricing bug of 2026-07-14 needed and did not
// have. Claude Code ships on its own schedule, so a model id can change
// under a released ape at any time; no release-time check can catch that
// on its own. A coverage sweep runs against whatever Claude Code is
// installed right now, which makes it correct whenever it is run — from
// `ape doctor`, from `ape costs coverage`, or from the release gate.
//
//nolint:tagliatelle // snake_case matches the on-disk / wire contract
type CoverageReport struct {
	Models             []ObservedModel `json:"models"                   yaml:"models"`
	TranscriptsScanned int             `json:"transcripts_scanned"      yaml:"transcripts_scanned"`
	TranscriptsSkipped int             `json:"transcripts_skipped"      yaml:"transcripts_skipped"`
	Since              time.Time       `json:"since,omitzero"           yaml:"since,omitzero"`
	TableUpdated       string          `json:"price_table_updated"      yaml:"price_table_updated"`
	ClaudeVersion      string          `json:"claude_version,omitempty" yaml:"claude_version,omitempty"`
	ExactModels        int             `json:"exact_models"             yaml:"exact_models"`
	EstimatedModels    int             `json:"estimated_models"         yaml:"estimated_models"`
	UnpricedModels     int             `json:"unpriced_models"          yaml:"unpriced_models"`
	// AliasDrifts lists family aliases superseded by a newer generation seen
	// locally. Empty on a healthy sweep — and empty when an OLDER generation
	// is pinned deliberately, which is not drift.
	AliasDrifts []AliasDrift `json:"alias_drifts,omitempty" yaml:"alias_drifts,omitempty"`
}

// OK reports whether every observed model has an exact rate AND every
// family alias points at a generation the local Claude Code is running.
// Alias drift counts because a bare `sonnet` resolves through that table:
// a stale entry selects the wrong model, not merely the wrong price.
func (r CoverageReport) OK() bool {
	return r.EstimatedModels == 0 && r.UnpricedModels == 0 && len(r.AliasDrifts) == 0
}

// Observed reports whether the sweep found any transcripts at all. A sweep
// that saw nothing is not evidence of health — callers (notably the
// release gate and CI) must treat it as "skipped", never "passed".
func (r CoverageReport) Observed() bool { return r.TranscriptsScanned > 0 }

// Gaps returns the observed models that are not exactly priced, worst
// first (unpriced before estimated), each ordered by turn count.
func (r CoverageReport) Gaps() []ObservedModel {
	var out []ObservedModel
	for _, m := range r.Models {
		if !m.Source.Exact() {
			out = append(out, m)
		}
	}
	return out
}

// Summary renders a one-line verdict for a human or a CI log.
func (r CoverageReport) Summary() string {
	if !r.Observed() {
		return "no Claude Code transcripts in the window — coverage not verified"
	}
	if r.OK() {
		return fmt.Sprintf("all %d observed model(s) exactly priced (%d transcript(s), table updated %s)",
			r.ExactModels, r.TranscriptsScanned, r.TableUpdated)
	}
	return fmt.Sprintf("%d model(s) unpriced, %d estimated, %d exact, %d alias drift(s) (%d transcript(s), table updated %s)",
		r.UnpricedModels, r.EstimatedModels, r.ExactModels, len(r.AliasDrifts),
		r.TranscriptsScanned, r.TableUpdated)
}

// ObserveModels sweeps the local Claude Code transcript tree and reports
// how this binary's price table covers every model id it finds.
//
// home defaults to the user's home directory when empty. Only transcripts
// modified at/after `since` are read; a zero `since` reads everything
// (bounded by maxCoverageFiles, newest first). A missing
// ~/.claude/projects is not an error — it yields an empty report whose
// Observed() is false.
func ObserveModels(home string, since time.Time) (CoverageReport, error) {
	rep := CoverageReport{Since: since, TableUpdated: PriceTableUpdated}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return rep, fmt.Errorf("cost.ObserveModels: %w", err)
		}
		home = h
	}
	paths, skipped, err := coverageTranscripts(home, since)
	if err != nil {
		return rep, err
	}
	rep.TranscriptsSkipped = skipped

	type acc struct {
		turns    int
		raws     map[string]bool
		lastSeen time.Time
		version  string
	}
	seen := map[string]*acc{}
	for _, p := range paths {
		if !accumulateModels(p, func(model, raw string, ts time.Time, version string) {
			a := seen[model]
			if a == nil {
				a = &acc{raws: map[string]bool{}}
				seen[model] = a
			}
			if raw != model {
				a.raws[raw] = true
			}
			a.turns++
			if ts.After(a.lastSeen) {
				a.lastSeen = ts
				if version != "" {
					a.version = version
				}
			}
		}) {
			continue
		}
		rep.TranscriptsScanned++
	}

	for model, a := range seen {
		price, src := LookupSourceAt(model, a.lastSeen)
		om := ObservedModel{
			Model:         model,
			Turns:         a.turns,
			Source:        src,
			BaseInput:     price.BaseInput,
			Output:        price.Output,
			LastSeen:      a.lastSeen,
			ClaudeVersion: a.version,
		}
		if len(a.raws) > 0 {
			om.Raw = make([]string, 0, len(a.raws))
			for r := range a.raws {
				om.Raw = append(om.Raw, r)
			}
			sort.Strings(om.Raw)
		}
		rep.Models = append(rep.Models, om)
		switch {
		case !src.Priced():
			rep.UnpricedModels++
		case src.Estimated():
			rep.EstimatedModels++
		default:
			rep.ExactModels++
		}
	}
	// Worst first (unpriced, then estimated, then exact), then by turn
	// count descending, then by id — stable output for diffing in CI.
	sort.Slice(rep.Models, func(i, j int) bool {
		a, b := rep.Models[i], rep.Models[j]
		if ra, rb := sourceRank(a.Source), sourceRank(b.Source); ra != rb {
			return ra < rb
		}
		if a.Turns != b.Turns {
			return a.Turns > b.Turns
		}
		return a.Model < b.Model
	})
	// Report the Claude Code version that wrote the newest observed turn.
	// Computed over the sorted slice rather than the map so the answer does
	// not depend on iteration order; the zero-timestamp case falls back to
	// the first version seen rather than reporting none.
	var newest time.Time
	for _, m := range rep.Models {
		if m.ClaudeVersion == "" {
			continue
		}
		if rep.ClaudeVersion == "" || m.LastSeen.After(newest) {
			newest = m.LastSeen
			rep.ClaudeVersion = m.ClaudeVersion
		}
	}
	rep.AliasDrifts = detectAliasDrift(rep.Models)
	return rep, nil
}

// detectAliasDrift finds family aliases that name an older generation than
// the one the local Claude Code is actually running.
//
// The condition is "a strictly NEWER sibling of this family was observed",
// not "the alias target was absent". Absence proves nothing: pinning
// `claude-opus-4-8` in every spec means `claude-opus-5` never appears, and
// flagging that would fail the release gate over a deliberate choice. Only a
// newer generation in the wild says the table has fallen behind.
//
// Ordering comes from parseGeneration, which reads the numeric segments of a
// `claude-<family>-<major>[-<minor>…]` id. Any id that does not parse that
// way — a legacy `claude-3-5-sonnet`, a future scheme — yields no comparison
// and therefore no claim. Silence on an id we cannot order is the correct
// answer; a guess is not.
func detectAliasDrift(observed []ObservedModel) []AliasDrift {
	byFamily := map[string][]string{}
	for _, m := range observed {
		if fam := ModelFamily(m.Model); fam != "" {
			byFamily[fam] = append(byFamily[fam], m.Model)
		}
	}
	drifts := make([]AliasDrift, 0, len(modelAliases))
	for alias, target := range modelAliases {
		targetGen, ok := parseGeneration(target, alias)
		if !ok {
			continue // cannot order the target; claim nothing
		}
		var newer []string
		for _, sibling := range byFamily[alias] {
			if sibling == target {
				continue
			}
			gen, parsed := parseGeneration(sibling, alias)
			if parsed && compareGenerations(gen, targetGen) > 0 {
				newer = append(newer, sibling)
			}
		}
		if len(newer) == 0 {
			continue
		}
		drifts = append(drifts, AliasDrift{Alias: alias, Target: target, Newer: newer})
	}
	sort.Slice(drifts, func(i, j int) bool { return drifts[i].Alias < drifts[j].Alias })
	return drifts
}

// parseGeneration extracts the numeric generation segments of a model id
// within a family: ("claude-opus-4-8", "opus") → ([4, 8], true).
//
// ok=false when the id is not `claude-<family>-<digits>[-<digits>…]` — which
// covers the legacy generation-first ordering (`claude-3-5-sonnet`), sentinels
// (`<synthetic>`), and any future naming scheme. Callers must treat that as
// "not comparable" rather than as equal or older.
func parseGeneration(model, family string) ([]int, bool) {
	rest, found := strings.CutPrefix(model, "claude-"+family+"-")
	if !found || rest == "" {
		return nil, false
	}
	segs := strings.Split(rest, "-")
	out := make([]int, 0, len(segs))
	for _, seg := range segs {
		n, err := strconv.Atoi(seg)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// compareGenerations orders two parsed generations, returning >0 when a is
// newer, <0 when b is newer, 0 when equal.
//
// Segments compare numerically left to right, so [5] outranks [4, 8]
// (Opus 5 is newer than Opus 4.8 — the case a string sort gets backwards).
// When one is a prefix of the other the longer wins: [5, 1] is newer than
// [5], matching how a point release relates to its base.
func compareGenerations(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] > b[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case len(a) > len(b):
		return 1
	case len(a) < len(b):
		return -1
	default:
		return 0
	}
}

// sourceRank orders price sources worst-first for reporting.
func sourceRank(s PriceSource) int {
	switch {
	case !s.Priced():
		return 0
	case s.Estimated():
		return 1
	default:
		return 2
	}
}

// accumulateModels reads one transcript and calls emit for every assistant
// turn's model. Returns false when the file could not be opened. Turns are
// NOT deduped by message.id: coverage counts observations of a model id,
// not billable turns, and a duplicate snapshot is still evidence that the
// id is in use.
func accumulateModels(path string, emit func(model, raw string, ts time.Time, version string)) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		// Cheap pre-filter before the JSON decode. A transcript is mostly user
		// turns, tool results, and metadata; only assistant lines carry a
		// `model` key, and unmarshalling every other line into AssistantLine
		// dominated the sweep (2.3s of a 2.35s `ape doctor` run). A line
		// without the literal `"model"` cannot have the field, so skipping it
		// is exact, not heuristic — no line that would have emitted is lost.
		if !bytes.Contains(line, modelKeyBytes) {
			continue
		}
		var al AssistantLine
		if err := json.Unmarshal(line, &al); err != nil {
			continue
		}
		if al.Type != "assistant" || al.IsMeta || al.Message.Model == "" {
			continue
		}
		emit(NormalizeModel(al.Message.Model), al.Message.Model, parseTurnTime(al.Timestamp), al.Version)
	}
	return true
}

// modelKeyBytes is the JSON key an assistant line must contain to name a
// model. Hoisted so the hot loop does not re-allocate it per line.
var modelKeyBytes = []byte(`"model"`)

// coverageTranscripts enumerates the transcript files to sweep: every main
// session under ~/.claude/projects/<hash>/<sid>.jsonl plus its sub-agent
// transcripts, filtered by mtime and capped at maxCoverageFiles (newest
// first). Returns the paths and the number dropped by the cap.
func coverageTranscripts(home string, since time.Time) (paths []string, skipped int, err error) {
	root := filepath.Join(home, ".claude", "projects")
	globs := []string{
		filepath.Join(root, "*", "*.jsonl"),
		filepath.Join(root, "*", "*", "subagents", "agent-*.jsonl"),
	}
	type cand struct {
		path  string
		mtime time.Time
	}
	var cands []cand
	for _, g := range globs {
		matches, gerr := filepath.Glob(g)
		if gerr != nil {
			return nil, 0, fmt.Errorf("cost.ObserveModels: glob: %w", gerr)
		}
		for _, p := range matches {
			info, statErr := os.Stat(p)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			if !since.IsZero() && info.ModTime().Before(since) {
				continue
			}
			cands = append(cands, cand{path: p, mtime: info.ModTime()})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mtime.After(cands[j].mtime) })
	if len(cands) > maxCoverageFiles {
		skipped = len(cands) - maxCoverageFiles
		cands = cands[:maxCoverageFiles]
	}
	for _, c := range cands {
		paths = append(paths, c.path)
	}
	return paths, skipped, nil
}
