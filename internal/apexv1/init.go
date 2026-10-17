package apexv1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/runlog"
)

// Hook commands `ape project init` writes. Literal `ape`: inside an
// `ape task` session PATH starts with the running binary (selfpath).
const (
	HookCmdSessionStart = "ape hook session-start"
	HookCmdStop         = "ape hook stop"
	HookCmdPreTool      = "ape hook pre-tool"
	PreToolMatcher      = "Edit|Write|MultiEdit|NotebookEdit|Bash"
	// StopHookTimeout is the stop hook's timeout in seconds: it re-runs
	// the change's short command checks, within StopRunBudget (600s), and
	// never a long one (D58 2.5) — so it is not derived from
	// timeouts.check, as D55 proposed: a longer timeout would only let a
	// wedged hook hold the session longer.
	StopHookTimeout = 1800
)

// InitResult lists what `ape project init` created and what it left alone.
type InitResult struct {
	Root       string   `json:"root"`
	Created    []string `json:"created"`
	Kept       []string `json:"kept"`
	HooksAdded []string `json:"hooks_added"`
	Brownfield bool     `json:"brownfield"`
	Commit     string   `json:"commit,omitempty"`
	// Amended lists existing files init changed: only a CLAUDE.md missing
	// the @AGENTS.md import.
	Amended []string `json:"amended,omitempty"`
}

// hasAgentsImport reports whether a CLAUDE.md imports the map.
func hasAgentsImport(doc string) bool {
	for l := range strings.SplitSeq(doc, "\n") {
		if strings.TrimSpace(l) == "@AGENTS.md" {
			return true
		}
	}
	return false
}

const projectSkeleton = `# %s

## Vision
<!-- one paragraph: what this is and why it exists -->

## Users
<!-- who uses it, and for what -->

## Scope
<!-- what it does -->

## Out of scope
<!-- what it deliberately does not do -->

## Constraints
<!-- hard limits: platforms, performance, compliance, NEVER rules that shape the product -->

## Stack
<!-- languages, frameworks, runtime -->
`

const agentsSkeleton = `# AGENTS.md

<!-- The project map, at most 120 lines: what this is, how to build, test and run the e2e
     checks, the NEVER rules, and pointers. Detail lives in development/ and docs/. -->

## What this is

## Commands
<!-- build / test / e2e, one line each -->

## NEVER

## Pointers
- development/project.md — vision, users, scope, constraints
- development/roadmap.md — milestones and their gates
- development/specs/ — living behaviour specs; they change only through ` + "`ape change archive`" + `
- development/changes/ — units of change; ` + "`ape status`" + ` shows what is in flight
- development/archive/ — archived and abandoned changes (read-only)
- development/decisions/ — ADRs
- development/briefs/ — each plan's input, dated and verbatim: history, never edited
`

// Init creates the v1 skeleton where absent and merges ape's hooks into
// .claude/settings.json. It never overwrites a file.
func Init(ctx context.Context, root string, brownfield bool) (*InitResult, error) {
	res, p, err := initSkeleton(ctx, root, brownfield)
	if err != nil {
		return res, err
	}
	if paths := p.initCommitPaths(res); len(paths) > 0 {
		sha, err := p.commitRecord(ctx, "apex: project init", "", paths...)
		if err != nil {
			return res, &FailError{Code: CodeCommitFailed, Msg: "skeleton written but its commit failed: " + err.Error()}
		}
		res.Commit = sha
	}
	return res, nil
}

