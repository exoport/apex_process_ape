package apecmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// H27: `ape doctor` in an APEX v1 project runs the host checks plus the v1
// ones, and none of v0's: a v1 project removed by design what those report
// missing, so under --strict they would fail every healthy project.

// v0OnlyChecks report on APEX v0's install and project data. They do not
// run in a v1 project; in a v0 one they run as before (a v0 project's own
// pinned ape is the one that should be answering, which apex.v0 says).
var v0OnlyChecks = map[string]bool{
	"framework.metadata":                 true,
	"pipelines.project":                  true,
	"operating_rules.fragment":           true,
	"operating_rules.import":             true,
	"operating_rules.orchestrator_skill": true,
	"framework.terminal_contracts":       true,
	"framework.change_routes":            true,
	"config.resolved":                    true,
	"config.folders":                     true,
	"registry.drift":                     true,
	"story.frontmatter":                  true,
	"sprint.divergence":                  true,
	"output.ape_ignored":                 true,
	"memory.size":                        true,
	"migration.pending":                  true,
	"apex.v0":                            true,
}

// v1OnlyChecks are the APEX v1 project checks; outside a v1 project they
// do not run. (plugins.project predates the split and keeps its SKIP.)
var v1OnlyChecks = map[string]bool{
	"project.check":    true,
	"hooks.project":    true,
	"claude.version":   true,
	"state.gitignore":  true,
	"settings.shared":  true,
	"hooks.pre-commit": true,
	"hooks.prompt":     true,
}

func init() {
	allChecks = append(allChecks,
		doctorCheck{Name: "apex.v0", Run: checkApexV0},
		doctorCheck{Name: "project.check", Run: checkV1Project},
		doctorCheck{Name: "hooks.project", Run: checkV1Hooks},
		doctorCheck{Name: "claude.version", Run: checkClaudeVersion},
		doctorCheck{Name: "state.gitignore", Run: checkV1StateGitignore},
		doctorCheck{Name: "settings.shared", Run: checkV1SettingsShared},
		doctorCheck{Name: "hooks.pre-commit", Run: checkV1PreCommitHook},
		doctorCheck{Name: "hooks.prompt", Run: checkV1PromptHook},
	)
}

// scopeChecks keeps the checks that apply to the project at env: in a v1
// project the host checks and the v1 ones, elsewhere everything but the v1
// ones. An explicit --only list is run as named.
func scopeChecks(checks []doctorCheck, env doctorEnv, only map[string]struct{}) []doctorCheck {
	if len(only) > 0 {
		return checks
	}
	_, v1 := v1ProjectAt(env.ProjectRoot)
	out := make([]doctorCheck, 0, len(checks))
	for _, c := range checks {
		if v1 && v0OnlyChecks[c.Name] || !v1 && v1OnlyChecks[c.Name] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// checkApexV0 says so when the project is an APEX v0 one: this ape retired
// v0's commands (backlog 38), so its skills need the project's pinned v0 ape.
func checkApexV0(_ context.Context, env doctorEnv) CheckResult {
	if !apexv1.IsV0Project(env.ProjectRoot) {
		return CheckResult{Status: StatusInfo, Message: "not an APEX v0 project"}
	}
	return CheckResult{
		Status:      StatusWarn,
		Message:     "an APEX v0 project: this ape retired the v0 commands its skills call",
		Remediation: apexv1.V0ProjectMsg + ".",
		FixCommand:  "ape framework update",
	}
}

// msgNotV1Project is the skip message of every v1 doctor row outside an
// APEX v1 project.
const msgNotV1Project = "not an APEX v1 project"

// checkV1Project is `ape project check` minus the hooks and plugins, which
// have their own rows.
func checkV1Project(_ context.Context, env doctorEnv) CheckResult {
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		return CheckResult{Status: StatusSkip, Message: msgNotV1Project}
	}
	fs := p.Check()
	if len(fs) == 0 {
		return CheckResult{Status: StatusOK, Message: "project check: green"}
	}
	return CheckResult{
		Status:      StatusFail,
		Message:     fmt.Sprintf("%d finding(s): %s", len(fs), joinFindings(fs, 3)),
		Remediation: "`ape project check` lists every finding with its fix.",
		FixCommand:  "ape project check --cwd " + p.Root,
	}
}

// checkV1Hooks reports ape's hooks missing from, or altered in,
// .claude/settings.json: a person's session there would run without them.
func checkV1Hooks(_ context.Context, env doctorEnv) CheckResult {
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		return CheckResult{Status: StatusSkip, Message: msgNotV1Project}
	}
	fs := p.CheckHooks()
	if len(fs) == 0 {
		return CheckResult{Status: StatusOK, Message: fmt.Sprintf("ape's %d hooks registered in .claude/settings.json", apexv1.RequiredHookCount())}
	}
	return CheckResult{
		Status:      StatusFail,
		Message:     joinFindings(fs, 4),
		Remediation: "`ape project init` merges ape's hooks back (existing keys and hooks are kept); `ape task` sessions get them inline either way.",
		FixCommand:  "ape project init --cwd " + p.Root,
	}
}

