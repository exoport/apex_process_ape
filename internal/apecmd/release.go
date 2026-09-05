package apecmd

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/release"
	"github.com/spf13/cobra"
)

func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Project the framework's release record",
		Long: `A release slice is an operator-declared set of epics, living in
sprint-status.yaml's release_slices: and active_slice: keys. A release
record is one file per release under {planning_folder}/releases/, whose
frontmatter asserts that release's status.

  status  project the records and the slices they decide`,
	}
	cmd.AddCommand(newReleaseStatusCmd())
	return cmd
}

func newReleaseStatusCmd() *cobra.Command {
	var (
		cwdFlag      string
		outputFormat string
		sliceFlag    string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the declared slices and the state each record asserts",
		Long: `Read sprint-status.yaml's release_slices: and active_slice: keys and,
for each slice, the frontmatter of {planning_folder}/releases/release-<id>.md.

Two things this reads and never computes.

THE STATUS IS THE RECORD'S. A release's status is asserted in exactly one
place — the record's own 'status:' field — so a slice is released when its
record says so and by no other route. A release_slices: entry deliberately
carries no status of its own, which is what stops a shipped release being
counted as unshipped by a writer that only ever wrote 'declared'. An
absent releases/ folder, an absent record and an UNREADABLE one all mean
"not released"; the unreadable one is reported as unreadable rather than
given a status it never asserted.

ONLY THE FRONTMATTER. The record's body carries nine assembled tables,
including the gate table with its per-row PASS / RED / NOT-RUN / PENDING
results. Those belong to apex-release-record, and re-deriving them from a
Markdown table here would put a second source of truth behind the
release's own verdict. What is reported about gates is the declaration —
how many, how many required — never a result. The verdict is in the
frontmatter already: 'status:' asserts it, 'blocking:' says why it is not
'prepared', 'acceptance:' says why a run that reached no verdict reached
one.

The active scope resolves the way the framework's own prose does:
active_slice names a declared slice, or — absent, empty, or naming a slice
release_slices: does not carry — the scope is every epic not inside a
released slice. Epics are enumerated from tracker rows, so with NO tracker
the epic sets come back null rather than empty: "no tracker to enumerate
from" and "no epics" are different answers, and only one of them is true.

EXIT 0 ALWAYS. The record's 'status:' is the verdict, not the exit code —
a projection that halted on one bad record could not report the others.`,
		Args: cobra.NoArgs,
		Example: "  ape release status\n" +
			"  ape release status --output-format json\n" +
			"  ape release status --slice v1.2.0",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := resolveProjectConfig(cwdFlag)
			if cfg.Paths.SprintStatus == "" {
				return usageErr(fmt.Errorf("%s, so there is no tracker to read release slices from",
					"implementation_folder is not configured"))
			}
			st, err := release.Project(cfg.Paths.SprintStatus, cfg.Paths.Planning)
			if err != nil {
				return err
			}
			if sliceFlag != "" {
				if err := narrowToSlice(st, sliceFlag); err != nil {
					return gateErr(ExitUsage, err)
				}
			}
			format := output.Format(outputFormat)
			if format != output.FormatHuman {
				return output.Print(cmd.OutOrStdout(), format, st)
			}
			emitReleaseStatusHuman(cmd.OutOrStdout(), st)
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	cmd.Flags().StringVar(&sliceFlag, "slice", "", "Report one slice by id instead of every declared slice")
	return cmd
}

// narrowToSlice keeps one slice and drops the rest. The derived epic sets
// are left exactly as the full projection computed them: they are
// statements about the whole tracker, and recomputing them from one slice
// would make --slice change the answer rather than the view.
func narrowToSlice(st *release.Status, id string) error {
	for i := range st.Slices {
		if st.Slices[i].ID == id {
			st.Slices = st.Slices[i : i+1]
			return nil
		}
	}
	declared := make([]string, 0, len(st.Slices))
	for i := range st.Slices {
		declared = append(declared, st.Slices[i].ID)
	}
	if len(declared) == 0 {
		return fmt.Errorf("no slice %q — the tracker declares none", id)
	}
	return fmt.Errorf("no slice %q — the tracker declares %s", id, strings.Join(declared, ", "))
}

func emitReleaseStatusHuman(w io.Writer, st *release.Status) {
	if !st.TrackerPresent {
		fmt.Fprintf(w, "no tracker at %s\n", st.TrackerPath)
		fmt.Fprintln(w,
			"epics are enumerated from tracker rows, so no epic set is reported —\n"+
				"the framework's own resolution discovers them from the epic shards.")
		return
	}
	if len(st.Slices) == 0 {
		fmt.Fprintln(w, "no release slice declared")
		fmt.Fprintf(w, "active scope: every epic (%s)\n", epicList(st.ActiveEpics))
		return
	}

	fmt.Fprintf(w, "active_slice: %s\n", activeLine(st))
	fmt.Fprintf(w, "active scope: %s\n", epicList(st.ActiveEpics))
	fmt.Fprintf(w, "unreleased epics: %s\n\n", epicList(st.UnreleasedEpics))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SLICE\tEPICS\tRECORD\tSTATUS\tGATES\tBLOCKING")
	for i := range st.Slices {
		sl := &st.Slices[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
			sliceLabel(sl), epicList(sl.Epics), recordState(&sl.Record),
			recordStatus(&sl.Record), gateSummary(&sl.Record), len(sl.Record.Blocking))
	}
	if err := tw.Flush(); err != nil {
		return
	}

	for i := range st.Slices {
		emitSliceDetail(w, &st.Slices[i])
	}
}

// emitSliceDetail prints only what a table cell cannot carry: the
// blocking list verbatim, a parse failure, and the tag fields.
func emitSliceDetail(w io.Writer, sl *release.Slice) {
	rec := &sl.Record
	if sl.Malformed == "" && rec.Unreadable == "" && len(rec.Blocking) == 0 &&
		rec.TagAuthorization == "" && rec.TaggerFromObject == "" {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", sl.ID)
	if sl.Malformed != "" {
		fmt.Fprintf(w, "  tracker entry: %s\n", sl.Malformed)
	}
	if rec.Unreadable != "" {
		fmt.Fprintf(w, "  record unreadable: %s\n", rec.Unreadable)
		fmt.Fprintln(w, "  counted as NOT released — a record that cannot be read asserts nothing")
	}
	for _, b := range rec.Blocking {
		fmt.Fprintf(w, "  blocking: %s\n", b)
	}
	if rec.TagAuthorization != "" {
		fmt.Fprintf(w, "  tag authorization: %s\n", rec.TagAuthorization)
	}
	if rec.TaggerFromObject != "" {
		// Labelled at every point it surfaces: it is reconstructed from a
		// git tag object on a --backfill-legacy record, so reading it as
		// authorization would let the record assert something no human
		// said.
		fmt.Fprintf(w, "  tagger (provenance from the tag object, NOT authorization): %s\n",
			rec.TaggerFromObject)
	}
}

func activeLine(st *release.Status) string {
	switch st.ActiveResolution {
	case release.ActiveDeclared:
		return st.ActiveSlice
	case release.ActiveDangling:
		return fmt.Sprintf("%q — declared but absent from release_slices; scope falls back to unreleased epics",
			st.ActiveSlice)
	default:
		return "(none declared — scope is every unreleased epic)"
	}
}

func sliceLabel(sl *release.Slice) string {
	if sl.Active {
		return sl.ID + " *"
	}
	return sl.ID
}

func recordState(rec *release.Record) string {
	switch {
	case !rec.Present:
		return "absent"
	case rec.Unreadable != "":
		return "unreadable"
	default:
		return "present"
	}
}

// recordStatus never invents one. A record that is absent or unreadable
// asserts no status, and a dash says exactly that.
func recordStatus(rec *release.Record) string {
	if !rec.Present || rec.Unreadable != "" || rec.Status == "" {
		return "—"
	}
	return rec.Status
}

// gateSummary reports the DECLARATION: how many gates, how many required.
// Never a result — those live in the record's body table.
func gateSummary(rec *release.Record) string {
	if !rec.Present || rec.Unreadable != "" {
		return "—"
	}
	return fmt.Sprintf("%d declared, %d required", len(rec.Gates), rec.GatesRequired())
}

// epicList renders an epic set. A nil set is not an empty one: it means
// there was nothing to enumerate from.
func epicList(epics []int) string {
	if epics == nil {
		return "(not enumerable — no tracker)"
	}
	if len(epics) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(epics))
	for _, e := range epics {
		parts = append(parts, strconv.Itoa(e))
	}
	return strings.Join(parts, ",")
}
