package apecmd

import (
	"fmt"
	"io"
	"os"
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

Pre-flight (refused, exit 1): a v0 install (_apex/framework.yaml records a v0 version,
or did in git history) and no v1 layout or earlier lift (lift.not-v0); a version in
v0.15.0–v0.31.x and a resolvable _apex/config.yaml (lift.unsupported-layout); a clean
tree (lift.dirty); apex-v0-final exists (lift.no-tag), is HEAD (lift.tag-not-head),
and is on the current branch's upstream remote at the same commit, checked with
git ls-remote — the one network call (lift.tag-not-pushed; lift.remote-unreachable).

The commit:
  deletes        the v0 families, at the paths _apex/config.yaml's folder variables
                 resolve to: the skills the v0 install manifest lists (only those —
                 never a project's own), the v0 _apex tables and pipelines, stories,
                 the sprint tracker, retrospectives, planning (PRD, architecture,
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
                 Markdown and in code comments to the new number
  strips         epic, story and deferred ids from code comments — Story 3.2,
                 Epic 12, (3-2) (a known story key), DW-…, paths into the deferred
                 store or the implementation folder — keeping the rest of the comment;
                 a comment that was only an id goes. Comments only: ids in
                 identifiers, strings and directives are listed, not edited.
                 Tokenized: Go (go/scanner), shell, Python, JS/TS (JSX/TSX: comments
                 that begin their line only), Dart, YAML, Makefile (recipe lines are
                 the shell's: left), C/C++. A file the tokenizer cannot read, a
                 generated file, or an --exclude match is left and listed. Go: the
                 token stream without comments is checked unchanged; other languages:
                 the text outside comments, re-tokenized, is checked unchanged (blank
                 lines and trailing whitespace aside) — a file failing its check is
                 left unedited and listed
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
                 fresh read-only review, then the person — who deletes the file — and
                 the ids left, ADR gaps, files not edited, references to removed paths
                 (CI guards over the v0 corpus show up there), readings at the tag

After the commit the regression command runs once (--no-verify-run skips it): its result
is the baseline; red is recorded, not a lift failure.

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
	cmd.Flags().BoolVar(&opt.NoVerifyRun, "no-verify-run", false, "do not run the regression command after the commit")
	cmd.Flags().StringArrayVar(&opt.Exclude, "exclude", nil, "a glob (** allowed) of files the lift must not edit, e.g. a byte-identity perimeter; repeatable")
	return cmd
}

func printLift(w io.Writer, r *apexv1.LiftResult) {
	verb := func(done, plan string) string {
		if r.DryRun {
			return plan
		}
		return done
	}
	fmt.Fprintf(w, "APEX v0 %s (%s); skills from %s\n", r.Version, r.VersionSource, r.SkillsSource)
	for _, p := range r.Preflight {
		fmt.Fprintf(w, "would refuse  %s — %s\n", p.Code, p.Message)
	}
	for _, d := range r.Deleted {
		fmt.Fprintf(w, "%-9s %s  (%s)\n", verb("deleted", "delete"), d.Path, d.Family)
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
	if n := len(r.IDsLeft); n > 0 {
		fmt.Fprintf(w, "ids left  %d in identifiers, strings or directives (listed in %s)\n", n, r.Disposition)
	}
	if n := len(r.UnresolvedCitations); n > 0 {
		fmt.Fprintf(w, "unresolved  %d ADR citation(s) name no ADR (listed in %s)\n", n, r.Disposition)
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
	if r.DryRun {
		fmt.Fprintln(w, "commit message:")
		for l := range strings.SplitSeq(strings.TrimRight(r.CommitMessage, "\n"), "\n") {
			fmt.Fprintln(w, "  | "+l)
		}
		fmt.Fprintln(w, "dry run: nothing written")
		return
	}
	fmt.Fprintf(w, "commit    %s  %s\n", r.Commit, strings.SplitN(r.CommitMessage, "\n", 2)[0])
	if r.Regression != nil {
		fmt.Fprintf(w, "regression exit %d — %s\n", r.Regression.Exit, r.RegressionNote)
	} else if r.RegressionNote != "" {
		fmt.Fprintln(w, "regression "+r.RegressionNote)
	}
	fmt.Fprintf(w, "next: apex-plan with %s (the judged v1.0.0 migration entry), then a fresh read-only review of it, then yours\n", r.Disposition)
}