// initSkeleton is Init without its commit: `ape project lift` lays the
// same skeleton down inside the lift commit.
func initSkeleton(ctx context.Context, root string, brownfield bool) (*InitResult, *Project, error) {
	p, err := Open(root)
	if err != nil {
		return nil, nil, err
	}
	res := &InitResult{Root: p.Root, Brownfield: brownfield}
	name := filepath.Base(p.Root)
	if cfg, err := LoadConfig(p.Root); err == nil && cfg.ProjectName != "" {
		name = cfg.ProjectName
	}

	write := func(path, content string) error {
		if fileOrDirExists(path) {
			res.Kept = append(res.Kept, p.Rel(path))
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(path, content); err != nil {
			return err
		}
		res.Created = append(res.Created, p.Rel(path))
		return nil
	}
	dir := func(path string, keep bool) error {
		if dirExists(path) {
			return nil
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		res.Created = append(res.Created, p.Rel(path)+"/")
		if keep {
			return writeFileAtomic(filepath.Join(path, ".gitkeep"), "")
		}
		return nil
	}

	if err := write(ConfigPath(p.Root), "project_name: "+yamlScalar(name)+"\ndevelopment_folder: "+p.DevRel+"\n"); err != nil {
		return nil, nil, err
	}
	for _, d := range []string{p.SpecsDir(), p.ChangesDir(), p.ArchiveDir(), p.DecisionsDir(), p.BriefsDir()} {
		if err := dir(d, true); err != nil {
			return nil, nil, err
		}
	}
	if err := dir(p.StateDir(), false); err != nil {
		return nil, nil, err
	}
	steps := []struct{ path, content string }{
		{p.ProjectMD(), fmt.Sprintf(projectSkeleton, name)},
		{p.RoadmapMD(), "# Roadmap\n"},
		{p.AgentsMD(), agentsSkeleton},
		{p.ClaudeMD(), "@AGENTS.md\n"},
		// A whitelist: whatever ape adds to _apex/state/ later is ignored
		// unless it is named here.
		{p.StateGitignore(), runlog.V1StateGitignore},
	}
	for _, s := range steps {
		if err := write(s.path, s.content); err != nil {
			return nil, nil, err
		}
	}
	// A CLAUDE.md that existed without the import gets the one line it
	// needs, and nothing else (item 15, OpenSpec Q8): the project's own
	// instructions stay as they are.
	if doc, err := readFileString(p.ClaudeMD()); err == nil && !hasAgentsImport(doc) {
		if !strings.HasSuffix(doc, "\n") && doc != "" {
			doc += "\n"
		}
		if err := writeFileAtomic(p.ClaudeMD(), doc+"@AGENTS.md\n"); err != nil {
			return nil, nil, err
		}
		res.Amended = append(res.Amended, "CLAUDE.md")
	}
	if !fileExists(p.ProjectJSON()) {
		rec := map[string]any{"initialized_at": Stamp(), "brownfield": brownfield, "base_sha": HeadSHA(ctx, p.Root)}
		data, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		if err := writeFileAtomic(p.ProjectJSON(), string(data)+"\n"); err != nil {
			return nil, nil, err
		}
		res.Created = append(res.Created, p.Rel(p.ProjectJSON()))
	} else {
		res.Kept = append(res.Kept, p.Rel(p.ProjectJSON()))
	}
	added, err := MergeHooks(p.SettingsJSON())
	if err != nil {
		return nil, nil, err
	}
	res.HooksAdded = added
	return res, p, nil
}

// initCommitPaths is what `apex: project init` commits, so the tree is
// clean after it (D11): only what init itself wrote — a brownfield repo's
// own dirty files stay as they were. project.json is ignored (the
// whitelist), so it is not staged; the whitelist always is, in case
// another ape command wrote it before init ran.
func (p *Project) initCommitPaths(res *InitResult) []string {
	var paths []string
	for _, c := range append(append([]string(nil), res.Created...), res.Amended...) {
		if c != p.Rel(p.ProjectJSON()) {
			paths = append(paths, c)
		}
	}
	if fileExists(p.StateGitignore()) {
		paths = append(paths, p.Rel(p.StateGitignore()))
	}
	if len(res.HooksAdded) > 0 {
		paths = append(paths, p.Rel(p.SettingsJSON()))
	}
	return paths
}

func yamlScalar(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(data)
}
