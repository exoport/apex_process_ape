package apecmd

import (
	"fmt"

	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/spf13/cobra"
)

// newSyncCmd is the retired verb-first spelling. Every other command in
// the CLI is noun-first (`ape adr list`, `ape framework update`,
// `ape sandbox forward`); `ape sync adrs` was the single inversion, so
// PLAN-25 D0 moved it to `ape adr sync` and left this here — hidden — as
// a pointer for one release. Then it goes.
//
// The stubs it used to hold printed "not yet implemented"; the delegates
// below run the real reconcile.
func newSyncCmd() *cobra.Command {
	var check bool

	cmd := &cobra.Command{
		Use:    "sync",
		Short:  "Deprecated: use `ape <family> sync`",
		Long:   "Deprecated. `ape sync adrs` is now `ape adr sync`, and `ape sync patterns` is `ape pattern sync`.",
		Hidden: true,
	}

	cmd.PersistentFlags().BoolVar(&check, "check", false, "Report the diff without writing")

	cmd.AddCommand(
		newLegacySyncDelegate("adrs", &check),
		newLegacySyncDelegate("patterns", &check),
	)

	return cmd
}

// newLegacySyncDelegate forwards `ape sync <family>` to the family's own
// sync, so the deprecation is a redirect rather than a second
// implementation that could drift.
func newLegacySyncDelegate(familyName string, check *bool) *cobra.Command {
	family, err := registry.FamilyByName(familyName)
	if err != nil {
		panic(err)
	}
	var (
		cwdFlag      string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:    familyName,
		Short:  "Deprecated: use `ape " + family.Singular + " sync`",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The warning goes through the command's error writer, not
			// os.Stderr: a deprecation nobody can capture is a deprecation
			// nobody can test.
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: `ape sync %s` is deprecated — use `ape %s sync`\n",
				familyName, family.Singular)
			return runRegistrySync(cmd.OutOrStdout(), cwdFlag, outputFormat, *check, []string{familyName})
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", helpCwd)
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", helpFormat)
	return cmd
}
