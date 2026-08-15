package sessiondriver

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Claude Code's `background_tasks` type vocabulary, as it appears on the
// wire. The harness maps its internal task kinds through a display-name
// table before serialising them onto the hook payload (`local_agent` →
// "subagent", `in_process_teammate` → "teammate", and so on), so these
// are the mapped names, not the internal ones. An unmapped kind falls
// through to its raw internal name — which is why classifyStop treats an
// unrecognised type as blocking rather than benign (see below).
const (
	taskTeammate = "teammate"      // in_process_teammate — never returns a tool result
	taskSubagent = "subagent"      // local_agent, backgrounded
	taskCloud    = "cloud session" // remote_agent
	taskWorkflow = "workflow"
	taskShell    = "shell"
	taskMonitor  = "monitor"
	taskMCP      = "MCP task"
	taskDream    = "dream"
	taskAutoScan = "auto-mode scan"
)

// benignTaskTypes are background kinds a skill may legitimately leave
// running across a turn boundary: a backgrounded shell command, a
// monitor, a scheduled scan. None of them owes the orchestrator a
// result, so none of them blocks step completion.
//
// Everything NOT listed here blocks — including a type this table does
// not know. That asymmetry is deliberate: the harness emits an unmapped
// kind under its raw internal name, so a future agent-like task type
// would arrive as an unrecognised string. Defaulting the unknown case to
// "ignore" would silently re-open the hole this gate exists to close;
// defaulting it to "block" costs at worst a deferred step bounded by the
// idle backstop, and surfaces immediately in the drift report.
var benignTaskTypes = map[string]bool{
	taskShell:    true,
	taskMonitor:  true,
	taskMCP:      true,
	taskDream:    true,
	taskAutoScan: true,
	taskWorkflow: true,
}

// agentToolName is the tool whose PostToolUse result carries a spawn
// status worth gating on.
const agentToolName = "Agent"

// spawnStatusTeammate is the Agent-tool result status for a spawn that
// detached into an addressable teammate. The harness renders that result
// as "The agent is now running and will receive instructions via
// mailbox" — the parent is never handed a tool result for it, so waiting
// cannot resolve it no matter how long the wait.
const spawnStatusTeammate = "teammate_spawned"

// BackgroundTask is one entry of the `background_tasks` array Claude Code
// puts on every Stop / SubagentStop hook payload: the tasks that were
// still running or pending when the turn ended. The harness has already
// filtered out foreground work (anything explicitly not backgrounded),
// so an entry here is by construction detached from the turn that is
// ending.
//
//nolint:tagliatelle // decodes Claude Code's snake_case hook payload; the wire shape is fixed
type BackgroundTask struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Description string `json:"description"`
	AgentType   string `json:"agent_type"`
}

// Label renders one task for an operator-facing diagnostic.
func (t BackgroundTask) Label() string {
	var b strings.Builder
	b.WriteString(t.Type)
	if t.ID != "" {
		fmt.Fprintf(&b, " %s", t.ID)
	}
	if t.AgentType != "" {
		fmt.Fprintf(&b, " (%s)", t.AgentType)
	}
	if t.Description != "" {
		fmt.Fprintf(&b, " — %q", t.Description)
	}
	if t.Status != "" {
		fmt.Fprintf(&b, " [%s]", t.Status)
	}
	return b.String()
}

// stopVerdict is what a Stop hook means for step completion.
type stopVerdict int

const (
	// stopComplete: nothing outstanding — signal step-done, the
	// behaviour ape has always had.
	stopComplete stopVerdict = iota
	// stopDefer: real work is still running and may yet report. Do NOT
	// complete the step; a later Stop re-evaluates.
	stopDefer
	// stopFatal: outstanding work that can never report back. Fail now.
	stopFatal
)

