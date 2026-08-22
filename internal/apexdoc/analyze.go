package apexdoc

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Routing thresholds, matching analyze_sources.py exactly. They are
// parameters rather than constants only so a test can exercise the
// boundary without building a 15k-token fixture.
const (
	DefaultSingleMaxFiles  = 3
	DefaultSingleMaxTokens = 15000
	DefaultSplitMinTokens  = 5000
	// distillateRatio is the assumed compression: a distillate is roughly a
	// third of its sources.
	distillateRatio = 3
	// bytesPerTokenEstimate is the crude chars-per-token ratio. Every
	// number derived from it is labelled an estimate; nothing gates on it.
	bytesPerTokenEstimate = 4
)

// sourceExtensions are the file types a source document can be.
var sourceExtensions = map[string]bool{
	".md": true, ".txt": true, ".yaml": true, ".yml": true, ".json": true,
}

// skipDirs are tool and IDE directories that are never source documents.
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "__pycache__": true, ".venv": true,
	".claude": true, ".cursor": true, ".vscode": true,
}

// Routing recommendations.
const (
	RoutingSingle = "single"
	RoutingFanOut = "fan-out"
)

// Split predictions.
const (
	SplitLikely   = "likely"
	SplitUnlikely = "unlikely"
)

// Roles a document plays inside a group.
const (
	RolePrimary    = "primary"
	RoleCompanion  = "companion"
	RoleStandalone = "standalone"
)

// FileInfo is one source document.
type FileInfo struct {
	Path            string `json:"path"             yaml:"path"`
	FileName        string `json:"filename"         yaml:"filename"`
	SizeBytes       int64  `json:"size_bytes"       yaml:"size_bytes"`
	EstimatedTokens int64  `json:"estimated_tokens" yaml:"estimated_tokens"`
	DocType         string `json:"doc_type"         yaml:"doc_type"`
}

// GroupMember is a file inside a group, with the role it plays.
type GroupMember struct {
	Path string `json:"path" yaml:"path"`
	Role string `json:"role" yaml:"role"`
}

// Group is a set of documents that belong together — a brief and its
// discovery notes, say — detected from naming convention alone.
type Group struct {
	Key   string        `json:"group_key" yaml:"group_key"`
	Files []GroupMember `json:"files"     yaml:"files"`
}

// Summary totals the corpus.
type Summary struct {
	TotalFiles           int   `json:"total_files"            yaml:"total_files"`
	TotalSizeBytes       int64 `json:"total_size_bytes"       yaml:"total_size_bytes"`
	TotalEstimatedTokens int64 `json:"total_estimated_tokens" yaml:"total_estimated_tokens"`
}

// Routing is the single-vs-fan-out recommendation.
type Routing struct {
	Recommendation string `json:"recommendation" yaml:"recommendation"`
	Reason         string `json:"reason"         yaml:"reason"`
}

// SplitPrediction estimates whether the distillate will need splitting.
type SplitPrediction struct {
	Prediction                string `json:"prediction"                  yaml:"prediction"`
	Reason                    string `json:"reason"                      yaml:"reason"`
	EstimatedDistillateTokens int64  `json:"estimated_distillate_tokens" yaml:"estimated_distillate_tokens"`
}

// AnalyzeResult is the payload of `ape doc analyze`, matching
// analyze_sources.py's JSON shape so the calling skill reads the same
// fields.
type AnalyzeResult struct {
	Status          string          `json:"status"           yaml:"status"`
	Files           []FileInfo      `json:"files"            yaml:"files"`
	Summary         Summary         `json:"summary"          yaml:"summary"`
	Groups          []Group         `json:"groups"           yaml:"groups"`
	Routing         Routing         `json:"routing"          yaml:"routing"`
	SplitPrediction SplitPrediction `json:"split_prediction" yaml:"split_prediction"`
}

// AnalyzeOptions carries the thresholds.
type AnalyzeOptions struct {
	SingleMaxFiles  int
	SingleMaxTokens int64
	SplitMinTokens  int64
}

func (o AnalyzeOptions) withDefaults() AnalyzeOptions {
	if o.SingleMaxFiles <= 0 {
		o.SingleMaxFiles = DefaultSingleMaxFiles
	}
	if o.SingleMaxTokens <= 0 {
		o.SingleMaxTokens = DefaultSingleMaxTokens
	}
	if o.SplitMinTokens <= 0 {
		o.SplitMinTokens = DefaultSplitMinTokens
	}
	return o
}

// docTypeRules map a filename fragment onto a document type. Order
// matters: the first match wins, so the more specific fragments come
// first.
var docTypeRules = []struct {
	fragment string
	docType  string
}{
	{"discovery-notes", "discovery-notes"},
	{"discovery", "discovery"},
	{"retrospective", "retrospective"},
	{"retro", "retrospective"},
	{"architecture", "architecture"},
	{"brief", "brief"},
	{"prd", "prd"},
	{"epic", "epic"},
	{"story", "story"},
	{"handoff", "handoff"},
	{"plan", "plan"},
	{"adr", "adr"},
	{"pattern", "pattern"},
	// The on-disk convention is `pat-NNNN_slug.md`, which contains neither
	// "pattern" nor anything longer to match on.
	{"pat-", "pattern"},
	{"review", "review"},
}

// companionSuffixRe strips the suffixes that mark a companion document, so
// `x-brief.md` and `x-brief-discovery-notes.md` share a group key.
var companionSuffixRe = regexp.MustCompile(
	`[-_](discovery[-_]notes|discovery|notes|appendix|addendum|supplement)$`)

