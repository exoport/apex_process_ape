package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Hook commands `ape project init` writes. Literal `ape`: inside an
// `ape task` session PATH starts with the running binary (selfpath).
const (
	HookCmdSessionStart = "ape hook session-start"
	HookCmdStop         = "ape hook stop"
	HookCmdPreTool      = "ape hook pre-tool"
	PreToolMatcher      = "Edit|Write|MultiEdit|NotebookEdit"
	// StopHookTimeout is the stop hook's timeout in seconds: it runs the
	// change's command checks, which can take minutes.
	StopHookTimeout = 1800
)

// InitResult lists what `ape project init` created and what it left alone.
type InitResult struct {
	Root       string   `json:"root"`
	Created    []string `json:"created"`
	Kept       []string `json:"kept"`
	HooksAdded []string `json:"hooks_added"`
	Brownfield bool     `json:"brownfield"`
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
- development/decisions/ — ADRs
`

// Init creates the v1 skeleton where absent and merges ape's hooks into
// .claude/settings.json. It never overwrites a file.
func Init(ctx context.Context, root string, brownfield bool) (*InitResult, error) {
	p, err := Open(root)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	for _, d := range []string{p.SpecsDir(), p.ArchiveDir(), p.DecisionsDir()} {
		if err := dir(d, true); err != nil {
			return nil, err
		}
	}
	if err := dir(p.ApexDir(), false); err != nil {
		return nil, err
	}
	steps := []struct{ path, content string }{
		{p.ProjectMD(), fmt.Sprintf(projectSkeleton, name)},
		{p.RoadmapMD(), "# Roadmap\n"},
		{p.AgentsMD(), agentsSkeleton},
		{p.ClaudeMD(), "@AGENTS.md\n"},
		{filepath.Join(p.ApexDir(), ".gitignore"), "logs/\nstop-gate/\n"},
	}
	for _, s := range steps {
		if err := write(s.path, s.content); err != nil {
			return nil, err
		}
	}
	if !fileExists(p.ProjectJSON()) {
		rec := map[string]any{"initialized_at": Stamp(), "brownfield": brownfield, "base_sha": HeadSHA(ctx, p.Root)}
		data, _ := json.MarshalIndent(rec, "", "  ")
		if err := writeFileAtomic(p.ProjectJSON(), string(data)+"\n"); err != nil {
			return nil, err
		}
		res.Created = append(res.Created, p.Rel(p.ProjectJSON()))
	} else {
		res.Kept = append(res.Kept, p.Rel(p.ProjectJSON()))
	}
	added, err := MergeHooks(p.SettingsJSON())
	if err != nil {
		return nil, err
	}
	res.HooksAdded = added
	return res, nil
}

func yamlScalar(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(data)
}

type hookWant struct {
	event, matcher, command string
	timeout                 int
}

var wantedHooks = []hookWant{
	{"SessionStart", "", HookCmdSessionStart, 0},
	{"Stop", "", HookCmdStop, StopHookTimeout},
	{"SubagentStop", "", HookCmdStop, StopHookTimeout},
	{"PreToolUse", PreToolMatcher, HookCmdPreTool, 0},
}

// MergeHooks adds ape's hooks to a settings.json, keeping every other key
// and hook. A hook whose command is already registered for its event is
// left as it is. Returns "<Event>: <command>" for each one added.
func MergeHooks(path string) ([]string, error) {
	root := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, &FailError{Msg: path + " is not valid JSON (" + err.Error() + ") — fix it, then run `ape project init` again"}
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	var added []string
	for _, w := range wantedHooks {
		groups, _ := hooks[w.event].([]any)
		if hasHookCommand(groups, w.command) {
			continue
		}
		cmd := map[string]any{"type": "command", "command": w.command}
		if w.timeout > 0 {
			cmd["timeout"] = w.timeout
		}
		group := map[string]any{"hooks": []any{cmd}}
		if w.matcher != "" {
			group["matcher"] = w.matcher
		}
		hooks[w.event] = append(groups, group)
		added = append(added, w.event+": "+w.command)
	}
	if len(added) == 0 {
		return nil, nil
	}
	root["hooks"] = hooks
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return added, writeFileAtomic(path, string(out)+"\n")
}

func hasHookCommand(groups []any, command string) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == command {
				return true
			}
		}
	}
	return false
}