// classifyStop decides what a Stop hook means given the background tasks
// the harness reported alongside it.
//
// tasks is a POINTER so "field absent" and "field present but empty"
// stay distinguishable. Both complete the step — an older Claude Code
// that does not send background_tasks must behave exactly as ape did
// before this gate existed — but only the absent case is drift worth
// reporting, and DriftFields is what reports it.
func classifyStop(tasks *[]BackgroundTask) (verdict stopVerdict, blocking []BackgroundTask) {
	if tasks == nil || len(*tasks) == 0 {
		return stopComplete, nil
	}
	verdict = stopComplete
	for _, t := range *tasks {
		if benignTaskTypes[t.Type] {
			continue
		}
		blocking = append(blocking, t)
		// A teammate outranks everything else: it is the one kind whose
		// non-resolvability is provable, so it decides the verdict even
		// if a resolvable subagent is outstanding too.
		if t.Type == taskTeammate {
			verdict = stopFatal
			continue
		}
		if verdict != stopFatal {
			verdict = stopDefer
		}
	}
	return verdict, blocking
}

// agentSpawnResult is the minimal shape of an Agent-tool PostToolUse
// result. Only `status` gates anything; the rest is for the diagnostic.
//
//nolint:tagliatelle // decodes Claude Code's snake_case hook payload; the wire shape is fixed
type agentSpawnResult struct {
	Status     string `json:"status"`
	TeammateID string `json:"teammate_id"`
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	AgentType  string `json:"agent_type"`
}

// classifyAgentSpawn returns a non-nil error when a PostToolUse envelope
// records an Agent-tool spawn that detached into a teammate. Every other
// shape — a different tool, a malformed or non-object tool_response, a
// `completed` / `async_launched` / `failed` status, or an older Claude
// Code that sends no status at all — returns nil and changes nothing.
func classifyAgentSpawn(env hookEnvelope) *DetachedAgentError {
	if env.ToolName != agentToolName || len(env.ToolResponse) == 0 {
		return nil
	}
	var res agentSpawnResult
	// A tool_response is not always an object (some tools return a bare
	// string). A decode failure means "not the shape we gate on" — the
	// gate asserts positively, so anything unrecognised is inert.
	if err := json.Unmarshal(env.ToolResponse, &res); err != nil {
		return nil //nolint:nilerr // an undecodable result is not this defect; never fail a run on it
	}
	if res.Status != spawnStatusTeammate {
		return nil
	}
	id := res.TeammateID
	if id == "" {
		id = res.AgentID
	}
	return &DetachedAgentError{
		Source:    DetectedAtSpawn,
		Kind:      taskTeammate,
		AgentID:   id,
		Name:      res.Name,
		AgentType: res.AgentType,
	}
}

// Where a detached agent was detected. Both are the same defect; the
// spawn site simply notices it sooner and with better context.
const (
	DetectedAtSpawn = "spawn"
	DetectedAtStop  = "turn boundary"
)

// DetachedAgentError reports that a step spawned work which can never
// hand a result back to the agent that spawned it, so the run cannot
// make progress no matter how long it waits.
//
// This is deliberately NOT a timeout: the condition is decidable the
// moment the harness reports it, and the idle / max-duration backstops
// stay reserved for a genuinely wedged session. Returning it from
// WaitStepDone fails the step immediately.
type DetachedAgentError struct {
	Source    string // DetectedAtSpawn / DetectedAtStop
	Skill     string // the step's skill, when the caller knows it
	Kind      string // background-task type ("teammate")
	AgentID   string
	Name      string
	AgentType string
	Tasks     []BackgroundTask // populated on the Stop path
}

func (e *DetachedAgentError) Error() string {
	var b strings.Builder
	b.WriteString("detached agent")
	if e.Skill != "" {
		fmt.Fprintf(&b, " in %s", e.Skill)
	}
	fmt.Fprintf(&b, " (detected at %s): ", e.Source)
	switch {
	case len(e.Tasks) > 0:
		labels := make([]string, 0, len(e.Tasks))
		for _, t := range e.Tasks {
			labels = append(labels, t.Label())
		}
		fmt.Fprintf(&b, "the turn ended with %s still outstanding", strings.Join(labels, "; "))
	default:
		fmt.Fprintf(&b, "the Agent tool returned %q", spawnStatusTeammate)
		if e.Name != "" {
			fmt.Fprintf(&b, " for @%s", e.Name)
		}
		if e.AgentID != "" {
			fmt.Fprintf(&b, " (agent %s)", e.AgentID)
		}
	}
	b.WriteString(" — a teammate reports through its mailbox and never returns a tool " +
		"result, so the spawning agent waits on a result that cannot arrive. " +
		"Spawn without a `name` so the agent runs as a normal sub-agent.")
	return b.String()
}