// DocType classifies a document from its name.
func DocType(name string) string {
	base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	for _, rule := range docTypeRules {
		if strings.Contains(base, rule.fragment) {
			return rule.docType
		}
	}
	return "document"
}

// groupKey is the base name a companion shares with its primary.
func groupKey(name string) (key string, isCompanion bool) {
	base := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	if trimmed := companionSuffixRe.ReplaceAllString(base, ""); trimmed != base {
		return trimmed, true
	}
	return base, false
}

// Analyze enumerates the inputs and reports sizes, groups, routing and a
// split prediction.
//
// Inputs may be file paths, directories (walked for source extensions), or
// glob patterns — the three forms analyze_sources.py accepts.
func Analyze(inputs []string, opts AnalyzeOptions) (*AnalyzeResult, error) {
	opts = opts.withDefaults()
	paths, err := expandInputs(inputs)
	if err != nil {
		return nil, err
	}
	res := &AnalyzeResult{Status: "ok"}
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		fi := FileInfo{
			Path:            filepath.ToSlash(path),
			FileName:        filepath.Base(path),
			SizeBytes:       info.Size(),
			EstimatedTokens: info.Size() / bytesPerTokenEstimate,
			DocType:         DocType(filepath.Base(path)),
		}
		res.Files = append(res.Files, fi)
		res.Summary.TotalFiles++
		res.Summary.TotalSizeBytes += fi.SizeBytes
		res.Summary.TotalEstimatedTokens += fi.EstimatedTokens
	}
	res.Groups = buildGroups(res.Files)
	res.Routing = decideRouting(res.Summary, opts)
	res.SplitPrediction = predictSplit(res.Summary, opts)
	return res, nil
}

// expandInputs resolves paths, directories and globs into a sorted,
// de-duplicated file list.
func expandInputs(inputs []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		if !sourceExtensions[strings.ToLower(filepath.Ext(path))] {
			return
		}
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, input := range inputs {
		info, err := os.Stat(input)
		switch {
		case err == nil && info.IsDir():
			if walkErr := walkSources(input, add); walkErr != nil {
				return nil, walkErr
			}
		case err == nil:
			add(input)
		default:
			matches, globErr := filepath.Glob(input)
			if globErr != nil {
				return nil, fmt.Errorf("bad glob %q: %w", input, globErr)
			}
			for _, m := range matches {
				if mi, statErr := os.Stat(m); statErr == nil && mi.IsDir() {
					if walkErr := walkSources(m, add); walkErr != nil {
						return nil, walkErr
					}
					continue
				}
				add(m)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func walkSources(root string, add func(string)) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		add(path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", root, err)
	}
	return nil
}

// buildGroups pairs companions with their primary. A companion with no
// primary, and a document with no companion, are both standalone — a group
// of one is not a grouping.
func buildGroups(files []FileInfo) []Group {
	type member struct {
		path        string
		isCompanion bool
	}
	byKey := map[string][]member{}
	var order []string
	for _, f := range files {
		key, isCompanion := groupKey(f.FileName)
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], member{path: f.Path, isCompanion: isCompanion})
	}
	sort.Strings(order)

	var groups []Group
	for _, key := range order {
		members := byKey[key]
		g := Group{Key: key}
		hasPrimary := false
		for _, m := range members {
			if !m.isCompanion {
				hasPrimary = true
			}
		}
		for _, m := range members {
			role := RoleStandalone
			switch {
			case len(members) == 1:
				role = RoleStandalone
			case m.isCompanion:
				role = RoleCompanion
			case hasPrimary:
				role = RolePrimary
			}
			g.Files = append(g.Files, GroupMember{Path: m.path, Role: role})
		}
		groups = append(groups, g)
	}
	return groups
}

// decideRouting is the `single` vs `fan-out` call: single when the corpus
// is BOTH small enough in file count AND in estimated tokens.
func decideRouting(s Summary, opts AnalyzeOptions) Routing {
	if s.TotalFiles <= opts.SingleMaxFiles && s.TotalEstimatedTokens <= opts.SingleMaxTokens {
		return Routing{
			Recommendation: RoutingSingle,
			Reason: fmt.Sprintf("%d file(s) (<= %d) and ~%d estimated tokens (<= %d)",
				s.TotalFiles, opts.SingleMaxFiles, s.TotalEstimatedTokens, opts.SingleMaxTokens),
		}
	}
	return Routing{
		Recommendation: RoutingFanOut,
		Reason: fmt.Sprintf("%d file(s) (> %d) or ~%d estimated tokens (> %d)",
			s.TotalFiles, opts.SingleMaxFiles, s.TotalEstimatedTokens, opts.SingleMaxTokens),
	}
}

// predictSplit estimates the distillate at a third of its sources and says
// whether that will need splitting.
func predictSplit(s Summary, opts AnalyzeOptions) SplitPrediction {
	estimate := s.TotalEstimatedTokens / distillateRatio
	if estimate > opts.SplitMinTokens {
		return SplitPrediction{
			Prediction:                SplitLikely,
			EstimatedDistillateTokens: estimate,
			Reason: fmt.Sprintf("~%d estimated distillate tokens (> %d)",
				estimate, opts.SplitMinTokens),
		}
	}
	return SplitPrediction{
		Prediction:                SplitUnlikely,
		EstimatedDistillateTokens: estimate,
		Reason: fmt.Sprintf("~%d estimated distillate tokens (<= %d)",
			estimate, opts.SplitMinTokens),
	}
}