// checkV1PreCommitHook reports git's pre-commit hook when it does not run
// ape's secret scan (Q89, project check's hooks.pre-commit-absent notice):
// a fresh clone has none, as git never versions .git/hooks.
func checkV1PreCommitHook(ctx context.Context, env doctorEnv) CheckResult {
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		return CheckResult{Status: StatusSkip, Message: msgNotV1Project}
	}
	n := p.PreCommitHookNotice(ctx)
	if n == nil {
		return CheckResult{Status: StatusOK, Message: "git's pre-commit hook runs `" + apexv1.HookCmdPreCommit + "` (or there is no git work tree)"}
	}
	res := CheckResult{Status: StatusWarn, Message: n.Code + ": " + n.Message, Remediation: n.Fix}
	if strings.Contains(n.Fix, "ape project init") {
		res.FixCommand = "ape project init --cwd " + p.Root
	}
	return res
}

// checkV1PromptHook reports the UserPromptSubmit hook missing from, or
// altered in, .claude/settings.json (E2, T10; project check's notice
// hooks.prompt-absent): without it no prompt is recorded, so a person act
// from a Claude Code session is refused — safe, and a person's terminal
// still works, hence a WARN.
func checkV1PromptHook(_ context.Context, env doctorEnv) CheckResult {
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		return CheckResult{Status: StatusSkip, Message: msgNotV1Project}
	}
	why := p.PromptHookProblem()
	if why == "" {
		return CheckResult{Status: StatusOK, Message: "`" + apexv1.HookCmdUserPrompt + "` records the person's prompts (UserPromptSubmit): person acts can be granted in Claude Code (E2)"}
	}
	return CheckResult{
		Status:  StatusWarn,
		Message: why + ": a person act from a Claude Code session is refused (no prompt is recorded to grant it)",
		Remediation: "`ape project init` merges ape's hooks back (existing keys and hooks are kept); " +
			"meanwhile a person runs person acts from a terminal outside Claude Code.",
		FixCommand: "ape project init --cwd " + p.Root,
	}
}

// checkClaudeVersion reports the installed Claude Code's version. Report
// only: the range v1 is tested against is H33, later.
func checkClaudeVersion(ctx context.Context, _ doctorEnv) CheckResult {
	path, err := exec.LookPath("claude")
	if err != nil {
		return CheckResult{Status: StatusSkip, Message: "claude is not on PATH (see claude.binary)"}
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(vctx, path, "--version").Output()
	v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(out)), "(Claude Code)"))
	if v == "" {
		return CheckResult{Status: StatusWarn, Message: path + " --version printed nothing", Remediation: "Reinstall Claude Code."}
	}
	return CheckResult{Status: StatusOK, Message: "Claude Code " + v + " (reported only: no tested range yet)"}
}

// checkV1StateGitignore checks _apex/state/'s whitelist: milestones.json
// and decisions.json tracked, ape's run records, logs and counters ignored.
func checkV1StateGitignore(ctx context.Context, env doctorEnv) CheckResult {
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		return CheckResult{Status: StatusSkip, Message: msgNotV1Project}
	}
	problems := p.StateIgnoreProblems(ctx)
	if len(problems) == 0 {
		return CheckResult{Status: StatusOK, Message: p.StateRel() + "/: milestones.json and decisions.json tracked, the rest ignored"}
	}
	return CheckResult{
		Status:  StatusFail,
		Message: strings.Join(problems, "; "),
		Remediation: "Restore " + p.StateRel() + "/.gitignore to the whitelist ape writes (" +
			strings.ReplaceAll(strings.TrimSpace(runlog.V1StateGitignore), "\n", " ") +
			"), and `git rm --cached` what is tracked there besides those two records.",
	}
}

// checkV1SettingsShared reports a .claude/settings.json git ignores (H25d).
func checkV1SettingsShared(ctx context.Context, env doctorEnv) CheckResult {
	p, ok := v1ProjectAt(env.ProjectRoot)
	if !ok {
		return CheckResult{Status: StatusSkip, Message: msgNotV1Project}
	}
	if msg := p.SettingsIgnored(ctx); msg != "" {
		what, fix, _ := strings.Cut(msg, " — ")
		return CheckResult{Status: StatusFail, Message: what, Remediation: fix}
	}
	return CheckResult{Status: StatusOK, Message: ".claude/settings.json is shared through git (not ignored)"}
}

// joinFindings renders up to n findings on one line, then a count of the rest.
func joinFindings(fs []apexv1.Finding, n int) string {
	parts := make([]string, 0, min(n, len(fs)))
	for i, f := range fs {
		if i == n {
			break
		}
		parts = append(parts, f.String())
	}
	s := strings.Join(parts, "; ")
	if len(fs) > n {
		s += fmt.Sprintf("; and %d more", len(fs)-n)
	}
	return s
}
