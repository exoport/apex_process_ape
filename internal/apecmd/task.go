package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/claudeprobe"
	"github.com/exoport/apex_process_ape/internal/commitowners"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/proctree"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
	"github.com/exoport/apex_process_ape/internal/usage"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// `ape task` uses the shared exit-code table in exitcodes.go
// (ExitOK / ExitRunFailed / ExitUsage / ExitREPLNotReady / ExitUpstreamAPI
// / ExitCommitContract).

// taskCommitDerivedSentinel is the NoOptDefVal for a bare
// `--task-commit` (no message). Contains a control byte so it cannot
// collide with a user-typed message.
const taskCommitDerivedSentinel = "\x01derived"

// taskLongHelp is `ape task --help`'s long text, kept out of newTaskCmd
// so the constructor reads as flags and wiring. The depth-1 allow-list in
// it is generated from apexv1.CoreChildSkills, so the help names exactly
// the skills the refusal message and the rule use.
var taskLongHelp = strings.Replace(taskLongHelpText, "{{depth1}}", depthOneHelp(), 1)

// depthOneHelp is the nesting table's depth-1 row of `ape task --help`.
func depthOneHelp() string {
	return hangingWrap("  depth 1  ", "           ", "a session started from depth 0: may start a child only when it is ape's own"+
		" apex-loop run (ape task apex-loop), only a skill on its allow-list ("+
		strings.Join(apexv1.CoreChildSkills, ", ")+", the human_review: delegate skill, and every skill"+
		" the tools: slots name), and one child at a time", taskHelpWidth)
}

// taskHelpWidth is the column the generated depth-1 row of `ape task
// --help`'s nesting table wraps at, beside the table's hand-written rows.
const taskHelpWidth = 76

// hangingWrap fills text into lines of at most width runes, the first after
// prefix and the rest after indent.
func hangingWrap(prefix, indent, text string, width int) string {
	var b strings.Builder
	line := prefix
	fresh := true
	for w := range strings.FieldsSeq(text) {
		if !fresh && len(line)+1+len(w) > width {
			b.WriteString(line + "\n")
			line, fresh = indent, true
		}
		if !fresh {
			line += " "
		}
		line += w
		fresh = false
	}
	b.WriteString(line)
	return b.String()
}

