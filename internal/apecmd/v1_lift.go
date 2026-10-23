package apecmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

func newProjectLiftCmd() *cobra.Command {
	var f v1Flags
	var opt apexv1.LiftOptions
	cmd := &cobra.Command{
		Use:   "lift",
		Short: "Lift an APEX v0 project to v1 in one commit (the mechanical half of the migration)",
		Long: `Lift an APEX v0 project (framework v0.15.0–v0.31.x) to the v1 layout, in ONE commit
whose subject starts "apex: lift" — the v1 epoch (once the regression command has run,
a second commit records its verdict, below). The judgment half (which rule lives
where, what M1 delivers) is apex-plan's, from the disposition file this writes.

Before: a person tags the commit to lift and pushes the tag. The tag is the archive of
everything the lift deletes; v0 files stay readable as ` + "`git show apex-v0-final:<path>`" + `.
The lift checks the tag on the current branch's upstream, so the branch needs one
(D162-5): ` + "`git push -u origin <branch>`" + ` once, if ` + "`git rev-parse @{u}`" + ` fails.

  git push -u origin "$(git branch --show-current)"   # only when the branch has no upstream
  git tag -a apex-v0-final -m "apex-v0 final" && git push origin apex-v0-final
  ape project lift --dry-run        # the whole plan, nothing written
  ape project lift

Which ape: the lift runs on the binary invoked, whatever the project's .bingo/ape.mod
pins — it is exempt from the pin swap, since a v0 pin names an ape with no v1 commands.
The output's first lines say which binary runs (version, path) and what becomes of the
pin: when this ape is a release (vX.Y.Z or vX.Y.Z-rc.N) and bingo is on PATH, the lift
commit moves the pin to it (bingo get); otherwise the lift prints the one command that
does, and until the pin moves every other v1 command there needs APE_TOOLCHAIN=local —
every git commit too (the pre-commit hook runs ape) and every Claude session in the
project (its hooks run ape: start it as APE_TOOLCHAIN=local claude).

Pre-flight (refused, exit 1): a v0 install (_apex/framework.yaml records a v0 version,
or did in git history) and no v1 layout or earlier lift (lift.not-v0); a version in
v0.15.0–v0.31.x and a resolvable _apex/config.yaml (lift.unsupported-layout); a clean
tree (lift.dirty); apex-v0-final exists (lift.no-tag), is HEAD (lift.tag-not-head),
and is on the current branch's upstream remote at the same commit, checked with
git ls-remote — the one network call (lift.tag-not-pushed; lift.remote-unreachable);
and git ignores nothing the v1 project commits (lift.claude-ignored) — above all
.claude/settings.json, where the v1 hooks go, and .claude/skills/, where the framework's
skills go — judged with the root .gitignore as the lift's commit leaves it (the .claude/
shape below, so a whole-folder .claude ignore there is no refusal); what is refused is an
ignore ape does not rewrite (a nested .gitignore, .git/info/exclude, core.excludesFile, a
later line of the root .gitignore), named with its rule: remove it, commit, and re-tag.
--dry-run lists every problem, not the first, and writes nothing.

The commit:
  deletes        the v0 families, at the paths _apex/config.yaml's folder variables
                 resolve to: the skills the v0 install manifest lists (only those —
                 never a project's own; ones git never tracked, under an ignored
                 .claude/, leave the working tree after the commit), the v0 _apex
                 tables and pipelines, stories, the sprint tracker, retrospectives, planning (PRD, architecture,
                 epics, proposals, release records), ADR and pattern records and
                 indexes, capability and feature registries, deferred records, team
                 memory, project-context. Kept: docs, CHANGELOG, evidence, research,
                 code, _apex/framework.yaml (the migration ledger)
  writes         ADR-0001, APEX's standard ADR "Record architecture decisions"
                 (D205): development/decisions/0001-record-architecture-decisions.md,
                 accepted, the installed payload's _apex/standards/adr-0001.md (else
                 ape's own copy), recorded in decisions.json by "APEX standard (ape
                 project lift)" with standard: {framework_version, sha256}. The
                 apex-v0 governance standard ("Use Markdown Architectural Decision
                 Records") is identified by its canon source — a record whose
                 output_document is governance/adrs/adr-NNNN_use-markdown-adrs.md, or
                 that canon record copied from the governance clone — not by its
                 number: it is not converted (its text stays at apex-v0-final), its
                 citations go to ADR-0001, and the disposition and the commit name it
                 as replaced. A project with no such record gets the standard all the
                 same. JSON: standard_adr {action: created|replaced, path, source,
                 framework_version, replaces}
  converts       every other ADR (accepted, superseded, proposed) into
                 development/decisions/NNNN-<slug>.md, renumbered from 0002 in the
                 original order, with Status, Date, Supersedes and a "Lifted from:"
                 line. Nothing is invented: no date → git's first date of the file; no
                 Enforced by → "review" (what ape decision new writes); a missing
                 Context/Decision/Consequences → the section, marked "not recorded";
                 each is listed as a gap. Accepted and superseded ones are recorded
                 in _apex/state/decisions.json (by: lift), so their text is held from
                 here on. Canon ADRs the project cites and has no record of are copied
                 from the governance clone when _apex/config.yaml resolves one
  rewrites       every ADR citation (ADR-NNNN, and paths to the old records) in
                 Markdown and in code comments to the new number. Only that
                 four-digit form is the project's own: a short ADR-08 cites
                 another decision log, and is listed as unresolved, never
                 rewritten — even where the project has an ADR-0008
  strips         epic, story and deferred ids from code comments — Story 3.2, Epic 12,
                 (3-2) (a known story key), DW-…, paths into the deferred store or the
                 implementation folder — keeping the rest of the comment; a comment
                 line that was only an id goes, and an edited list item keeps its
                 marker ("- ", "2. "). A story takes its fragments with it (Story 3.2,
                 Task 2 · Subtask 3.1 of Story 5.10 · AC 2) and nothing else: what
                 follows them is kept, ADR ids renumbered
                 ("(Story 3.2, ADR-0019 decision 3)" → "(ADR-0019 decision 3)",
                 "(Story 3.2, 2026-08-08)" → "(2026-08-08)"), and the separator a
                 removal leaves by a bracket or a mark goes too
                 ("(ADR-0019, Story 3.2)" → "(ADR-0019)"); a "," ";" or ":" ending a
                 line goes only when the id was right before it ("proves," and "bump:"
                 stay). Comments only: ids in identifiers, strings and directives are
                 listed, not edited — and so are ids in a data-shaped comment, which a
                 tool may parse: one opening with a lowercase key ("# owner: …") or a
                 record of key: value fields ("# pending: q | owner: …"), and so is a
                 comment using an id or a removed path as a possessive
                 ("Story 3.2's own test", "(Story 2.3)'s own",
                 "development/deferred/'s Gates"), which no removal keeps whole. An
                 aside split over comment lines loses both brackets with its id, and a
                 mark a removal leaves at the start of a line moves to the end of the
                 line above ("(ADR-0008," over "Story 1.3), extracted" → "(ADR-0008)"
                 over "extracted"; a separator goes): no edited line starts with ")"
                 or "." or loses the blank after its "//". Only a bracket pair the
                 removal emptied goes: run()'s, []byte stay.
                 Tokenized: Go (go/scanner), shell, Python, JS/TS (JSX/TSX: comments
                 that begin their line only), Dart, YAML, Makefile (recipe lines are
                 the shell's: left), C/C++. A file the tokenizer cannot read, a
                 generated file, or an --exclude match is left and listed. Go: the
                 token stream without comments is checked unchanged; other languages:
                 the text outside comments, re-tokenized, is checked unchanged (blank
                 lines and trailing whitespace aside) — a file failing its check is
                 left unedited and listed. An edited Go file that was gofmt-clean at
                 the tag is formatted with go/format (an id's removal can leave a
                 line gofmt reads as a heading or a code block); one that then does
                 not format is left unedited and listed, and one that was not clean
                 at the tag is edited, not reformatted, and listed
  carries        ## Code Validation (project-context.md) into AGENTS.md ## Commands,
                 and the runnable ones, joined with &&, into regression: in
                 _apex/config.yaml (rewritten to project_name, development_folder,
                 regression). A command with a placeholder — <…>, {…} (not a
                 shell ${…}) or a lone … — is listed, not run; Go's ./... is no
                 placeholder. A trailing " # …" shell comment is dropped first.
                 A code block whose lines hold a construct (for … done) is one
                 command, ( set -e ⏎ its lines ⏎ ), so it fails on its first
                 red line wherever it runs as written; its AGENTS.md bullet
                 reads "The Code Validation block, as one command:". A block
                 line that is exactly "source <file>" is written ". <file>"
                 (RN-1: ape runs checks with sh, which has no source where it
                 is dash), each listed in gates.rewrites, the .md and the output
  lays down      the v1 skeleton, as ` + "`ape project init --brownfield`" + ` (CLAUDE.md keeps its
                 content, loses the v0 apex:managed block and gains @AGENTS.md;
                 AGENTS.md gains "Talk to APEX: type ` + "`/apex-orchestrator`" + ` in Claude
                 Code." when it does not name /apex-orchestrator; models: names
                 apex-loop, never apex-orchestrator), hot/.gitignore (` + "`*`" + `, when hot/ has none) included: it ignores
                 itself, so it is not in the lift commit; and git's pre-commit hook,
                 the secret scan, when the repository has none (an existing one is
                 kept; the output's "git hook" line says which; see ape hook
                 pre-commit --help)
  fences         a library's module fences (W7): when the v0 _apex/config.yaml says
                 library: true, the v1 config keeps the line and, with a go.mod at the
                 root, the lift commit holds a go.mod fence in .claude/, _apex/, the
                 development folder and v0's root evidence/ (JSON "fences"; a dry run
                 lists them) — see ape project fence --help. Without the key, a Go
                 module with no main package gets the suggestion (a "suggest" line,
                 JSON "suggest_library"); the lift never sets it
  shapes         the root .gitignore's .claude/ rules (D203), as ape project init does:
                 .claude/*, !.claude/settings.json, !.claude/skills/, CLAUDE.local.md —
                 a whole-folder .claude ignore (pdfa's .claude/) rewritten in place, else
                 the block appended; every other line kept. settings.json and skills/ are
                 the team's, the rest of .claude/ personal. The output's ".gitignore:" line
                 and JSON "gitignore" ({action: added|rewritten|already, revealed}) say
                 which; "revealed" lists the files under .claude/ it un-ignored and did not
                 commit (a skill of your own: commit it or ignore it by name)
  writes         development/lift-disposition.md (16 KB, unless its candidates need
                 more; they are never cut, whatever its size): the binding-rule
                 candidates, EVERY one, under a line giving their count and the
                 file's size ("N rows, numbered 1–N … read on with offset/limit
                 until row N"): from project-context.md, every section, an
                 overview too — its list items of three words or more (not one
                 that is only links) and its rule-shaped paragraphs (must,
                 never, always, do not, avoid, only, required, prefer, shall,
                 forbidden), whole, and the notes in ## Code Validation's code
                 blocks (a command's # comment, with its command, or a run of
                 comment lines), whose commands go to AGENTS.md ## Commands.
                 CLAUDE.md (outside the apex-v0 managed block) and team-memory
                 are read whole: every list item, paragraph and table body row
                 of three words or more, whatever its wording, and every
                 code-block note (a fenced markdown example's lines are not); a
                 team-memory entry that gives nothing else is listed by its
                 title. An HTML comment is text: indented under a list item it
                 continues the item, and a comment of several lines elsewhere
                 is a paragraph. Each CLAUDE.md import the lift drops — the
                 managed block's @_apex/apex-operating-rules.md, an @ import of
                 any other file it deletes — is one row naming the file, its
                 size, and git show apex-v0-final:<path>; the operating rules' row
                 also names the two carried already: tags and pushes by ape hook
                 pre-tool, zsh quoting by ape hook session-start. Grouped by source and section (JSON
                 rule_candidates[].section, the heading path); text found twice is
                 listed once with both sources (also_in). An empty Disposition
                 column for apex-plan, then a fresh read-only review, then the
                 person, who deletes it. Then "Code and
                 tests that read deleted v0 state": every kept code, test, script,
                 Makefile or CI file (not docs, not evidence) that names a path the
                 lift deletes outside its comments (SQL's -- and /* */ too) — by its
                 path (./ and ../ too), a path under a deleted folder at a segment
                 boundary, or the base name of a deleted file when it has an extension
                 and no other file has it and its line reads it (cat, test -f, a
                 redirection, an open/read/glob/Join call, a quoted path). Neither
                 counts in an echo's or a printf's words or a test's or logger's
                 message (t.Errorf, fmt.Errorf, log.*, console.*, print(, a testify
                 message), a message string a call opens on one line and carries on
                 the next included, unless a reading call or command on its line
                 takes it — one line per file with
                 what it reads (--json and the report: v0_state_readers, {file,
                 reads}); re-point each to v1 state or retire it. Then, as summaries:
                 other code and CI that still names removed paths (in a comment, or a
                 file that is not code), one line per file with its count and first
                 reference, and edited files a guard hashes — those a lock file or a
                 hasher names one line per file (at most 50), those only a folder ties
                 to a hasher one line per hasher ("57 files in pkg/x share a folder
                 with guard_test.go (folder match only)"); ADR gaps; ids left and
                 files not edited, counted per file; references in evidence and docs,
                 counted; readings at the tag. To fit 16 KB the summaries shrink to
                 counts, then the ADR table to the ADRs with gaps; the v0 state
                 readers grow the file past 16 KB, up to 48 KB, and past that go to
                 counts (saying so). The candidates are never cut: the file is as
                 big as they need (cortex: 146 KB, 441 rows), since the JSON below
                 is not in any other clone. Every non-rule list whole goes to
                 _apex/state/lift-disposition.json after the commit: ignored by the
                 state folder's whitelist, never committed, in this checkout only

A byte-identity perimeter: a file a test, CI script or lock file hashes goes red once
the lift edits a comment in it. The plan lists, as "hashed", the edited files that a lock
file names, that a test or CI file names within 3 lines of a hash word or call (sha256,
sha256sum, checksum, fingerprint, byte-identical, crypto/sha3, hashlib, …), or that sit
beside a hashing test that globs or names them — run --dry-run, then --exclude them. A
test or CI file hashes only when a hash word or call stands outside its comments: one
that says "byte-identical" in prose hashes nothing. Documents and evidence (.md, .json,
.log, .txt, anything under an evidence folder) never count as hashers. The order is
how sure the match is: what a lock file names, then what a hasher names (by path, or a
test beside the file by its name), then what only its folder ties to a hasher (a glob,
a ReadDir). --dry-run lists every one, text and --json; a real lift's output lists at
most 50, and the disposition file at most 50 of the first two kinds and the folder kind
as one line per hasher; edited_but_hashed_total counts them all, and the report has
every one. It is a heuristic: a copy compared byte for byte with no hash (an
embedded mirror of a hashed file) needs its own --exclude.

The regression command runs twice (--no-verify-run skips both): at the tag, before
anything is written, and at the lift commit — the baseline red gates are judged against.
regression_verdict says whose a red is: green · red-at-tag (red there too, failing as it
did: pre-existing, for apex-plan to roadmap) · red-at-tag-new-failures (red there too, but
tests fail that did not at the tag — the LIFT broke those, named in
regression_new_failures) · red-after-lift (green at the tag: the LIFT broke it — fix it
in a commit, or reset to the tag and lift with --exclude) · cannot-run (exit 126/127, or
an output saying a command was not found — "command not found", "No such file or
directory" — with make's "Error 127", and no failing test named: a tool it runs is
missing here, which is not a red; read the same at both points; when the output says a
bash/zsh builtin was not found — "sh: 2: source: not found" — the note names it instead:
/bin/sh here has no such builtin, so write it in POSIX sh, "." for source, RN-1) · red-no-baseline ·
flaky (RG-1: a red-after-lift or red-at-tag-new-failures command is run once more at the
lift commit, and passed that time: a flake, not the lift's; record it, plan no fix; a
red-at-tag-new-failures command whose re-run fails only as at the tag is red-at-tag, its
note naming the new failures that did not reproduce (RV-1); red again keeps its verdict,
its note saying so; regression_commands[] / regression.commands[] carry the re-run's
exit_rerun and log_rerun). A run
that exits 0 but whose output names failing tests (as below) is red for the verdict, by
those names, and its note says "exit 0, but the output names N failing tests": green at
the tag, it is red-after-lift with the names in regression_new_failures (a ( … ) block
without set -e passes on its last line's exit alone). A run red at the lift commit and
green at the tag lists the failing tests its output names in regression_new_failures
whatever its exit code (less any the tag's run named), at most 50. Red at both, the
failing tests are compared by name where the output names them (go test "--- FAIL:",
gotestsum "=== FAIL: <pkg> <test>" and "✖ <pkg>", pytest "FAILED", cargo "... FAILED",
jest "✕", flutter and dart test "<name> [E]", the counters before the name dropped);
otherwise by output hash — tool-install lines ("(re)installing …", as bingo
prints on a first run) and durations (0.12s, 4m19s, (12ms)) left out — and a changed
output stays red-at-tag with the note "the output changed after the lift", never called
pre-existing. A command that is a chain joined with && at its top level (outside quotes,
( ), { }, $( ) and backticks; ;, || and pipes stay inside a command) runs command by
command at both points, past a red one, so a red lint cannot hide the tests after it:
each command is judged on its own, regression_verdict and the regression_* fields are
the worst one's, and --json lists every command in regression_commands (cmd, verdict,
note, new_failures, regression, regression_at_tag; logs regression-<n>.log and
regression-<n>-at-tag.log). From a command that changes the shell's state (cd, export,
source, NAME=value, …) on, the chain stays one command. A ( … ) block of several lines
that splits (each line a simple command, a first set -e line dropped, as a change's verify splits it) is, for these
two runs, one command per line in that chain, not its lines joined by &&: each line runs
past a red one and is judged on its own in regression_commands, and its log opens with
"split into N commands: the lift runs each line of the ( … ) block on its own"; a block
that does not split (a line that changes the shell's state, a construct left open) stays
one command, run as written. The exit code stays 0: the
commit stands either way, and the verdict is in --json and the output.

The verdict is also recorded where apex-plan reads it (RG-6): ` + "`regression`" + ` in
_apex/state/lift-disposition.json — verdict, note, regression_new_failures, commit, and
commands (cmd, verdict, note, exit_at_tag, exit_after, regression_new_failures, log,
log_at_tag), one per command run — and a "## Regression" section of the disposition
file (the verdict and its note, then one line per command with its exits and logs),
committed on its own after the lift commit as "apex: lift regression verdict"
(regression_commit in --json). With no run (no command, --no-verify-run) the lift
commit's disposition file says why, and the JSON has the note with an empty verdict.

--from-update is how the framework's v1.0.0 migration entry runs it inside
` + "`ape framework update`" + `: uncommitted changes under .claude/skills/, _apex/ (not
config.yaml or state/), .aboard/ and .gitignore are the install's, left out of the lift
commit for the update's own; skills the v1 install placed are never deleted. An untracked
.claude/settings.json (the update's .claude/ shape un-ignored it) is no refusal: the lift
merges the v1 hooks into it and commits it. The regression verdict lines go to stderr,
which the update shows (its stdout is not).

Exit codes: 0 lifted (or planned) · 1 refused or failed (code in --json) · 2 usage.`,
		Example: "  ape project lift --dry-run\n  ape project lift --json\n  ape project lift --exclude 'schema/core/**' --exclude deploy/init/00-bootstrap.sh",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := f.cwd
			if dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				dir = apexv1.FindRoot(wd)
			}
			opt.Run = f.runOptions()
			self, _ := os.Executable()
			if r, rerr := filepath.EvalSymlinks(self); rerr == nil {
				self = r
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			opt.Ape = liftApe(absDir, Version, self, opt.DryRun, func() bool {
				_, err := findBingo(cmd.Context(), absDir)
				return err == nil
			})
			res, err := apexv1.Lift(cmd.Context(), dir, opt)
			if err != nil {
				var partial any
				if res != nil {
					partial = res
				}
				return v1Fail(cmd, &f, err, partial)
			}
			// RG-6: inside `ape framework update` only a command's stderr
			// is shown, so the verdict goes there (and not twice); FR-2
			// (D277): so do the lines saying which ape ran and where the
			// pin moved, which the update swallowed.
			if opt.FromUpdate && !res.DryRun {
				printLiftApe(cmd.ErrOrStderr(), res)
				printLiftWarnings(cmd.ErrOrStderr(), res)
				printLiftRegression(cmd.ErrOrStderr(), res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) { printLift(w, res, !opt.FromUpdate) })
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", false, "print the whole plan; write nothing (pre-flight problems are reported, not refused)")
	cmd.Flags().BoolVar(&opt.FromUpdate, "from-update", false, "run inside ape framework update (the v1.0.0 migration entry): the install's uncommitted files are left to it")
	cmd.Flags().BoolVar(&opt.NoVerifyRun, "no-verify-run", false, "do not run the regression command (at the tag, and after the commit)")
	cmd.Flags().StringArrayVar(&opt.Exclude, "exclude", nil, "a glob (** allowed) of files the lift must not edit, e.g. a byte-identity perimeter; repeatable")
	return cmd
}

// releaseVersionRe is a version a pin can name and bingo can fetch: a
// release or a release candidate, not a dev or pseudo-version build.
var releaseVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-rc\.\d+)?$`)

// liftApe says which ape runs the lift and what becomes of the project's
// pin (L1): moved to this version in the lift commit by bingo, when this
// is a release and bingo is here; otherwise the one command that moves it.
// dryRun plans the move without it.
func liftApe(root, running, self string, dryRun bool, haveBingo func() bool) *apexv1.LiftApe {
	a := &apexv1.LiftApe{Version: running, Path: self}
	pin, err := findApePin(root)
	if err != nil || pin == nil || pin.root != root {
		if err != nil {
			a.PinNote = err.Error()
		}
		return a
	}
	a.PinFile, a.Pinned = ".bingo/ape.mod", pin.version
	if sameApeVersion(running, pin.version) {
		a.PinNote = "the pin is this version already"
		return a
	}
	tag := "v" + trimV(running)
	a.PinCommand = "bingo get " + apeModulePath + "@" + tag
	switch {
	case !releaseVersionRe.MatchString(running):
		a.PinNote = "this ape (" + running + ") is no release a pin can name: the lift leaves " + pin.version +
			"; once this version is published, run " + a.PinCommand + " and commit .bingo/; until then " + apexv1.LiftPinLocal
	case !haveBingo():
		a.PinNote = "no bingo found (not on PATH, nor the project's own in $GOBIN): after the lift, run " + a.PinCommand + " and commit .bingo/; until then " + apexv1.LiftPinLocal
	case dryRun:
		a.PinNote = "the lift moves the pin from " + pin.version + " to " + tag + " (" + a.PinCommand + ") in its commit"
	default:
		a.MovePin = func(ctx context.Context) error { return movePin(ctx, io.Discard, root, tag, false) }
	}
	return a
}

// printLift prints the lift; regression says whether its regression
// verdict lines, and its ape and pin lines, are among them (under
// --from-update they go to stderr: printLiftApe, printLiftRegression).
func printLift(w io.Writer, r *apexv1.LiftResult, regression bool) {
	verb := func(done, plan string) string {
		if r.DryRun {
			return plan
		}
		return done
	}
	if regression || r.DryRun {
		printLiftApe(w, r)
	}
	fmt.Fprintf(w, "APEX v0 %s (%s); skills from %s\n", r.Version, r.VersionSource, r.SkillsSource)
	if regression || r.DryRun {
		printLiftWarnings(w, r)
	}
	for _, p := range r.Preflight {
		fmt.Fprintf(w, "would refuse  %s — %s\n", p.Code, p.Message)
	}
	for _, d := range r.Deleted {
		family := d.Family
		if d.Untracked {
			family += "; untracked, so not archived by " + apexv1.LiftTag
		}
		fmt.Fprintf(w, "%-9s %s  (%s)\n", verb("deleted", "delete"), d.Path, family)
	}
	printLiftStandard(w, r.StandardADR)
	for i := range r.ADRs {
		a := &r.ADRs[i]
		line := fmt.Sprintf("adr       %s -> %s  %s  [%s]", a.Old, a.New, a.Path, a.Status)
		if len(a.Gaps) > 0 {
			line += "  gaps: " + strings.Join(a.Gaps, "; ")
		}
		fmt.Fprintln(w, line)
	}
	if r.Canon != "" {
		fmt.Fprintln(w, "canon     "+r.Canon)
	}
	for _, c := range r.Changed {
		fmt.Fprintf(w, "%-9s %s  (%d comment(s) stripped, %d ADR citation(s) rewritten)\n", verb("edited", "edit"), c.File, c.IDsRemoved, c.ADRsRewritten)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(w, "left      %s — %s\n", s.File, s.Reason)
	}
	for _, f := range r.GoUnformatted {
		fmt.Fprintf(w, "gofmt     %s — not gofmt-clean at %s: edited, not formatted\n", f, apexv1.LiftTag)
	}
	if n := len(r.IDsLeft); n > 0 {
		fmt.Fprintf(w, "ids left  %d in identifiers, strings or directives (listed in %s)\n", n, r.Disposition)
	}
	if n := len(r.UnresolvedCitations); n > 0 {
		fmt.Fprintf(w, "unresolved  %d ADR citation(s) name no ADR (listed in %s)\n", n, r.Disposition)
	}
	for _, h := range r.Hashed {
		fmt.Fprintf(w, "hashed    %s — %s seems to pin its bytes: --exclude it to keep them\n", h.File, strings.Join(h.By, ", "))
	}
	// A dry run lists every one: it writes no report to point at (R6).
	if more := r.HashedTotal - len(r.Hashed); more > 0 && !r.DryRun {
		fmt.Fprintf(w, "hashed    … and %d more (in %s)\n", more, r.Report)
	}
	for _, v := range r.V0StateReaders {
		fmt.Fprintf(w, "v0 state  %s reads %s — re-point it to v1 state or retire it\n", v.File, strings.Join(v.Reads, ", "))
	}
	if n := len(r.DanglingRefs); n > 0 {
		fmt.Fprintf(w, "refs      %d reference(s) to removed paths remain (listed in %s)\n", n, r.Disposition)
	}
	if r.Gates.Source != "" {
		fmt.Fprintf(w, "gates     %d command(s) from %s; regression: %s\n", len(r.Gates.Commands), r.Gates.Source, r.Gates.Regression)
		for _, rw := range r.Gates.Rewrites {
			fmt.Fprintf(w, "gates     written for /bin/sh: %s (ape runs checks with sh, which has no source where it is dash)\n", rw)
		}
	} else {
		fmt.Fprintln(w, "gates     no ## Code Validation found: no commands, no regression")
	}
	fmt.Fprintf(w, "rules     %d binding-rule candidate(s) in %s\n", len(r.Candidates), r.Disposition)
	// W7: a library's module fences, or the suggestion of the key.
	for _, f := range r.Fences {
		fmt.Fprintf(w, "%-9s %s  (module fence: the v0 config says library: true)\n", verb("fenced", "fence"), f)
	}
	if s := r.SuggestLibrary; s != nil {
		fmt.Fprintln(w, "suggest   "+s.Text())
	}
	printLiftGitIgnore(w, r)
	fmt.Fprintf(w, "report    %s: every list whole, ignored by git (the disposition file summarises them)\n", r.Report)
	if r.DryRun {
		fmt.Fprintln(w, "commit message:")
		for l := range strings.SplitSeq(strings.TrimRight(r.CommitMessage, "\n"), "\n") {
			fmt.Fprintln(w, "  | "+l)
		}
		fmt.Fprintln(w, "dry run: nothing written")
		return
	}
	fmt.Fprintf(w, "commit    %s  %s\n", r.Commit, strings.SplitN(r.CommitMessage, "\n", 2)[0])
	printLiftPreCommit(w, r)
	if regression {
		printLiftRegression(w, r)
	}
	fmt.Fprintf(w, "next: apex-plan with %s (the judged v1.0.0 migration entry), then a fresh read-only review of it, then yours\n", r.Disposition)
}

// printLiftRegression is the lift's regression verdict: the runs at the
// tag and at the lift commit, each command of a split chain, and where the
// verdict is recorded (RG-6).
// printLiftWarnings prints what the lift does not refuse but the person
// must act on (RG-2), with the file's absolute path.
func printLiftWarnings(w io.Writer, r *apexv1.LiftResult) {
	for _, x := range r.Warnings {
		fmt.Fprintf(w, "warning   %s (%s): %s\n", x.Code, x.Path, x.Message)
	}
}

// printLiftApe prints which ape runs the lift and where the project's ape
// pin goes (L1, R5).
func printLiftApe(w io.Writer, r *apexv1.LiftResult) {
	if a := r.Ape; a != nil {
		fmt.Fprintf(w, "ape       %s (%s) runs this lift — exempt from the project's ape pin\n", a.Version, a.Path)
		if a.PinFile != "" {
			fmt.Fprintf(w, "pin       %s pins %s: %s\n", a.PinFile, a.Pinned, a.PinNote)
		}
	}
}

func printLiftRegression(w io.Writer, r *apexv1.LiftResult) {
	if t := r.RegressionAtTag; t != nil {
		fmt.Fprintf(w, "regression at %s exit %d (%.1fs)\n", apexv1.LiftTag, t.Exit, t.Secs)
	}
	if r.Regression != nil {
		fmt.Fprintf(w, "regression exit %d [%s] — %s\n", r.Regression.Exit, r.RegressionVerdict, r.RegressionNote)
	} else if r.RegressionNote != "" {
		fmt.Fprintln(w, "regression "+r.RegressionNote)
	}
	for i, c := range r.RegressionCommands {
		at := "not run"
		if c.RegressionAtTag != nil {
			at = fmt.Sprintf("exit %d", c.RegressionAtTag.Exit)
		}
		after := fmt.Sprintf("exit %d", c.Regression.Exit)
		if c.Regression.Exit == 0 && c.Verdict != apexv1.LiftRegressionGreen {
			after = "exit 0 (but the output names failing tests)" // R24: red by the names
		}
		if re := c.RegressionRerun; re != nil {
			after += fmt.Sprintf(", exit %d on a re-run", re.Exit) // RG-1
		}
		fmt.Fprintf(w, "  %d [%s] %s at %s, %s at the lift commit — %s\n", i+1, c.Verdict, at, apexv1.LiftTag, after, c.Cmd)
	}
	switch {
	case r.RegressionCommit != "":
		fmt.Fprintf(w, "regression verdict recorded in %s (## Regression, commit %s %s) and %s (regression)\n", r.Disposition, short(r.RegressionCommit), apexv1.LiftRegressionSubject, r.Report)
	case r.Commit != "":
		fmt.Fprintf(w, "regression not run: recorded in %s (## Regression) and %s (regression)\n", r.Disposition, r.Report)
	}
}

// printLiftGitIgnore says what the lift does to the root .gitignore's
// .claude/ rules (D203): a dry run what its commit will do.
func printLiftGitIgnore(w io.Writer, r *apexv1.LiftResult) {
	gi := r.GitIgnore
	if gi == nil {
		return
	}
	if !r.DryRun {
		for _, l := range apexv1.ClaudeIgnoreLines(gi) {
			fmt.Fprintln(w, l)
		}
		return
	}
	switch gi.Action {
	case apexv1.ClaudeIgnoreAlready:
		fmt.Fprintln(w, ".gitignore: the .claude/ shape is already in place")
	case apexv1.ClaudeIgnoreRewritten:
		fmt.Fprintln(w, ".gitignore: the lift commit rewrites the whole-folder .claude ignore in place to the .claude/ shape (.claude/* !.claude/settings.json !.claude/skills/ CLAUDE.local.md)")
	default:
		fmt.Fprintln(w, ".gitignore: the lift commit adds the .claude/ shape (.claude/* !.claude/settings.json !.claude/skills/ CLAUDE.local.md)")
	}
}

// printLiftPreCommit says what the lift's skeleton did with git's
// pre-commit hook (Q89).
func printLiftPreCommit(w io.Writer, r *apexv1.LiftResult) {
	if r.Init == nil {
		return
	}
	if pc := preCommitLine(r.Init.PreCommit); pc != "" {
		fmt.Fprintln(w, pc)
	}
}

// printLiftStandard is the lift's line for ADR-0001, APEX's standard
// (D205 Q119): what it replaces, if anything, and where its text came from.
func printLiftStandard(w io.Writer, sa *apexv1.StandardADRResult) {
	if sa == nil {
		return
	}
	old := "(none)"
	if sa.Replaces != "" {
		old, _, _ = strings.Cut(sa.Replaces, ",")
	}
	fmt.Fprintf(w, "adr       %s -> %s  %s  [accepted]  APEX's standard ADR (%s)\n", old, apexv1.StandardADRID, sa.Path, adrStandardSource(sa))
}
