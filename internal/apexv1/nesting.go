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
// it is ape's own apex-loop run, only one at a time, and only an
// allow-listed skill. That child is depth 2, and starts nothing.
//
// ape decides the depth, never the model: from what it recorded when it
// launched the session (apecmd's nesting.go reads its environment marker
// and the process ancestry). There is no switch to turn the rule off.

// The nesting rule's codes.
const (
	// CodeSessionDepth: a model session from a session that may not start
	// one — depth 1 that is not an apex-loop run, or depth 2.
	CodeSessionDepth = "session.depth"
	// CodeSessionNotAllowed: an apex-loop run starting a skill
	// outside the allow-list, or a session that is not `ape task <skill>` /
	// `ape change review --run`.
	CodeSessionNotAllowed = "session.not-allowed"
	// CodeChildRunning: the loop run already has a child session running;
	// one at a time.
	CodeChildRunning = "session.child-running"
	// CodeLoopRunning: the project already has an apex-loop run; one per
	// project. It was session.orchestrator-running until v1.3, kept as its
	// alias (CodeAliases, D510).
	CodeLoopRunning = "session.loop-running"
)

// OrchestratorSkill is the loop skill, apex-loop: the unattended runner of
// one approved milestone or tweak, whose ape task run may start child
// sessions (D62). It was apex-orchestrator until rc.7 (D171, Q95); the
// identifier keeps its name.
const OrchestratorSkill = "apex-loop"

// OperatorSkill is apex-orchestrator, since rc.7 the operator's interface
// (D171, D182): a skill a person opens in their own Claude Code session
// with /apex-orchestrator, holding no ape privilege. ape never launches it
// (CodeSkillRenamed). A run an older ape recorded under this name was the
// loop's (IsLoopRun).
const OperatorSkill = "apex-orchestrator"

// IsLoopRun reports whether a recorded ape task run of skill is a loop
// run: apex-loop, or apex-orchestrator as an older ape recorded the loop
// before rc.7 (history stays as recorded, Q95). ape launches neither name
// for anything else, so a live run of either is the loop's.
func IsLoopRun(skill string) bool { return skill == OrchestratorSkill || skill == OperatorSkill }

// MaxDepth is the deepest session: a loop run's child.
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

// Orchestrator reports whether the session is ape's own loop run: an `ape
// task` (or `ape script` task) session ape typed apex-loop into (or, as an
// older ape recorded it, apex-orchestrator: IsLoopRun).
func (s SessionAt) Orchestrator() bool {
	return s.Depth == 1 && IsLoopRun(s.Skill) && (s.Kind == "task" || s.Kind == "script")
}

// SessionStart is what a command about to start a model session (verb, e.g.
// "ape task"; skill, "" for a prompt or chat) may do from the session it runs
// in. It returns the new session's depth, or a UsageError the command
// refuses with: CodeSessionDepth or CodeSessionNotAllowed, both action stop.
// allow is the loop's allow-list (ChildSkillAllowList).
func SessionStart(at SessionAt, verb, skill string, allow []string) (int, error) {
	where := describeSession(at)
	switch {
	case at.Depth == 0:
		return 1, nil
	case at.Depth >= MaxDepth:
		return 0, &UsageError{Code: CodeSessionDepth, Action: ActionStop, Msg: fmt.Sprintf(
			"%s refused: this runs inside %s, which an apex-loop run started (depth %d), and a session at that depth starts no model session"+
				" — finish this session's own work and report back; the loop starts the next session", verb, where, at.Depth)}
	case !at.Orchestrator():
		return 0, &UsageError{Code: CodeSessionDepth, Action: ActionStop, Msg: fmt.Sprintf(
			"%s refused: this runs inside %s (depth 1), and only an apex-loop run started with `ape task apex-loop` starts model sessions from inside ape"+
				" — run it from a terminal, or let the loop start it", verb, where)}
	case skill == "" || !slices.Contains(allow, skill):
		what := "a session with no skill"
		if skill != "" {
			what = skill
		}
		return 0, &UsageError{Code: CodeSessionNotAllowed, Action: ActionStop, Msg: fmt.Sprintf(
			"%s refused: an apex-loop run starts only `ape task <skill>` (or `ape change review <id> --run`) for a skill on its allow-list (%s), and this is %s"+
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
// CodeChildRunning for a loop run's second child, else
// CodeLoopRunning. Both are conflicts with action retry: wait for
// that run (`ape run wait <id>`), then run the command again.
func SessionBusy(child bool, holder string) error {
	if child {
		return &ConflictError{Code: CodeChildRunning, Action: ActionRetry, Msg: "this apex-loop run already has a child session running (" + holder +
			") — one at a time: wait for it (`ape run wait <id> --wait 9m`), then start the next"}
	}
	return &ConflictError{Code: CodeLoopRunning, Action: ActionRetry, Msg: "this project already has an apex-loop run (" + holder +
		") — one per project: wait for it to end, or stop it (`ape run stop <id>`), then start again"}
}

// rc.7 (rc7-rename).
const (
	// CodeSkillRenamed: ape task (or ape service submit task, apescript's
	// RunTask) of apex-orchestrator, the operator's interface since rc.7,
	// which ape never launches (Q95).
	CodeSkillRenamed = "skill.renamed"
)

// RenamedSkill is the refusal of a session of skill when skill is the
// operator's apex-orchestrator (Q95, OI-2): not a silent alias, as the name
// now means another skill, and a stopped run's old resume line must fail
// loudly rather than start the wrong thing. args are the session's
// --args, named in the pointer when given. nil for any other skill.
func RenamedSkill(verb, skill, args string) error {
	if skill != OperatorSkill {
		return nil
	}
	target := "<M>"
	if a := strings.TrimSpace(args); a != "" {
		target = a
	}
	return &UsageError{Code: CodeSkillRenamed, Action: ActionStop, Msg: fmt.Sprintf(
		"%s %s refused: since rc.7 apex-orchestrator is the operator's interface, a skill you open in your own Claude Code session, and the unattended runner of a milestone or tweak is apex-loop (D171)"+
			" — run the milestone or approved tweak unattended with `ape task apex-loop --args %s`, or talk to APEX: run `claude` in the project and type `/apex-orchestrator`", verb, skill, target)}
}