const taskLongHelpText = `Run one framework skill in one interactive Claude Code session, with
ape's agent prefix, preflight, bridge hooks, run manifest and telemetry,
every parameter passed as a flag. Execution is PTY-interactive only: claude runs as a REPL,
the prompt is typed as keystrokes, and completion is detected via the
bridge Stop hook.

Commit control is two-layered:
  --no-commit     skill layer — tells the skill/framework not to commit, by
                  typing --no-commit into the invocation. Without it none is
                  typed, on either path: APEX v1 skills commit their own work.
  --task-commit   task layer — opt-in git commit of the complete task at
                  the end of the run. Off by default. A bare flag derives
                  the message "ape:task/<skill>".

Run artifacts land under <project>/_output/ape/tasks/<skill>/<run-id>/
(manifest.yaml, per-step ndjson, runlog streams) — in an APEX v1 project
(_apex/state/ exists) under _apex/state/runs/tasks/ instead, a directory
git ignores, where a model clearing _output/ cannot reach.

In an APEX v1 project (development/project.md exists) ape decides the
session's model and effort itself, and records the decision in the run
manifest as launch: {model, model_source, effort, effort_source}:
  model    --model, else _apex/config.yaml models.<skill> ("sonnet" or
           {model: sonnet, effort: high}), else the model of the ape session
           that started this one (APE_SESSION_MODEL), else claude's default
  effort   --effort, else models.<skill>.effort, else the parent session's
           (APE_SESSION_EFFORT; skipped when models.<skill> names a model
           without an effort), else _apex/config.yaml effort:, else nothing
The effort is passed as claude --effort <level>, which sets the session's
effort only; CLAUDE_CODE_EFFORT_LEVEL is never set (an inherited one is
removed) and _apex/effort-defaults.yaml is not used. Sources: flag, config,
parent-session, config-default, default. The session's tools see the
resolved values as APE_SESSION_MODEL / APE_SESSION_EFFORT.

In an APEX v1 project ape first checks the floor the installed framework
sets: when _apex/ape-commands.yaml declares a min_ape_version above this
ape, the session is refused (exit 1, code ape.below-floor) with the
command that updates ape — the same floor ` + "`ape framework update`" + ` refuses an
install by (exit 11). A dev build or a pseudo-version cannot be compared:
a warning, and the session runs.

In an APEX v1 project ape also enforces the milestone budget, when
_apex/config.yaml says so (it is off by default):
  limits:
    budget: enforce   # or off
With enforce, and a Budget: line on the current milestone (the first not
done, and only when approved), a session that would start with the
milestone's spend already at its budget is refused (exit 1, code
milestone.budget-exceeded); a running session is stopped when the
milestone's spend — its runs since approval, plus this session's live cost
read from its transcripts, sub-agents included, every 30 s (sooner in its
first half minute) — reaches it.
The stop ends the run (exit 1) with termination kind "budget" in the run
manifest (spent_usd, budget_usd, session_cost_usd) and is recorded under
the milestone's stops in _apex/state/limit-stops.json (ignored by git:
nothing is committed, whichever branch the session was on); ` + "`ape status`" + ` shows it. Raising the Budget:
line (a person's call) lets sessions start again. An unparseable
_apex/config.yaml in a v1 project refuses every session (exit 2) rather
than run it with no limits.

apex-orchestrator is refused (exit 2, code skill.renamed, action stop), and
so is the same skill through ape service submit task and an ape script's
RunTask: since rc.7 (D171) it is the operator's interface, a skill a person
opens in their own Claude Code session by typing /apex-orchestrator, and the
unattended runner of one approved milestone or tweak is apex-loop. The
refusal carries both pointers: ` + "`ape task apex-loop --args <M>`" + ` (the run's own
--args) and /apex-orchestrator. It is no alias: a stopped run's older resume
line fails loudly instead of starting the wrong skill. A loop run an older
ape recorded as apex-orchestrator still counts as the loop's (its lock, its
children; ape costs groups both names).

A session of apex-loop is also refused (exit 1, code
milestone.blocks-exceeded) when the milestone it runs — --args M<n>, else
the current one — has had limits.consecutive_blocks (default 2) changes
blocked in a row; a person deals with them and re-approves the milestone,
which resets the count. A tweak's run (--args <tweak-id>) is not refused.

--prompt-file <path> reads the prompt from a file, and "-" reads it
from stdin. It is how a printed escalation command carries the
operator's own words: argv runs command substitution on backticks and
reads a leading dash as a flag, so text that a person wrote never goes
through it. The text is held to the same shape a REPL can be typed:
one line, no control characters, no trailing backslash.

--handoff <file> is a shorthand for --prompt: it checks the file
exists and derives the prompt "Read <abs-path> and follow the Resume
Protocol inside it." (the same continuation prompt the /handoff skill
suggests), and is mutually exclusive with --prompt.

--prompt, --prompt-file and --handoff each need --prompt-flag, the skill
flag the text is typed after: without it ape refuses (exit 2) instead of
starting the skill without the text. No APEX skill takes one: resume a
handoff by pasting its continuation line after /clear.

A line longer than 700 characters is not typed. Claude Code turns a
typed burst past 800 characters into a "[Pasted text #N]" placeholder
that never runs as a skill, so ape writes the step's --args and prompt
text, verbatim and with its newlines, to
stages/NN-<stage>/step-NN-<skill>.args.md in the run directory, and
types a line that names that file instead. The step-start event records
it as args_file.

The same file is used, whatever the length, when the --args or prompt
text would not arrive as written if typed: when it holds two or more
spaces in a row, a tab, a line break or any other control character.
The typed line splits --args on whitespace, and a line break submits
early. Leading and trailing whitespace of --args is not part of the
text. Shorter, single-spaced, one-line text is typed exactly as before.

After typing, ape presses Enter once claude has drawn the line, then
confirms the submit through the UserPromptSubmit hook, pressing Enter up
to twice more. A line that is still not submitted ends the run with
termination prompt_not_submitted instead of idling to the timeout.

Where the project declares commit ownership in _apex/commit-owners.csv,
every dispatch is asserted against it. A skill ABSENT from that file
must leave HEAD, the index and the stash reflog unchanged — "git add"
and "git stash" both leave HEAD alone, so HEAD by itself is not the
check. A skill PRESENT in it must produce at least one commit, every one
matching a message format the file declares for it.

No APEX v1 release ships _apex/commit-owners.csv, so a v1 project is not
asserted unless it adds one. A project with NO commit-owners.csv is not
asserted at all: with nothing
declaring which skills commit, neither assertion has a basis, and the
verdict is reported as skipped rather than guessed in either direction.
Rows naming a skill ape never dispatches (the conducting session's own)
are simply never reached. --task-commit is ape's own commit and is not
judged against a skill's declaration.

The verdict is written to the run manifest as commit_contract, so a
failure stays diagnosable after the fact.

A run's commits are the range its HEAD moved over. A commit in it that was
made while another ape session's run in the same checkout went (not this
run's parent loop run, not its own children) cannot be told apart, so
neither run claims it: both list it under commits_shared, in the JSON
envelope and the run manifest, and it is left out of commits and of the
commit-message check.

Usage limits (H20): when a claude.ai plan's usage limit stops the session,
Claude Code draws its limit line ("You've hit your usage limit · resets
11pm (Europe/Madrid)", or "Usage limit reached · limit resets …") and no
hook fires again. ape reads the PTY pane every 15 s; the same line on two
reads with no hook between them ends the run (exit 14) with termination
kind "usage_limit" in the run manifest, resets as printed and resets_at
parsed; ` + "`ape status`" + ` shows it as the latest run ("stopped: usage-limit,
resets <ts>"). Run again after the reset. Matched against Claude Code
2.1.302's text, unmeasured live: so the first limit screen per Claude Code
version is kept, the raw PTY bytes in <user cache>/ape/claude-limit-<version>.bin
and the last usage reading (ape usage) beside them in .json, and the stop's
message names both (W4).

Usage-aware launching (W4): every unattended session ape launches records
the plan's usage from its status line (ape usage --help; not ape chat). In an
APEX v1 project, before the session starts — every ape task, the loop's
children and the sessions ape change starts included — ape's usage guard
reads the newest reading against _apex/config.yaml
  limits:
    usage:
      seven_day: 0.85   # stop over this 7-day fill
      five_hour: 0.95   # hold over this 5-hour fill, with the launch's share
      ceiling: 0.95     # stop when the 7-day fill with the share is over it
      share: auto       # the skill's median over its last 10 runs (0.05 below
                        # 3), or a number
      max_age: 10m      # an older reading is unknown
      off: false        # true: no guard
(the defaults; guarded, like every limit) and decides:
  launch   a fresh reading under every limit
  hold     5-hour window + share over five_hour: the launch waits until the
           window resets + 1 min (a line every 10 min; ape status shows the
           session held, ape run wait --json state "held" with held_until and
           exit 75 as for running), then decides again. Nothing is logged:
           a hold resolves itself.
  stop     7-day window over seven_day, or + share over ceiling: the session
           is not started — exit 14, termination usage_guard (resets,
           resets_at) in the run manifest, events stop (reason usage-guard)
           and person (action: the command with --usage-ok). A person's call.
  unknown  no reading, or one older than max_age: launch, with "usage
           unknown (no reading in 10 min)" in the output and the manifest
A window whose reset has passed counts as 0. --usage-ok launches whatever
the reading says; it is a person's (refused in a model session, exit 2,
session.person-only) and recorded. The decision is the run manifest's
usage.guard: {decision, reading, share_five_hour, share_seven_day,
share_source, held_until, held_seconds, usage_ok, note}, beside usage.before
and usage.after.

Events and the notifier (H20, W3): in an APEX v1 project every run logs
"run-end" in _apex/state/events.jsonl when it ends, attached or detached
(a detached run's supervisor logs it with the exit it records), and a run
stopped on a limit (usage limit, budget) logs "stop" first. Each line is
{seq, at, event, milestone, change, run, exit, reason, action, skill}:
the milestone and change --args names, the run's id, its exit code, the
termination kind as the reason. After the line, _apex/config.yaml's
optional
  notify: <shell command>
runs (sh -c, 10 s timeout, output dropped, a failure only warned about)
with APE_EVENT, APE_SEQ, APE_MILESTONE, APE_CHANGE, APE_RUN, APE_EXIT,
APE_SKILL, APE_REASON and APE_ACTION in its environment. The other
events (person, gate-green, milestone-done, block, run-lost) and the wait
on them: ape status --help.

Sessions and the nesting rule (D61, D62): every skill of the v1 loop runs
as its own ape task session, and depth counts only the model sessions ape
launched (ape task, ape prompt, ape chat, ape change review --run, and the
ape service task/prompt jobs, which run those commands, decided from the
caller's stamp that ape service submit adds — see ape service --help):
  depth 0  a terminal, a Claude Code session ape did not launch, a shell,
           ape script, the service daemon: may start any session
{{depth1}}
  depth 2  a session a loop run started: starts nothing
A refusal is exit 2, code session.depth or session.not-allowed (action
stop); a second child while one runs is exit 3, session.child-running, and
a second apex-loop run in a project exit 3,
session.orchestrator-running (action retry). Deterministic ape commands are
never limited. ape decides the depth from what it recorded, never from the
model: the session's marker (APE_SESSION, APE_DEPTH) and the process
ancestry — every session it launches is a row in ~/.ape/registry.json
naming the ape process and the claude it spawned, so a scrubbed environment
changes nothing. Every session carries APE_DEPTH, and a child also
APE_PARENT_RUN (the loop's run) and APE_PARENT_PID (the ape process
running it); its manifest records depth and parent_run, and it is stopped,
termination parent_gone, when that process is gone. A loop run's
manifest lists its children and tree_cost_usd when it ends: always, $0 and
no children included, so an absent tree_cost_usd means not computed (totals
stay its own). There is no switch to turn the rule off.

The loop relay (OI-11): an APEX v1 loop session's context grows with its
milestone, so ape relays it. At its next child launch (ape task <skill>,
ape change review --run, ape change tool, ape change accept --delegate)
with its context fill at or over _apex/config.yaml
  limits:
    context_relay: 0.6   # 0 < x <= 0.95; default 0.6
    relays: 5            # relays per loop run; default 5
the launch is refused (exit 2, code session.relay, action stop): the loop
starts nothing, replies and ends, and this process starts a fresh loop
session with the same args — its manifest records relay_of (the run it
relays) and relay (the count), under the same lock; the JSON envelope
carries relays and relay_of. The fill is the newest turn of the loop
session's transcript (input + cache read + cache creation tokens) over its
model's context window; an unknown window is no relay, said once. Past
limits.relays the launch is refused with limits.relays (exit 1, action
person) and the run stops, exit 1, termination relays. Both keys are
guarded (ape project accept-config), like every limit. ape status --json
shows a live loop's fill and relays (sessions[].relay).

A loop run dispatches a skill as
  ape task <skill> --args <id> --detach --output-format json
which prints {"id", "wait", "log", "stdout"} (four lines without json), then
  ape run wait <id> --wait 9m --json
until it stops exiting 75; its JSON carries the session's last message as
result (see ape run wait --help).

Exit codes: 0 success · 1 run failed, idle timeout, the typed line
never submitted, an APEX v1 milestone budget stopped or refused the
session, or a v1 limit or ape floor refused it, or the loop's relays ran
out (limits.relays) · 2 usage or
preflight error, an APEX v0 project (run its pinned v0 ape), apex-orchestrator
(skill.renamed), or the nesting rule refused the session (session.depth,
session.not-allowed), or the loop relay refused a child launch (session.relay) · 3 REPL
never became ready (last pane on stderr), or a session lock another run
holds (session.child-running, session.orchestrator-running) · 5 upstream
API failure · 6 the dispatch violated its declared commit ownership (the run
itself may have succeeded) · 14 a claude.ai usage limit stopped the session,
or ape's usage guard did not start it (termination usage_guard).`

