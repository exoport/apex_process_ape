package apecmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// rc.7b (rc7b-adr): D205 Q120, D206 Q123, D210.
func newDecisionStandardCmd() *cobra.Command {
	var f v1Flags
	var check bool
	cmd := &cobra.Command{
		Use:   "standard [--check]",
		Short: "Bring ADR-0001 to APEX's installed standard ADR (framework migrations run it)",
		Long: `ADR-0001 of every APEX project is APEX's standard ADR, "Record architecture
decisions" (D205): the decision file, its sections, the proposed → accepted lifecycle,
and change only by superseding. It is framework-owned, like the payload (D206): its
text is one shipped version of the standard — the installed payload's
_apex/standards/adr-0001.md, else ape's own copy of it — recorded in
_apex/state/decisions.json as accepted by "APEX standard (<the ape command>)", with
standard: {framework_version, sha256}, the version it came from and the digest of its
bytes. ape project init and the lift write it. This command brings a project there
that was made before them, and follows a framework release that changes the standard:
the framework's migration v1.0.0_seq-08 runs it, as does the entry of each release
that changes the standard (command: ape decision standard; check: ape decision
standard --check).

In order:
  differs    ADR-0001 is the project's own decision (neither the standard nor
             framework-owned — the apex-v0 governance standard an earlier lift
             converted, its Lifted from: line naming a use-markdown-adrs record, or a
             standard ape wrote), or there are two ADR-0001 files: nothing is done; ape
             never replaces a project's decision (ape project check:
             decision.standard-differs)
  opted-out  an ACCEPTED ADR has Supersedes: ADR-0001: the project opted out of the
             standard; nothing is done, the command says so, and exits 0 (ape project
             check: decision.standard-superseded). A proposed supersede does not opt out
  created    there is no ADR-0001: the standard is written as
             development/decisions/0001-record-architecture-decisions.md
  kept       ADR-0001 is the installed standard already (compared without its
             Status: line): nothing is done
  replaced   ADR-0001 is framework-owned and not the installed standard: its file is
             replaced by the standard (an old file name, 0001-use-markdown-adrs.md, is
             removed) and the decisions.json record re-written. When it was the apex-v0
             governance standard, each PROPOSED ADR that supersedes it is removed too —
             an unaccepted supersede of what is replaced has nothing left to supersede
             (pdfa's ADR-0031) — named, with the lines that still cite it, listed and
             not rewritten. A proposed supersede of a standard ape wrote is a person's
             pending opt-out and is left. No other ADR is renumbered
A write is its own commit, "apex: ADR-0001 standard <framework version>" (or "… ape's
own copy" when no payload is installed). decision.accepted-edited accepts exactly that
change of an accepted ADR-0001 — its bytes equal the digest ape recorded for a shipped
standard — and still refuses a hand edit. When a release changes the standard and no
entry runs this, ape project check lists the notice decision.standard-stale.

Refused on a change branch (branch.change-branch), outside a git repository
(git.not-a-repo), and while a file it writes (ADR-0001's, decisions.json, an ADR it
removes) has uncommitted changes (tree.dirty): nothing is written. A command any
session may run (not person-only): it applies the framework's record, and decides
nothing a person decides.

--check writes nothing: exit 0 when nothing is due (kept, opted-out or differs), 1
(decision.standard-pending, action person, naming what is due) when a write is — the
migration's check: 0 applied, 1 not applied.

JSON: {"action": "created"|"replaced"|"kept"|"opted-out"|"differs", "path", "source":
"_apex/standards/adr-0001.md"|"ape", "framework_version", "replaces" (the file it
replaced), "opted_out_by": [ADR-NNNN, …], "removed_adrs": [{id, path, citations:
["<file>:<line>", …]}], "commit"}. Under --check it says what is due, under "result"
beside the failure's ok/exit/diagnostics (exit 1).

Exit codes: 0 written, or nothing due (--check: nothing due) · 1 refused, or (--check)
a write due.`,
		Example: "  ape decision standard --check\n  ape decision standard\n  ape decision standard --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if check {
				res, err := p.DecisionStandardCheck(cmd.Context())
				if err != nil {
					return v1Fail(cmd, &f, err, res)
				}
				return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
					fmt.Fprintln(w, "ADR-0001: "+adrStandardLine(res)+"; nothing due")
				})
			}
			res, err := p.DecisionStandard(cmd.Context())
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintln(w, "ADR-0001: "+adrStandardLine(res))
				for _, r := range res.Removed {
					cites := "cited nowhere else"
					if len(r.Citations) > 0 {
						cites = "still cited at " + strings.Join(r.Citations, ", ") + " (not rewritten)"
					}
					fmt.Fprintf(w, "removed %s (%s): proposed, it superseded the replaced apex-v0 governance standard; %s\n", r.ID, r.Path, cites)
				}
				if res.Commit != "" {
					fmt.Fprintf(w, "commit %.7s\n", res.Commit)
				}
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&check, "check", false, "write nothing: exit 0 when nothing is due, 1 when a write of the standard is")
	return cmd
}

// adrStandardLine says what happened to ADR-0001, for init and ape
// decision standard.
func adrStandardLine(r *apexv1.StandardADRResult) string {
	from := adrStandardSource(r)
	switch r.Action {
	case apexv1.StandardCreated:
		return r.Path + " written: APEX's standard ADR, Record architecture decisions (" + from + ")"
	case apexv1.StandardReplaced:
		return r.Replaces + " replaced by APEX's standard ADR, " + r.Path + " (" + from + ")"
	case apexv1.StandardKept:
		return r.Path + " is APEX's standard ADR already (" + from + ")"
	case apexv1.StandardOptedOut:
		return r.Path + " left alone: the project opted out of the standard (" + strings.Join(r.OptedOutBy, ", ") + " accepted, superseding it)"
	}
	return r.Path + " left alone: it is the project's own decision, not APEX's standard (ape project check: decision.standard-differs)"
}

// adrStandardSource names where the standard's text came from.
func adrStandardSource(r *apexv1.StandardADRResult) string {
	if r.Source == apexv1.StandardSourceApe {
		return "ape's own copy"
	}
	if r.FrameworkVersion != "" {
		return r.Source + ", framework " + r.FrameworkVersion
	}
	return r.Source
}
