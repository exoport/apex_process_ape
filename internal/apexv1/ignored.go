package apexv1

import (
	"bytes"
	"context"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// What git ignores in a v1 project, where that decides whether the
// project works in another checkout (H25d, H27).

func init() {
	findingRules = append(findingRules, rule("", `^\.claude/settings\.json is ignored by git`, "settings.ignored"))
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{"settings.ignored", 1, ActionPerson, ".claude/settings.json is ignored by git: ape's hooks there reach no other checkout (only ape task sessions get them inline); un-ignore it and commit it"},
	)
}

// gitIgnored returns which of paths (relative to dir) git's ignore rules
// match. noIndex judges the rules alone, tracked or not; without it a
// tracked path is never ignored. nil outside a repository.
func gitIgnored(ctx context.Context, dir string, noIndex bool, paths ...string) []string {
	args := []string{"check-ignore", "--stdin"}
	if noIndex {
		args = append(args, "--no-index")
	}
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	c.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	var out bytes.Buffer
	c.Stdout = &out
	// Exit 1 is "none of them is ignored", 128 is not a repository: both
	// leave stdout empty.
	_ = c.Run()
	var ignored []string
	for l := range strings.SplitSeq(out.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			ignored = append(ignored, l)
		}
	}
	return ignored
}

// inGitRepo reports whether dir is inside a git work tree.
func inGitRepo(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// settingsIgnoredFindings reports a .claude/settings.json git ignores
// (H25d): the hooks ape merged there would not reach another checkout of
// the project, so only `ape task` sessions, which get them inline, would
// run them. A tracked settings.json is shared whatever the rules say.
func (p *Project) settingsIgnoredFindings(ctx context.Context) []Finding {
	const file = ".claude/settings.json"
	rel := p.Rel(p.SettingsJSON())
	if !inGitRepo(ctx, p.Root) || len(gitIgnored(ctx, p.Root, false, rel)) == 0 {
		return nil
	}
	return []Finding{{File: file, Message: ".claude/settings.json is ignored by git, so ape's hooks there reach no other checkout " +
		"(only `ape task` sessions get them inline) — un-ignore it (e.g. `!.claude/settings.json` in .gitignore) and commit it"}}
}

// SettingsIgnored is settingsIgnoredFindings' message, "" when git shares
// .claude/settings.json (or this is not a repository).
func (p *Project) SettingsIgnored(ctx context.Context) string {
	if fs := p.settingsIgnoredFindings(ctx); len(fs) > 0 {
		return fs[0].Message
	}
	return ""
}

// stateTracked are the _apex/state/ records git tracks (backlog 26–28);
// everything else there is ignored by its whitelist .gitignore.
var stateTracked = []string{".gitignore", "milestones.json", "decisions.json"}

// stateIgnoredProbes are paths ape writes under _apex/state/ that must be
// ignored: run records, logs, hook verdicts and the limit counters.
var stateIgnoredProbes = []string{"runs/x", "logs/x", "hooks/x", "attempts.json", "limit-stops.json", "project.json"}

// StateIgnoreProblems checks _apex/state/'s whitelist (H27): milestones.json
// and decisions.json tracked (never ignored), everything else ape writes
// there ignored and untracked. Empty when it holds, or outside a repository.
func (p *Project) StateIgnoreProblems(ctx context.Context) []string {
	if !inGitRepo(ctx, p.Root) {
		return nil
	}
	state := p.StateRel()
	var problems []string
	probe := func(names []string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = path.Join(state, n)
		}
		return out
	}
	for _, f := range gitIgnored(ctx, p.Root, true, probe(stateTracked[1:])...) {
		problems = append(problems, f+" is ignored: ape commits it, so it must be tracked")
	}
	ignored := gitIgnored(ctx, p.Root, true, probe(stateIgnoredProbes)...)
	for _, f := range probe(stateIgnoredProbes) {
		if !slices.Contains(ignored, f) {
			problems = append(problems, strings.TrimSuffix(f, "/x")+" is not ignored: ape's run records, logs and counters stay out of git")
		}
	}
	if out, err := git(ctx, p.Root, "ls-files", "--", state); err == nil {
		for f := range strings.SplitSeq(out, "\n") {
			rel := strings.TrimPrefix(f, state+"/")
			if f != "" && !slices.Contains(stateTracked, rel) {
				problems = append(problems, f+" is tracked: only "+strings.Join(stateTracked[1:], " and ")+" are (git rm --cached it)")
			}
		}
	}
	for _, name := range stateTracked[1:] {
		f := path.Join(state, name)
		if !fileExists(filepath.Join(p.Root, filepath.FromSlash(f))) {
			continue
		}
		if out, err := git(ctx, p.Root, "ls-files", "--", f); err == nil && out == "" {
			problems = append(problems, f+" exists but is not committed")
		}
	}
	return problems
}