func newTaskCmd() *cobra.Command {
	var (
		agentFlag          string
		modelFlag          string
		effortFlag         string
		argsFlag           string
		promptFlag         string
		promptFileFlag     string
		promptFlagName     string
		handoffFlag        string
		noCommitFlag       bool
		taskCommitFlag     string
		allowDirtyFlag     bool
		idleTimeoutFlag    time.Duration
		maxDurationFlag    time.Duration
		outputFormat       string
		jsonAlias          bool
		cwdFlag            string
		detachFlag         bool
		quietFlag          bool
		manifestDirFlag    string
		ignoreProjSettings bool
		outputStyleFlag    string
		natsURLFlag        string
		natsCredsFlag      string
		eventsPrefixFlag   string
		uploadTranscripts  bool
		transcriptStore    string
		leftoversFlag      string
		usageOKFlag        bool
	)
	cmd := &cobra.Command{
		Use:   "task <skill>",
		Short: "Run a single framework skill through the interactive PTY runner",
		Long:  taskLongHelp,
		Example: `  ape task apex-plan --args development/briefs/greeter.md
  ape task apex-loop --args M1 --detach --output-format json
  ape task apex-apply --args <change-id> --model sonnet
  ape task apex-plan --args "add a --name flag to greet" --output-format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("cannot determine working directory: %w", err)
				}
				projectRoot = wd
			}
			// DR-11 (D277): a prompt with nowhere to go is a usage error,
			// before anything is read or started.
			if err := checkPromptFlag(cmd.Flags().Changed, promptFlagName); err != nil {
				return usageErr(err)
			}
			leftovers, err := proctree.ParseLeftoversMode(leftoversFlag)
			if err != nil {
				return usageErr(err)
			}
			// Q95: apex-orchestrator is the operator's now, never a task;
			// said with the run's own args, before the nesting rule.
			if err := refuseRenamed("ape task", args[0], argsFlag, projectRoot); err != nil {
				return sessionRefusal(err)
			}
			// W4: --usage-ok is a person's, from a terminal.
			if usageOKFlag {
				if err := apexv1.RequirePersonFlag("--usage-ok"); err != nil {
					return sessionRefusal(err)
				}
			}
			// The nesting rule (D62), before anything else is read or
			// started: what the session this runs in may start.
			launch, err := gateSession("ape task", args[0], projectRoot)
			if err != nil {
				return sessionRefusal(err)
			}
			jsonMode := jsonAlias || outputFormat == "json"
			if !jsonMode && outputFormat != "human" {
				fmt.Fprintf(os.Stderr, "Error: --output-format must be human or json, got %q\n", outputFormat)
				os.Exit(ExitUsage)
			}
			// Backlog 38: a v0 project runs its skills on its pinned v0 ape.
			if apexv1.IsV0Project(apexv1.FindRoot(projectRoot)) {
				return usageErr(errors.New(apexv1.V0ProjectMsg))
			}
			if detachFlag {
				return startDetached(cmd, projectRoot, launch, jsonMode)
			}
			if err := launch.acquire(projectRoot, lockToken()); err != nil {
				return sessionRefusal(err)
			}
			defer launch.release()
			if handoffFlag != "" {
				prompt, err := resolveHandoffPrompt(handoffFlag, cmd.Flags().Changed("prompt"))
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %s\n", err)
					os.Exit(ExitUsage)
				}
				promptFlag = prompt
			}
			if promptFileFlag != "" {
				prompt, err := resolvePromptFile(promptFileFlag,
					cmd.Flags().Changed("prompt"), handoffFlag != "", cmd.InOrStdin())
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %s\n", err)
					os.Exit(ExitUsage)
				}
				promptFlag = prompt
			}
			var taskCommit *pipeline.CommitDirective
			if cmd.Flags().Changed("task-commit") {
				msg := taskCommitFlag
				if msg == taskCommitDerivedSentinel {
					msg = pipeline.DerivedTaskCommitMessage(args[0])
				}
				if strings.TrimSpace(msg) == "" {
					fmt.Fprintln(os.Stderr, "Error: --task-commit message cannot be empty")
					os.Exit(ExitUsage)
				}
				taskCommit = &pipeline.CommitDirective{Mode: pipeline.CommitModeExplicit, Message: msg}
			}
			warnUnknownOutputStyleFlag(cmd, outputStyleFlag)
			opts := taskOptions{
				skill:                 args[0],
				agent:                 agentFlag,
				model:                 resolveModelArg(modelFlag),
				effort:                effortFlag,
				args:                  argsFlag,
				prompt:                promptFlag,
				promptFlagName:        promptFlagName,
				skillNoCommit:         noCommitFlag,
				taskCommit:            taskCommit,
				allowDirty:            allowDirtyFlag,
				idleTimeout:           idleTimeoutFlag,
				maxDuration:           maxDurationFlag,
				jsonMode:              jsonMode,
				quiet:                 quietFlag,
				projectRoot:           projectRoot,
				manifestDir:           manifestDirFlag,
				ignoreProjectSettings: ignoreProjSettings,
				outputStyle:           outputStyleFlag,
				outputStyleSet:        outputStyleFlagSet(cmd),
				natsURL:               natsURLFlag,
				natsCreds:             natsCredsFlag,
				eventsPrefix:          eventsPrefixFlag,
				uploadTranscripts:     uploadTranscripts,
				transcriptStore:       transcriptStore,
				launch:                launch,
				usageOK:               usageOKFlag,
				leftovers:             leftovers,
			}
			return runTask(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&agentFlag, "agent", "", "Framework agent (slash-command) fronting the skill: /<agent> --autonomous -- <skill> ...")
	cmd.Flags().StringVar(&modelFlag, "model", "", modelFlagUsage("Claude model (APEX v1 unset: models.<skill> in _apex/config.yaml, else the parent session's, else claude's default)."))
	cmd.Flags().StringVar(&effortFlag, "effort", "", "Explicit reasoning effort for the session and its sub-agents (low|medium|high|xhigh|max). Unset: the project's _apex/effort-defaults.yaml per model, or xhigh without one (see ape config effort). In an APEX v1 project: the session's effort only, passed as claude --effort; unset, models.<skill>, the parent session or _apex/config.yaml effort: decide, else none is passed")
	cmd.Flags().StringVar(&argsFlag, "args", "", "Verbatim skill args appended to the invocation (whitespace-separated)")
	cmd.Flags().StringVar(&promptFlag, "prompt", "", "Run prompt forwarded via --prompt-flag (no APEX v1 skill takes one)")
	cmd.Flags().StringVar(&promptFileFlag, "prompt-file", "",
		`File holding the --prompt text; "-" reads stdin. Mutually exclusive with --prompt and --handoff`)
	cmd.Flags().StringVar(&promptFlagName, "prompt-flag", "", "Skill flag name the --prompt value is forwarded through (spec prompt_flag equivalent)")
	cmd.Flags().StringVar(&handoffFlag, "handoff", "", "Path to a handoff/context file; derives a \"Read <path> and follow the Resume Protocol\" --prompt value (mutually exclusive with --prompt)")
	cmd.Flags().BoolVar(&noCommitFlag, "no-commit", false, "Skill layer: tell the skill/framework not to commit (types --no-commit into the skill invocation; without it none is typed)")
	cmd.Flags().StringVar(&taskCommitFlag, "task-commit", "", "Task layer: commit the complete task at the end; bare flag derives \"ape:task/<skill>\"")
	cmd.Flags().Lookup("task-commit").NoOptDefVal = taskCommitDerivedSentinel
	cmd.Flags().BoolVar(&allowDirtyFlag, "commit-allow-dirty", false, "Bypass the dirty-tree gate (relevant only with --task-commit)")
	cmd.Flags().DurationVar(&idleTimeoutFlag, "idle-timeout", 0, "Idle backstop: cancel only after this long with no progress across hook events or transcript growth (e.g. 15m); default 60m. PTY output is NOT an anchor here — see `ape prompt`")
	cmd.Flags().DurationVar(&maxDurationFlag, "max-duration", sessiondriver.DefaultMaxDuration, "Wall-clock ceiling regardless of progress (e.g. 8h); off by default (D159: a run showing progress stops only on the idle timeout, ape's limits or a person). The clock resets on each sub-agent boundary, so a sequential batch skill is bounded per item.")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json (json = result envelope on stdout, progress on stderr)")
	cmd.Flags().BoolVar(&jsonAlias, "json", false, "Alias for --output-format json")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "Suppress the per-event progress stream")
	cmd.Flags().StringVar(&manifestDirFlag, "manifest-dir", "", "Override the run-artifact base dir (default: <project>/_output/ape/tasks; _apex/state/runs/tasks in an APEX v1 project)")
	cmd.Flags().BoolVar(&ignoreProjSettings, "ignore-project-settings", false, "Tell the spawned claude to skip project + local .claude/settings*.json")
	_ = cmd.Flags().MarkHidden("json")
	addOutputStyleFlag(cmd, &outputStyleFlag)
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	addDetachFlag(cmd, &detachFlag)
	cmd.Flags().BoolVar(&usageOKFlag, "usage-ok", false, "Launch whatever the plan's usage reads: the usage guard's override (a person only, from a terminal; recorded in the run manifest with the reading)")
	addLeftoversFlag(cmd, &leftoversFlag)
	addNatsFlags(cmd, &natsURLFlag, &natsCredsFlag, &eventsPrefixFlag, &uploadTranscripts, &transcriptStore)
	return cmd
}

// taskOptions bundles the resolved `ape task` invocation parameters.
type taskOptions struct {
	skill          string
	agent          string
	model          string
	effort         string
	args           string
	prompt         string
	promptFlagName string
	skillNoCommit  bool
	// sessionKind overrides the APE_SESSION kind for a caller that
	// dispatches through the task runner: `ape change`.
	sessionKind           string
	taskCommit            *pipeline.CommitDirective
	allowDirty            bool
	idleTimeout           time.Duration
	maxDuration           time.Duration
	jsonMode              bool
	quiet                 bool
	projectRoot           string
	manifestDir           string
	ignoreProjectSettings bool
	outputStyle           string
	// outputStyleSet records that --output-style was typed, which is what
	// lets an explicit flag outrank the project's own declaration.
	outputStyleSet    bool
	natsURL           string
	natsCreds         string
	eventsPrefix      string
	uploadTranscripts bool
	transcriptStore   string
	// launch is the session's place in the nesting rule (D62), decided by
	// the caller's gateSession; nil decides it at dispatch.
	launch *sessionLaunch
	// usageOK is a person's --usage-ok: the usage guard launches whatever
	// the plan's usage reads (W4).
	usageOK bool
	// leftovers is --leftovers: what the run does with the processes its
	// session leaves running (O-1); "" is kill, as for every caller that
	// dispatches without the flag (ape change, ape script).
	leftovers string
}

// resolveHandoffPrompt derives the --prompt value from --handoff: a
// pointer prompt at the file's absolute path, matching the /handoff
// skill's own suggested continuation prompt. Embedding the file's full
// content instead would risk a premature Enter mid-typing, since the
// prompt is typed into the REPL as literal PTY keystrokes.
func resolveHandoffPrompt(handoff string, promptChanged bool) (string, error) {
	if promptChanged {
		return "", errors.New("--handoff and --prompt are mutually exclusive")
	}
	absPath, err := filepath.Abs(handoff)
	if err != nil {
		return "", fmt.Errorf("resolve --handoff path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", fmt.Errorf("--handoff file not found: %s", absPath)
	}
	return fmt.Sprintf("Read %s and follow the Resume Protocol inside it.", absPath), nil
}

// buildTaskStep maps taskOptions onto a pipeline.Step. The skill-layer
// --no-commit is injected by prefixing step.Args, on both paths, and only
// when the caller asked for it: ExplicitNoCommit switches off the PAT-25
// convention that adds it to every no-agent line. APEX v1 skills commit
// their own work, so `ape task <skill>` must not tell them not to (v1
// trial decision D4).
func buildTaskStep(o taskOptions) pipeline.Step {
	step := pipeline.Step{
		Skill:      o.skill,
		Agent:      o.agent,
		Model:      o.model,
		Effort:     o.effort,
		Args:       o.args,
		PromptFlag: o.promptFlagName,
		// v1 trial D4: no implicit --no-commit on the no-agent path.
		ExplicitNoCommit: true,
	}
	if o.skillNoCommit {
		step.Args = strings.TrimSpace("--no-commit " + step.Args)
	}
	return step
}

// taskUsage mirrors the stream-json result-event usage block shape so
// the envelope drops into consumers that previously read `claude -p`
// output (the eval harness). snake_case is the wire contract.
//
//nolint:tagliatelle // envelope mirrors the stream-json result-event field names
type taskUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// Ephemeral cache-write split (PLAN-10 D1). Additive — the summed
	// cache_creation_input_tokens above is unchanged; these break it into
	// the 5m/1h tiers for consumers that price them differently.
	CacheCreation5mInputTokens int `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens int `json:"cache_creation_1h_input_tokens"`
	NumTurns                   int `json:"num_turns"`
}

// taskModelUsage is one model's (or one session's) usage share on the
// envelope. snake_case is the wire contract.
//
//nolint:tagliatelle // envelope mirrors the stream-json result-event field names
type taskModelUsage struct {
	CostUSD                    float64 `json:"cost_usd"`
	InputTokens                int     `json:"input_tokens"`
	OutputTokens               int     `json:"output_tokens"`
	CacheReadInputTokens       int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens   int     `json:"cache_creation_input_tokens"`
	CacheCreation5mInputTokens int     `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens int     `json:"cache_creation_1h_input_tokens"`
	NumTurns                   int     `json:"num_turns"`
}

// taskSessionUsage is one claude session's usage within the run: the
// main REPL session or a sub-agent (Agent tool) session.
//
//nolint:tagliatelle // envelope mirrors the manifest's snake_case contract
type taskSessionUsage struct {
	SessionID       string                    `json:"session_id"`
	ParentSessionID string                    `json:"parent_session_id,omitempty"`
	CostUSD         float64                   `json:"cost_usd"`
	InputTokens     int                       `json:"input_tokens"`
	OutputTokens    int                       `json:"output_tokens"`
	NumTurns        int                       `json:"num_turns"`
	ModelUsage      map[string]taskModelUsage `json:"model_usage,omitempty"`
}

// taskEnvelope is the `--output-format json` result printed on stdout.
// snake_case is the wire contract (see taskUsage).
//
//nolint:tagliatelle // envelope mirrors the stream-json result-event field names
type taskEnvelope struct {
	Skill           string                    `json:"skill"`
	Agent           string                    `json:"agent,omitempty"`
	Model           string                    `json:"model,omitempty"`
	Success         bool                      `json:"success"`
	ExitCode        int                       `json:"exit_code"`
	DurationSeconds float64                   `json:"duration_seconds"`
	CostUSD         float64                   `json:"cost_usd"`
	Usage           taskUsage                 `json:"usage"`
	ModelUsage      map[string]taskModelUsage `json:"model_usage,omitempty"`
	Sessions        []taskSessionUsage        `json:"sessions,omitempty"`
	Commits         []string                  `json:"commits"`
	// CommitsShared are the commits of the run's range made while another
	// ape session's run in the same checkout went: claimed by neither run,
	// listed in both (commitshare.go).
	CommitsShared []string `json:"commits_shared,omitempty"`
	ManifestPath  string   `json:"manifest_path,omitempty"`
	TelemetryNote string   `json:"telemetry_note,omitempty"`
	// CommitContract is the per-dispatch commit-ownership verdict. Always
	// emitted, including when it passed and when it had to be skipped:
	// a consumer must be able to tell "asserted and clean" from "could
	// not assert", and a field that appears only on failure cannot.
	CommitContract *commitowners.Result `json:"commit_contract,omitempty"`
	Error          *string              `json:"error"`
	// rc.7 (rc7-relay).
	// Relays / RelayOf: for the loop's run, how many times ape relayed it
	// to a fresh session (OI-11), and the run the reported session relays.
	// Absent when it was never relayed.
	Relays  int    `json:"relays,omitempty"`
	RelayOf string `json:"relay_of,omitempty"`
}

// taskExitCode maps a run error onto the PLAN-11 exit-code table.
func taskExitCode(runErr error) int {
	if runErr == nil {
		return ExitOK
	}
	if _, ok := errors.AsType[*repl.NotReadyError](runErr); ok {
		return ExitREPLNotReady
	}
	// The startup probe found a claude that would not come up either — the
	// same failure, found before the dispatch rather than during it.
	if claudeprobe.IsBroken(runErr) {
		return ExitREPLNotReady
	}
	// Upstream, not the skill. Reported separately so a caller can tell a
	// degraded API from a run that genuinely failed.
	if _, ok := errors.AsType[*sessiondriver.TerminalAPIError](runErr); ok {
		return ExitUpstreamAPI
	}
	// H20: the plan's usage limit, not the skill: run again after the reset.
	// W4: as is the usage guard's stop, before the session started.
	if isUsageLimitStop(runErr) || isUsageGuardStop(runErr) {
		return ExitUsageLimit
	}
	return ExitRunFailed
}

// taskRun is one dispatch's outcome: everything the command reports, and
// everything a caller that holds the pen itself needs before it decides
// what to commit.
type taskRun struct {
	// Envelope is the `--output-format json` result, already filled from
	// the run manifest and carrying the mapped exit code.
	Envelope taskEnvelope
	// Contract is the per-dispatch commit-ownership verdict, already
	// folded into Envelope.ExitCode.
	Contract commitowners.Result
	// RunErr is the dispatch's own failure. Kept beside the envelope
	// because the human summary prints the error itself and the envelope
	// carries only its text.
	RunErr error
	// ManifestDir is the resolved run-artifact base dir, so a caller can
	// find the run without re-deriving the default.
	ManifestDir string
	// HeadBefore is where HEAD pointed before the dispatch — the left
	// edge of the commit range the assertion judged.
	HeadBefore string
	// RunID / RunDir are the dispatch's run ("" when none was made).
	RunID, RunDir string
}

// taskPreflightError is a failure found BEFORE any claude process
// spawns. It is the one error dispatchTask returns, and it is ExitUsage.
type taskPreflightError struct{ err error }

func (e *taskPreflightError) Error() string { return e.err.Error() }
func (e *taskPreflightError) Unwrap() error { return e.err }

// dispatchTask runs one task and RETURNS. It exits for nothing and
// writes nothing to stdout.
//
// That is the whole point of the split: `ape change` dispatches through
// this, then runs its own reconciliation and composes the commits
// itself, and a preflight calling os.Exit would take the caller's run
// down mid-flight with no chance to save the residue. `ape script`'s
// task runner dispatches through dispatchTaskWith for the same reason.
//
// The error return is preflight only. A dispatch that spawned and then
// failed reports through the returned taskRun, whose envelope carries
// the exit code.
func dispatchTask(ctx context.Context, o taskOptions) (taskRun, error) {
	cfg := runConfig{
		prompt:                o.prompt,
		allowDirty:            o.allowDirty,
		ignoreProjectSettings: o.ignoreProjectSettings,
		quiet:                 o.quiet,
		suppressSummary:       o.jsonMode,
		idleTimeout:           o.idleTimeout,
		maxDuration:           o.maxDuration,
		natsURL:               o.natsURL,
		natsCreds:             o.natsCreds,
		eventsPrefix:          o.eventsPrefix,
		uploadTranscripts:     o.uploadTranscripts,
		transcriptStore:       o.transcriptStore,
		kind:                  eventing.KindTask,
		sessionKind:           o.sessionKind,
		leftovers:             o.leftovers,
	}
	if o.jsonMode {
		// stdout carries only the envelope; progress goes to stderr.
		cfg.progressWriter = os.Stderr
	}
	return dispatchTaskWith(ctx, o, cfg)
}

// dispatchTaskWith is dispatchTask over a caller-built runConfig: the
// task's own decisions — the commit-ownership declaration and both
// assertions, the per-skill output style, the manifest dir — are made
// here and override cfg's, so no caller can dispatch a task without
// them. `ape script` brings its own eventing and session kind in cfg.
func dispatchTaskWith(ctx context.Context, o taskOptions, cfg runConfig) (taskRun, error) {
	// The nesting rule (D62): a caller that has not applied it yet gets it
	// here, so no dispatch starts a session the rule would refuse.
	if o.launch == nil {
		l, err := gateSession("ape task", o.skill, o.projectRoot)
		if err != nil {
			return taskRun{}, &taskPreflightError{err: err}
		}
		o.launch = l
	}
	// OI-11: an APEX v1 loop is relayed to a fresh session when its context
	// fills (v1_relay.go).
	if o.skill == apexv1.OrchestratorSkill && runlog.IsV1Project(o.projectRoot) {
		return dispatchLoop(ctx, o, cfg)
	}
	return dispatchTaskOnce(ctx, o, cfg)
}

// dispatchTaskOnce is one session of dispatchTaskWith, o.launch decided.
func dispatchTaskOnce(ctx context.Context, o taskOptions, cfg runConfig) (taskRun, error) {
	cfg.launch = o.launch
	// A child session dies with the orchestrator run that started it: when
	// that run's ape process is gone, the session is stopped (a detached
	// one's supervisor does the same, and marks its handle).
	ctx, stopWatch := watchParent(ctx, o.launch.parentPID)
	defer stopWatch()

	// An APEX v1 project decides the session's model and effort itself
	// (backlog 29): the effort goes on claude's command line, never in
	// CLAUDE_CODE_EFFORT_LEVEL, and nothing is passed when nothing decided.
	var v1Launch *v1TaskLaunch
	if p, ok := v1ProjectAt(o.projectRoot); ok {
		l, err := prepareV1Task(ctx, p, &o, &cfg)
		if err != nil {
			return taskRun{}, err
		}
		v1Launch = l
	} else if err := v1ConfigError(o.projectRoot); err != nil {
		return taskRun{}, &taskPreflightError{err: err}
	}
	step := buildTaskStep(o)
	spec := pipeline.NewSingleStepSpec(o.skill, step, o.taskCommit)

	manifestDir := o.manifestDir
	if manifestDir == "" {
		manifestDir = runlog.TasksRoot(o.projectRoot)
	}

	// The commit-ownership declaration is loaded BEFORE the dispatch, so a
	// malformed CSV is a preflight failure rather than a verdict delivered
	// after an hour of model time. A missing file is not an error — it
	// means no skill commits, and every dispatch takes the non-committer
	// assertion.
	owners, ownersErr := commitowners.Load(filepath.Join(o.projectRoot, apexcfg.DirName))
	if ownersErr != nil {
		return taskRun{}, &taskPreflightError{err: ownersErr}
	}

	stateBefore := commitowners.Capture(ctx, o.projectRoot)
	headBefore := stateBefore.Head

	// The framework declares, per skill, which style a dispatch runs
	// under; the flag overrides it. Resolved here rather than inside the
	// settings builder because this is where the skill is known, and the
	// skill is the table's key.
	cfg.outputStyle = resolveSkillOutputStyle(o.projectRoot, o.skill, o.outputStyle, o.outputStyleSet,
		func(msg string) { fmt.Fprintf(os.Stderr, "⚠ %s\n", msg) })
	// Already resolved above, so nothing downstream may re-rank it
	// against a spec's declaration.
	cfg.outputStyleSet = true
	cfg.manifestDir = manifestDir
	cfg.stopGate = newStopGateRecorder(o.projectRoot)
	cfg.stopGate.inline = v1Launch != nil
	// H20: a claude.ai usage limit ends the run when Claude Code draws it.
	cfg.usageLimit = &usageLimitWatch{claudeBin: cfg.claudeBin}
	// W4: the session's status line keeps this run's usage readings.
	cfg.spawnSettings.usageKey = usage.NewKey()

	start := time.Now()
	runErr := runWithInteractive(ctx, spec, o.projectRoot, cfg)
	recordStopGate(o.projectRoot, o.skill, manifestDir, cfg.stopGate)
	recordLaunch(o.projectRoot, o.skill, manifestDir, v1Launch)
	recordLineage(ctx, o.projectRoot, o.skill, manifestDir, o.launch)
	duration := time.Since(start)
	if pfe, ok := errors.AsType[*pipeline.PreflightError](runErr); ok {
		// Nothing spawned, so there is no run to report or assert.
		return taskRun{}, &taskPreflightError{err: pfe}
	}

	// The run's commits, less those another session's run in this checkout
	// may have made while this one went (commitshare.go): those are
	// commits_shared, claimed by neither run, and not asserted below.
	runDir := pipeline.ResolveLatestRunDir(o.projectRoot, o.skill, manifestDir)
	runID := ""
	if runDir != "" {
		runID = runIDFromDir(runDir)
	}
	endRunUsage(cfg.spawnSettings.usageKey, o.skill, runDir)
	subjects, shared := splitSharedCommits(o.projectRoot, runID, o.launch.parentRunOrEmpty(),
		gitCommitsSince(ctx, o.projectRoot, headBefore), time.Now())
	recordCommitsShared(runDir, shared)

	// The two per-dispatch assertions. They run whether or not the skill
	// itself succeeded: a run that failed halfway can still have left a
	// commit, a staged index or a stash behind, and that is precisely the
	// state the caller needs told about.
	//
	// `--task-commit` is ape's own commit, in ape's own derived format,
	// made after the skill is done. Judging it against the skill's
	// declaration would fail every task that used the flag, so the
	// assertion is skipped when ape is the one committing.
	var contract commitowners.Result
	if o.taskCommit == nil {
		// The run's context is cancelled when a signal stopped it, and a
		// git read on it fails at once — reported as "not a git
		// repository". After the dispatch is exactly when this state
		// matters, so the read survives cancellation.
		contract = owners.Assert(o.skill, stateBefore,
			commitowners.Capture(context.WithoutCancel(ctx), o.projectRoot), subjects,
			commitowners.AssertOptions{NoCommit: o.skillNoCommit})
	} else {
		// Explicitly skipped, never left as the zero Result. The zero
		// value marshals as `{"skill":"","declared":false}` with no
		// violations, which a consumer reads as "the non-committer
		// assertion ran and was clean" — a pass nobody earned on a
		// dispatch where nothing was asserted at all.
		contract = commitowners.Skipped(o.skill,
			"--task-commit: ape makes this dispatch's commit itself, "+
				"so the skill's declaration cannot be asserted against the range")
	}

	exitCode := taskExitCode(runErr)
	if runErr != nil && cfg.resumeCommand != "" {
		// After the exit code: the line changes the message, not the verdict.
		runErr = &resumeHintError{err: runErr, line: pipeline.ResumeLine(pipeline.TerminationKind(runErr), cfg.resumeWhat, cfg.resumeCommand)}
	}
	if exitCode == ExitOK && !contract.OK() {
		// Only promoted to the verdict when nothing worse happened: a
		// skill that crashed AND left a stash is reported as the crash,
		// because that is the thing to fix first.
		exitCode = ExitCommitContract
	}
	env := taskEnvelope{
		Skill:           o.skill,
		Agent:           o.agent,
		Model:           o.model,
		Success:         runErr == nil && contract.OK(),
		ExitCode:        exitCode,
		DurationSeconds: duration.Seconds(),
		Commits:         subjects,
		CommitsShared:   shared,
		CommitContract:  &contract,
	}
	if runErr != nil {
		msg := runErr.Error()
		env.Error = &msg
	}
	fillEnvelopeFromManifest(&env, o.projectRoot, o.skill, manifestDir)
	recordCommitContract(o.projectRoot, o.skill, manifestDir, contract)
	// After the assertion: the stop's commit is ape's, not the skill's.
	recordBudgetStop(o.projectRoot, o.skill, manifestDir, cfg.budgetWatch)
	// H20: a run stopped on a limit logs `stop` and runs the project's
	// notifier; W3: every run logs `run-end` after it.
	recordUsageGuard(runDir, cfg.usageGuard)
	if lse, ok := errors.AsType[*pipeline.LimitStopError](runErr); ok && v1Launch != nil {
		if p, ok := v1ProjectAt(o.projectRoot); ok {
			if lse.Kind == pipeline.TerminationUsageGuard {
				// W4: stop (usage-guard), and the person's override.
				emitUsageGuardStop(ctx, p, cfg.usageGuard, runID, lse)
			} else {
				nctx := context.WithoutCancel(ctx)
				milestone, change := p.EventScope(nctx, o.args)
				p.Emit(nctx, apexv1.Event{Event: apexv1.EventStop, Milestone: milestone, Change: change, Run: runID, Reason: lse.Message, Skill: o.skill}, os.Stderr)
			}
		}
	}
	if v1Launch != nil {
		emitTaskRunEnd(ctx, o.projectRoot, o.skill, o.args, runID, exitCode, runErr)
	}

	return taskRun{
		Envelope:    env,
		Contract:    contract,
		RunErr:      runErr,
		ManifestDir: manifestDir,
		HeadBefore:  headBefore,
		RunID:       runID,
		RunDir:      runDir,
	}, nil
}

// orchestratorResumeCommand is the command that resumes an APEX v1
// orchestrator run of args (D159 Q85): the orchestrator picks the
// milestone up from its recorded state.
func orchestratorResumeCommand(args string) string {
	args = strings.TrimSpace(args)
	if strings.ContainsAny(args, " \t\"'") {
		args = strconv.Quote(args)
	}
	return "ape task " + v1OrchestratorSkill + " --args " + args
}

// resumeHintError is a stopped run's error with ResumeLine's sentence
// after it; errors.As and errors.Is still see the run's own error.
type resumeHintError struct {
	err  error
	line string
}

func (e *resumeHintError) Error() string { return e.err.Error() + " — " + e.line }
func (e *resumeHintError) Unwrap() error { return e.err }

// prepareV1Task makes an APEX v1 project's launch decisions for
// dispatchTaskWith, writing them into o and cfg. Every error is a
// preflight failure.
func prepareV1Task(ctx context.Context, p *apexv1.Project, o *taskOptions, cfg *runConfig) (*v1TaskLaunch, error) {
	// H1: the installed framework's ape floor, before anything else.
	if err := v1ApeFloor(o.projectRoot, os.Stderr); err != nil {
		return nil, &taskPreflightError{err: err}
	}
	l, err := decideV1TaskLaunch(p, o.skill, o.model, o.effort)
	if err != nil {
		return nil, &taskPreflightError{err: err}
	}
	o.model, o.effort = l.launch.Model, ""
	cfg.effort, cfg.effortAsArg, cfg.effortSource = l.launch.Effort, true, l.launch.EffortSource
	// Backlog 31: the project's hooks ride inline, so a hand-edited
	// settings.json cannot drop them (apexv1.InlineHooks).
	cfg.spawnSettings.hooks, cfg.spawnEnv = v1InlineHooks()
	// H19, D49: an orchestrator does not run a milestone whose changes
	// were blocked limits.consecutive_blocks times in a row.
	if o.skill == v1OrchestratorSkill {
		if err := p.OrchestratorLimits(ctx, o.args); err != nil {
			return nil, &taskPreflightError{err: err}
		}
		// D159 Q85: a stopped orchestrator says how to resume it.
		cfg.resumeCommand, cfg.resumeWhat = orchestratorResumeCommand(o.args), strings.TrimSpace(o.args)
	}
	// Backlog 30: the milestone's enforced budget refuses the start, or
	// stops the session when its live spend reaches it.
	if cfg.budgetWatch, err = newV1BudgetWatch(ctx, p, o.skill, o.args); err != nil {
		return nil, &taskPreflightError{err: err}
	}
	// Backlog 36: the plugins _apex/config.yaml lists are installed at
	// their refs before anything spawns, and enabled inline.
	plugins, err := v1TaskPlugins(ctx, p, cfg.claudeBin)
	if err != nil {
		return nil, &taskPreflightError{err: err}
	}
	cfg.spawnSettings.settings = plugins
	// W4: the usage guard decides before the session is spawned.
	cfg.usageGuard = newUsageGuard(p, o.skill, o.args, o.usageOK)
	return &l, nil
}

// runTask is `ape task`: dispatch, report, exit. The exiting half of the
// split — every decision it makes is dispatchTask's, and everything here
// is output.
func runTask(ctx context.Context, o taskOptions) error {
	res, err := dispatchTask(ctx, o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		o.launch.release()
		os.Exit(taskPreflightExit(err))
	}
	env := res.Envelope

	if o.jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(env); encErr != nil {
			return encErr
		}
	} else {
		printTaskSummary(env, res.RunErr)
	}
	if env.ExitCode != ExitOK {
		// A NotReadyError's text carries the last pane snapshot, so an
		// unknown blocking modal is diagnosable straight from stderr.
		//
		// RunErr may be nil here: ExitCommitContract is reached by a run
		// that SUCCEEDED and then failed its declaration, and the
		// violations are the whole diagnosis in that case.
		if res.RunErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", res.RunErr.Error())
		}
		for _, v := range res.Contract.Violations {
			fmt.Fprintf(os.Stderr, "Error: %s: %s\n", v.Check, v.Message)
		}
		o.launch.release()
		os.Exit(env.ExitCode)
	}
	return nil
}

// fillEnvelopeFromManifest lifts cost / usage / turns from the run's
// manifest.yaml (via the `latest` symlink). Best-effort: a missing or
// unreadable manifest leaves the telemetry fields zero.
func fillEnvelopeFromManifest(env *taskEnvelope, projectRoot, skill, manifestDir string) {
	runDir := pipeline.ResolveLatestRunDir(projectRoot, skill, manifestDir)
	if runDir == "" {
		return
	}
	manifestPath := filepath.Join(runDir, "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var m pipeline.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return
	}
	if rel, relErr := filepath.Rel(projectRoot, manifestPath); relErr == nil {
		env.ManifestPath = rel
	} else {
		env.ManifestPath = manifestPath
	}
	env.CostUSD = m.Totals.CostUSD
	env.Usage.InputTokens = m.Totals.TokensInput
	env.Usage.OutputTokens = m.Totals.TokensOutput
	env.Usage.CacheReadInputTokens = m.Totals.TokensCacheRead
	env.Usage.CacheCreationInputTokens = m.Totals.TokensCacheCreation
	env.Usage.CacheCreation5mInputTokens = m.Totals.TokensCacheCreation5m
	env.Usage.CacheCreation1hInputTokens = m.Totals.TokensCacheCreation1h
	env.ModelUsage = modelUsageRecordsToEnvelope(m.Totals.ModelUsage)
	for i := range m.Stages {
		for j := range m.Stages[i].Steps {
			step := &m.Stages[i].Steps[j]
			env.Usage.NumTurns += step.NumTurns
			if step.TelemetryNote != "" && env.TelemetryNote == "" {
				env.TelemetryNote = step.TelemetryNote
			}
			for _, s := range step.Sessions {
				env.Sessions = append(env.Sessions, taskSessionUsage{
					SessionID:       s.SessionID,
					ParentSessionID: s.ParentSessionID,
					CostUSD:         s.CostUSD,
					InputTokens:     s.TokensInput,
					OutputTokens:    s.TokensOutput,
					NumTurns:        s.NumTurns,
					ModelUsage:      modelUsageRecordsToEnvelope(s.ModelUsage),
				})
			}
		}
	}
}

// recordCommitContract writes the dispatch's commit-ownership verdict
// into the run manifest.
//
// The JSON envelope already carries it, but the envelope is ephemeral: a
// consumer that parses it, sees failure and discards stdout leaves
// nothing on disk saying why the dispatch was judged to have failed.
// That is how an 89-minute capture was thrown away with the cause
// unrecoverable from the artifacts. The manifest is the durable record.
//
// Best-effort, and deliberately so: this runs after the work is done and
// after the verdict has already been reported on stdout and via the exit
// code. Failing the command because a diagnostic could not be persisted
// would trade the run for the note about the run.
func recordCommitContract(projectRoot, skill, manifestDir string, contract commitowners.Result) {
	if contract.Skill == "" {
		return // no assertion ran (e.g. --task-commit); nothing to record
	}
	runDir := pipeline.ResolveLatestRunDir(projectRoot, skill, manifestDir)
	if runDir == "" {
		return
	}
	manifestPath := filepath.Join(runDir, "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var m pipeline.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return
	}
	rec := &pipeline.CommitContractRecord{
		Skill:      contract.Skill,
		Declared:   contract.Declared,
		Asserted:   !contract.Skipped,
		SkipReason: contract.SkipReason,
		OK:         contract.OK(),
		Subjects:   contract.Subjects,
	}
	for _, v := range contract.Violations {
		rec.Violations = append(rec.Violations, v.Check+": "+v.Message)
	}
	m.CommitContract = rec

	out, err := yaml.Marshal(&m)
	if err != nil {
		return
	}
	_ = atomicfile.Write(manifestPath, out)
}

// modelUsageRecordsToEnvelope converts manifest model_usage records to
// the envelope shape. nil in → nil out (field omitted).
func modelUsageRecordsToEnvelope(mu map[string]pipeline.ModelUsageRecord) map[string]taskModelUsage {
	if len(mu) == 0 {
		return nil
	}
	out := make(map[string]taskModelUsage, len(mu))
	for model, u := range mu {
		out[model] = taskModelUsage{
			CostUSD:                    u.CostUSD,
			InputTokens:                u.TokensInput,
			OutputTokens:               u.TokensOutput,
			CacheReadInputTokens:       u.TokensCacheRead,
			CacheCreationInputTokens:   u.TokensCacheCreation,
			CacheCreation5mInputTokens: u.TokensCacheCreation5m,
			CacheCreation1hInputTokens: u.TokensCacheCreation1h,
			NumTurns:                   u.NumTurns,
		}
	}
	return out
}

// printTaskSummary emits the human-mode post-run lines.
func printTaskSummary(env taskEnvelope, runErr error) {
	if runErr == nil {
		// The contract's state rides this line, not only the ⚠️ below it.
		// "✅ task X done" is what gets quoted, pasted and carried forward
		// as evidence, and on its own it says the dispatch was fine when
		// the commit-ownership assertion may never have run. A skip is
		// only reported if the summary cannot be quoted without it.
		fmt.Fprintf(os.Stdout, "✅ task %s done in %.1fs — $%.2f, %d turn(s)%s\n",
			env.Skill, env.DurationSeconds, env.CostUSD, env.Usage.NumTurns,
			contractSkipSuffix(env.CommitContract))
	}
	if env.ManifestPath != "" {
		fmt.Fprintf(os.Stdout, "📊 manifest: %s\n", env.ManifestPath)
	}
	if env.Relays > 0 {
		fmt.Fprintf(os.Stdout, "↻ relayed %d time(s) (limits.context_relay); this session relays run %s\n", env.Relays, env.RelayOf)
	}
	if len(env.Commits) > 0 {
		fmt.Fprintf(os.Stdout, "📌 commits: %d\n", len(env.Commits))
		for _, c := range env.Commits {
			fmt.Fprintf(os.Stdout, "   %s\n", c)
		}
	}
	if len(env.CommitsShared) > 0 {
		fmt.Fprintf(os.Stdout, "📌 commits shared with another session's run in this checkout (claimed by neither): %d\n", len(env.CommitsShared))
		for _, c := range env.CommitsShared {
			fmt.Fprintf(os.Stdout, "   %s\n", c)
		}
	}
	printCommitContract(env.CommitContract)
}

// contractSkipSuffix marks a summary line whose commit-ownership
// assertion was never made. Empty when the assertion ran, so a normal
// dispatch reads exactly as it did.
func contractSkipSuffix(res *commitowners.Result) string {
	if res == nil || !res.Skipped {
		return ""
	}
	return " — commit contract NOT asserted"
}

// printCommitContract reports the per-dispatch assertion. A skipped
// assertion is announced rather than passed over in silence — "we could
// not look" and "we looked and it was clean" are different answers, and
// a check that says nothing when it could not run reads as a pass.
func printCommitContract(res *commitowners.Result) {
	switch {
	case res == nil:
		return
	case res.Skipped:
		fmt.Fprintf(os.Stdout, "⚠️  commit contract not asserted: %s\n", res.SkipReason)
	case !res.OK():
		fmt.Fprintf(os.Stdout, "❌ commit contract violated (%s):\n", contractRole(*res))
		for _, v := range res.Violations {
			fmt.Fprintf(os.Stdout, "   %s: %s\n", v.Check, v.Message)
		}
	}
}

func contractRole(res commitowners.Result) string {
	if res.Declared {
		return "declared committer"
	}
	return "non-committer"
}

// gitHeadFull returns the full HEAD SHA of projectRoot, or "" when
// unavailable (not a repo, no commits yet, git missing).
func gitHeadFull(ctx context.Context, projectRoot string) string {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = projectRoot
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

// checkPromptFlag is DR-11 (D277): --prompt, --prompt-file and --handoff
// reach the skill only through --prompt-flag, and without it ape used to
// start the skill and drop the text without a word — a person resuming a
// handoff that way got a session that never read it. changed reports
// whether a flag was given.
func checkPromptFlag(changed func(string) bool, promptFlagName string) error {
	if promptFlagName != "" {
		return nil
	}
	for _, f := range []string{"prompt", "prompt-file", "handoff"} {
		if changed(f) {
			return fmt.Errorf("--%s needs --prompt-flag <the skill's flag>: without it the text is dropped "+
				"(no APEX skill takes one — resume a handoff by pasting its continuation line after /clear)", f)
		}
	}
	return nil
}
