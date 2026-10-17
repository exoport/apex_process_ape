package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Running a tool (D74, D77): `ape change tool <id> <name>` runs a
// tools.design or tools.review entry for a change in its own detached
// session, `ape task <skill> --args "<filled>"`, the args filled from the
// entry's template (argstemplate.go) — by default `--change {id}`, plus
// ` --task {task}` when a task is given. A design tool is run for the task
// whose line names it (`— tool: <name>`); a review tool, for a change whose
// review_tools: name it, before the review. The session writes what the
// skill writes (a design tool's mock-up, a review tool's report in
// assets/): no read-only rule, and no verdict for ape to settle.

// DefaultToolArgs is a tool entry's args when unset; with a task,
// DefaultToolTaskArgs is appended.
const (
	DefaultToolArgs     = "--change {id}"
	DefaultToolTaskArgs = " --task {task}"
)

// ToolPlan is what `ape change tool` starts.
type ToolPlan struct {
	Change string
	Name   string
	// Slot is design or review.
	Slot  string
	Skill string
	Task  string
	Args  string
	Model SkillModel
}

// PrepareTool resolves tool name for change id (in flight) and fills its
// args: tool.unknown when no tools.design or tools.review entry has the
// name; usage.no-such-task for a task the change does not have.
func (p *Project) PrepareTool(ctx context.Context, id, name, task string) (*ToolPlan, error) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return nil, err
	}
	tools := toolsOf(cfg, p.Root)
	slot, entry := "design", tools.Find("design", name)
	if entry == nil {
		slot, entry = "review", tools.Find("review", name)
	}
	if entry == nil {
		return nil, &FailError{Code: "tool.unknown", Msg: "no tools.design or tools.review entry in _apex/config.yaml is named " + name +
			" — name a declared tool (`ape plugins status` lists them), or re-plan without it"}
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if task != "" && ci.Doc.Task(task) == nil {
		return nil, &UsageError{Code: "usage.no-such-task", Msg: "change " + id + " has no task " + task}
	}
	// TODO(rc3-d merge): a design tool runs for a task whose line names it
	// — refuse `--task` (or its absence) when ci.Doc.Task(task).Tool !=
	// name, once Task.Tool (rc3-d's `— tool:` parser) is on this branch.
	tmpl := entry.Args
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultToolArgs
		if task != "" {
			tmpl += DefaultToolTaskArgs
		}
	}
	if strings.Contains(tmpl, "{task}") && task == "" {
		return nil, &UsageError{Code: "usage.task", Msg: "tool " + name + "'s args (" + tmpl + ") use {task} — give --task T<n>"}
	}
	args, err := FillArgs(tmpl, ArgsVarsFor(id, task, p.Rel(p.ChangeDir(id))))
	if err != nil {
		return nil, &FailError{Code: "tool.args", Msg: "tool " + name + ": " + err.Error() + " — a person fixes the tools: entry in _apex/config.yaml"}
	}
	m := cfg.Models[entry.Skill]
	if v := strings.TrimSpace(entry.Model); v != "" {
		m.Model = v
	}
	if v := strings.TrimSpace(entry.Effort); v != "" {
		m.Effort = v
	}
	return &ToolPlan{Change: id, Name: name, Slot: slot, Skill: entry.Skill, Task: task, Args: args, Model: m}, nil
}

// ToolRun is ape's record of the tool session it started:
// _apex/state/logs/<id>/tool-<name>.json (ignored). A second call attaches
// to it while it runs; it is cleared once its end is reported.
type ToolRun struct {
	Change     string `json:"change"`
	Name       string `json:"name"`
	Slot       string `json:"slot"`
	Skill      string `json:"skill"`
	Task       string `json:"task,omitempty"`
	Args       string `json:"args"`
	DetachedID string `json:"detached_id"`
	StartedAt  string `json:"started_at"`
	Parent     string `json:"parent,omitempty"`
	Launch     Launch `json:"launch"`
}

// ToolRunPath is where a change's run of tool name is recorded.
func (p *Project) ToolRunPath(id, name string) string {
	return filepath.Join(p.LogDir(id), "tool-"+name+".json")
}

// LoadToolRun reads the record; nil when there is none.
func (p *Project) LoadToolRun(id, name string) *ToolRun {
	var r ToolRun
	if !readJSON(p.ToolRunPath(id, name), &r) {
		return nil
	}
	return &r
}

// SaveToolRun writes the record.
func (p *Project) SaveToolRun(r *ToolRun) error {
	p.ensureState()
	return writeJSON(p.ToolRunPath(r.Change, r.Name), r)
}

// ClearToolRun removes the record.
func (p *Project) ClearToolRun(id, name string) {
	_ = os.Remove(p.ToolRunPath(id, name))
}
