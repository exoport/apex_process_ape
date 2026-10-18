package apecmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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
whose subject starts "apex: lift" — the v1 epoch. The judgment half (which rule lives
where, what M1 delivers) is apex-plan's, from the disposition file this writes.

Before: a person tags the commit to lift and pushes the tag. The tag is the archive of
everything the lift deletes; v0 files stay readable as ` + "`git show apex-v0-final:<path>`" + `.

  git tag -a apex-v0-final -m "APEX v0 final" && git push origin apex-v0-final
  ape project lift --dry-run        # the whole plan, nothing written
  ape project lift

Which ape: the lift runs on the binary invoked, whatever the project's .bingo/ape.mod
pins — it is exempt from the pin swap, since a v0 pin names an ape with no v1 commands.
The output's first lines say which binary runs (version, path) and what becomes of the
pin: when this ape is a release (vX.Y.Z or vX.Y.Z-rc.N) and bingo is on PATH, the lift
commit moves the pin to it (bingo get); otherwise the lift prints the one command that
does, and until the pin moves every other v1 command there needs APE_TOOLCHAIN=local.

Pre-flight (refused, exit 1): a v0 install (_apex/framework.yaml records a v0 version,
or did in git history) and no v1 layout or earlier lift (lift.not-v0); a version in
v0.15.0–v0.31.x and a resolvable _apex/config.yaml (lift.unsupported-layout); a clean
tree (lift.dirty); apex-v0-final exists (lift.no-tag), is HEAD (lift.tag-not-head),
and is on the current branch's upstream remote at the same commit, checked with
git ls-remote — the one network call (lift.tag-not-pushed; lift.remote-unreachable);
and git ignores nothing the lift commits (lift.claude-ignored) — above all
.claude/settings.json, where the v1 hooks go: un-ignore it with .claude/* and
!.claude/settings.json, commit, and re-tag. --dry-run lists every problem, not the first.

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
  converts       every ADR (accepted, superseded, proposed) into
                 development/decisions/NNNN-<slug>.md, renumbered from 0001 in the
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
  strips         epic, story and deferred ids from code comments — Story 3.2,
                 Epic 12, (3-2) (a known story key), DW-…, paths into the deferred
                 store or the implementation folder — keeping the rest of the comment;
                 a comment that was only an id goes. A story takes its fragments with
                 it (Story 3.2, Task 2 · Subtask 3.1 of Story 5.10 · AC 2), and the
                 separator a removal leaves by a bracket or a mark goes too
                 ("(ADR-0019, Story 3.2)" → "(ADR-0019)"). Comments only: ids in
                 identifiers, strings and directives are listed, not edited — and
                 so are ids in a data-shaped comment, which a tool may parse: one
                 opening with a lowercase key ("# owner: …") or a record of
                 key: value fields ("# pending: q | owner: …"), and so is a comment
                 using an id as a possessive ("Story 3.2's own test"), which no
                 removal keeps whole. An aside split over comment lines loses
                 both brackets with its id: no line is left starting with ")".
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
                 placeholder. A trailing " # …" shell comment is dropped first
  lays down      the v1 skeleton, as ` + "`ape project init --brownfield`" + ` (CLAUDE.md keeps its
                 content, loses the v0 apex:managed block and gains @AGENTS.md)
  writes         development/lift-disposition.md: the binding-rule candidates
                 (CLAUDE.md list items, project-context "don't miss" items, team-memory
                 gotcha titles) with an empty Disposition column for apex-plan, then a
                 fresh read-only review, then the person, who deletes it. Then, as
                 summaries: code and CI that still read removed paths (CI guards over
                 the v0 corpus show up there) and edited files a guard hashes first;
                 ADR gaps; ids left and files not edited, counted per file;
                 references in evidence and docs, counted; readings at the tag.
                 development/lift-disposition.json, beside it, holds every list whole

A byte-identity perimeter: a file a test, CI script or lock file hashes goes red once
the lift edits a comment in it. The plan lists, as "hashed", the edited files that a lock
file names, that a test or CI file names within 3 lines of a hash word (sha256,
checksum, fingerprint, byte-identical, …), or that sit beside a hashing test that globs
or names them — run --dry-run, then --exclude them. Documents and evidence (.md, .json,
.log, .txt, anything under an evidence folder) never count as hashers. What a lock file
names is listed first; at most 50 are listed, edited_but_hashed_total counts them all.
It is a heuristic: a copy compared byte for byte with no hash (an embedded mirror of a
hashed file) needs its own --exclude.

The regression command runs twice (--no-verify-run skips both): at the tag, before
anything is written, and at the lift commit — the baseline red gates are judged against.
regression_verdict says whose a red is: green · red-at-tag (red there too: pre-existing,
for apex-plan to roadmap) · red-after-lift (green at the tag: the LIFT broke it — fix it
in a commit, or reset to the tag and lift with --exclude) · cannot-run (exit 126/127: a
tool it runs is missing here, which is not a red) · red-no-baseline. The exit code stays
0: the commit stands either way, and the verdict is in --json and the output.

--from-update is how the framework's v1.0.0 migration entry runs it inside
` + "`ape framework update`" + `: uncommitted changes under .claude/skills/, _apex/ (not
config.yaml or state/), .aboard/ and .gitignore are the install's, left out of the lift
commit for the update's own; skills the v1 install placed are never deleted.

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
				_, err := exec.LookPath("bingo")
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
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) { printLift(w, res) })
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
			"; once this version is published, run " + a.PinCommand + " and commit .bingo/ — until then run v1 commands here with " + envToolchain + "=local"
	case !haveBingo():
		a.PinNote = "bingo is not on PATH: after the lift, run " + a.PinCommand + " and commit .bingo/ — until then run v1 commands here with " + envToolchain + "=local"
	case dryRun:
		a.PinNote = "the lift moves the pin from " + pin.version + " to " + tag + " (" + a.PinCommand + ") in its commit"
	default:
		a.MovePin = func(ctx context.Context) error { return movePin(ctx, io.Discard, root, tag, false) }
	}
	return a
}

func printLift(w io.Writer, r *apexv1.LiftResult) {
	verb := func(done, plan string) string {
		if r.DryRun {
			return plan
		}
		return done
	}
	if a := r.Ape; a != nil {
		fmt.Fprintf(w, "ape       %s (%s) runs this lift — exempt from the project's ape pin\n", a.Version, a.Path)
		if a.PinFile != "" {
			fmt.Fprintf(w, "pin       %s pins %s: %s\n", a.PinFile, a.Pinned, a.PinNote)
		}
	}
	fmt.Fprintf(w, "APEX v0 %s (%s); skills from %s\n", r.Version, r.VersionSource, r.SkillsSource)
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
	if more := r.HashedTotal - len(r.Hashed); more > 0 {
		fmt.Fprintf(w, "hashed    … and %d more (in %s)\n", more, r.Report)
	}
	if n := len(r.DanglingRefs); n > 0 {
		fmt.Fprintf(w, "refs      %d reference(s) to removed paths remain (listed in %s)\n", n, r.Disposition)
	}
	if r.Gates.Source != "" {
		fmt.Fprintf(w, "gates     %d command(s) from %s; regression: %s\n", len(r.Gates.Commands), r.Gates.Source, r.Gates.Regression)
	} else {
		fmt.Fprintln(w, "gates     no ## Code Validation found: no commands, no regression")
	}
	fmt.Fprintf(w, "rules     %d binding-rule candidate(s) in %s\n", len(r.Candidates), r.Disposition)
	fmt.Fprintf(w, "report    %s: every list whole (the disposition file summarises them)\n", r.Report)
	if r.DryRun {
		fmt.Fprintln(w, "commit message:")
		for l := range strings.SplitSeq(strings.TrimRight(r.CommitMessage, "\n"), "\n") {
			fmt.Fprintln(w, "  | "+l)
		}
		fmt.Fprintln(w, "dry run: nothing written")
		return
	}
	fmt.Fprintf(w, "commit    %s  %s\n", r.Commit, strings.SplitN(r.CommitMessage, "\n", 2)[0])
	if t := r.RegressionAtTag; t != nil {
		fmt.Fprintf(w, "regression at %s exit %d (%.1fs)\n", apexv1.LiftTag, t.Exit, t.Secs)
	}
	if r.Regression != nil {
		fmt.Fprintf(w, "regression exit %d [%s] — %s\n", r.Regression.Exit, r.RegressionVerdict, r.RegressionNote)
	} else if r.RegressionNote != "" {
		fmt.Fprintln(w, "regression "+r.RegressionNote)
	}
	fmt.Fprintf(w, "next: apex-plan with %s (the judged v1.0.0 migration entry), then a fresh read-only review of it, then yours\n", r.Disposition)
}
