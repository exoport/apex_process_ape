package apexv1

import (
	"fmt"
	"slices"
	"strings"
)

// The nesting rule (D61, D62; design/conductor-orchestrator.md).
//
// Depth counts only the model sessions ape launched: `ape task`, `ape
// prompt`, `ape chat`, `ape change review --run`, and the `ape service`
// task/prompt jobs, which are those commands. A terminal, a Claude Code
// session ape did not launch, a shell, an `ape script` program and the
// service daemon are depth 0, and may start any model session. A session ape
// launched from depth 0 is depth 1; it may start a child session only when
// it is ape's own apex-orchestrator run, only one at a time, and only an
// allow-listed skill. That child is depth 2, and starts nothing.
//
// ape decides the depth, never the model: from what it recorded when it
// launched the session (apecmd's nesting.go reads its environment marker
// and the process ancestry). There is no switch to turn the rule off.

// The nesting rule's codes.
const (
	// CodeSessionDepth: a model session from a session that may not start
	// one — depth 1 that is not an apex-orchestrator run, or depth 2.
	CodeSessionDepth = "session.depth"
	// CodeSessionNotAllowed: an apex-orchestrator run starting a skill
	// outside the allow-list, or a session that is not `ape task <skill>` /
	// `ape change review --run`.
	CodeSessionNotAllowed = "session.not-allowed"
	// CodeChildRunning: the orchestrator run already has a child session
	// running; one at a time.
	CodeChildRunning = "session.child-running"
	// CodeOrchestratorRunning: the project already has an apex-orchestrator
	// run; one per project.
	CodeOrchestratorRunning = "session.orchestrator-running"
)

// OrchestratorSkill is the skill whose ape task run may start child
// sessions.
const OrchestratorSkill = "apex-orchestrator"

// CoreChildSkills are the core skills an orchestrator run may start
// (D62, RC3-PLAN Q3): the loop's skills and apex-release. Not apex-handoff
// (a person's or a peer's tool) and not apex-orchestrator itself.
var CoreChildSkills = []string{"apex-plan", "apex-change", "apex-apply", ReviewSkill, "apex-release"}

// MaxDepth is the deepest session: an orchestrator's child.
const MaxDepth = 2

// SessionAt is the ape session a command runs in, as ape recorded it.
type SessionAt struct {
	// Depth is 0 outside every ape-launched session.
	Depth int
	// Run is the session's run id; Skill the skill ape typed into it ("" for
	// a prompt or chat session, or when ape's record of it is gone).
	Run, Skill string
	// Kind is the command that launched it (task, prompt, chat, script, …).
	Kind string
}

// Orchestrator reports whether the session is ape's own apex-orchestrator
// run: an `ape task` (or `ape script` task) session ape typed the skill
// into.
func (s SessionAt) Orchestrator() bool {
	return s.Depth == 1 && s.Skill == OrchestratorSkill && (s.Kind == "task" || s.Kind == "script")
}

// SessionStart is what a command about to start a model session (verb, e.g.
// "ape task"; skill, "" for a prompt or chat) may do from the session it runs
// in. It returns the new session's depth, or a UsageError the command
// refuses with: CodeSessionDepth or CodeSessionNotAllowed, both action stop.
// allow is the orchestrator's allow-list (ChildSkillAllowList).
func SessionStart(at SessionAt, verb, skill string, allow []string) (int, error) {
	where := describeSession(at)
	switch {
	case at.Depth == 0:
		return 1, nil
	case at.Depth >= MaxDepth:
		return 0, &UsageError{Code: CodeSessionDepth, Action: ActionStop, Msg: fmt.Sprintf(
			"%s refused: this runs inside %s, which an orchestrator started (depth %d), and a session at that depth starts no model session"+
				" — finish this session's own work and report back; the orchestrator starts the next session", verb, where, at.Depth)}
	case !at.Orchestrator():
		return 0, &UsageError{Code: CodeSessionDepth, Action: ActionStop, Msg: fmt.Sprintf(
			"%s refused: this runs inside %s (depth 1), and only an apex-orchestrator run started with `ape task apex-orchestrator` starts model sessions from inside ape"+
				" — run it from a terminal, or let the orchestrator start it", verb, where)}
	case skill == "" || !slices.Contains(allow, skill):
		what := "a session with no skill"
		if skill != "" {
			what = skill
		}
		return 0, &UsageError{Code: CodeSessionNotAllowed, Action: ActionStop, Msg: fmt.Sprintf(
			"%s refused: an apex-orchestrator run starts only `ape task <skill>` (or `ape change review <id> --run`) for a skill on its allow-list (%s), and this is %s"+
				" — a person runs it from a terminal, or the project lists the skill under tools:", verb, strings.Join(allow, ", "), what)}
	}
	return at.Depth + 1, nil
}

func describeSession(at SessionAt) string {
	s := "an ape session"
	if at.Kind != "" && at.Run != "" {
		s += " (" + at.Kind + "/" + at.Run
		if at.Skill != "" {
			s += ", " + at.Skill
		}
		s += ")"
	}
	return s
}

// SessionBusy is the refusal for a session lock another live run holds:
// CodeChildRunning for an orchestrator's second child, else
// CodeOrchestratorRunning. Both are conflicts with action retry: wait for
// that run (`ape run wait <id>`), then run the command again.
func SessionBusy(child bool, holder string) error {
	if child {
		return &ConflictError{Code: CodeChildRunning, Action: ActionRetry, Msg: "this orchestrator run already has a child session running (" + holder +
			") — one at a time: wait for it (`ape run wait <id> --wait 9m`), then start the next"}
	}
	return &ConflictError{Code: CodeOrchestratorRunning, Action: ActionRetry, Msg: "this project already has an apex-orchestrator run (" + holder +
		") — one per project: wait for it to end, or stop it (`ape run stop <id>`), then start again"}
}
