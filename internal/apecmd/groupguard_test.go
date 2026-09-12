package apecmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// groupsOf returns every command in the tree that ape OWNS and that has
// subcommands, keyed by its full command path.
//
// Three exclusions, each a decision rather than an oversight:
//
//   - the root, because cobra already errors there — `ape zzunknown` has
//     always exited 1, which is why this defect only ever lived one level
//     down;
//   - cobra's generated `completion` and `help`, which cobra adds to the
//     root itself;
//   - everything BELOW `aboard`. The node ape mounts is guarded; its
//     subtree is another module's contract. `ape aboard recipes zzunknown`
//     therefore still exits 0 with help, and that is deliberate: the
//     standalone `aboard` binary serves the same tree, and a host that
//     answered differently would be a host an agent could detect — the
//     thing aboard.go's identity rules exist to prevent.
func groupsOf(t *testing.T, root *cobra.Command) map[string]*cobra.Command {
	t.Helper()
	out := map[string]*cobra.Command{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			switch sub.Name() {
			case "completion", "help":
				continue
			}
			if sub.HasSubCommands() {
				out[sub.CommandPath()] = sub
			}
			if sub.Name() == "aboard" && sub.Parent() == root {
				continue // guarded at the node, not into the subtree
			}
			walk(sub)
		}
	}
	walk(root)
	return out
}

// TestGroupGuard_EveryGroupRejectsAnUnknownVerb is the structural
// assertion: a group added later cannot arrive without the guard,
// because this test walks the tree rather than a list someone maintains.
//
// The defect it pins: cobra answers an unknown sub-verb of a group with
// the parent's help on stdout and exit 0, so a framework release ahead of
// this binary would have a skill parse help text as its payload and read
// success from the exit code.
func TestGroupGuard_EveryGroupRejectsAnUnknownVerb(t *testing.T) {
	groups := groupsOf(t, newRootCmd())
	require.NotEmpty(t, groups, "the tree has groups to guard")

	for path, cmd := range groups {
		t.Run(path, func(t *testing.T) {
			require.NotNil(t, cmd.Args, "%s has no Args validator", path)

			err := cmd.Args(cmd, []string{"zzunknown"})
			require.Error(t, err, "%s accepted an unknown verb", path)
			require.Contains(t, err.Error(), `unknown command "zzunknown"`)
			require.Contains(t, err.Error(), path,
				"the message names the group the reader typed")

			code, silent := ExitCode(err)
			require.Equal(t, ExitUsage, code, "an unknown verb is a usage error")
			require.True(t, silent, "the guard prints its own diagnostic")
		})
	}
}

// TestGroupGuard_EveryGuardedGroupIsRunnable pins the measured cobra
// behaviour that makes the guard work at all.
//
// cobra's execute() returns flag.ErrHelp for a command that is not
// Runnable BEFORE it calls ValidateArgs. So on a group with no Run — which
// is eighteen of ape's twenty — an Args validator is never consulted and
// the guard is dead code that passes its own unit test. Every group must
// therefore be Runnable, which guardGroup arranges with a Help closure.
//
// Without this test, someone simplifying the "redundant" RunE away would
// silently restore the original defect and see a green suite.
func TestGroupGuard_EveryGuardedGroupIsRunnable(t *testing.T) {
	for path, cmd := range groupsOf(t, newRootCmd()) {
		require.True(t, cmd.Runnable(),
			"%s is not Runnable, so cobra never reaches its Args validator "+
				"and the unknown-verb guard does nothing", path)
	}
}

// TestGroupGuard_BareGroupIsAccepted — the guard changes what an unknown
// VERB does, never what a bare invocation does. `ape costs` and
// `ape sessions` carry their own Run and must keep it.
func TestGroupGuard_BareGroupIsAccepted(t *testing.T) {
	for path, cmd := range groupsOf(t, newRootCmd()) {
		require.NoError(t, cmd.Args(cmd, nil),
			"%s rejected a bare invocation", path)
	}
}

// TestGroupGuard_GroupsWithTheirOwnRunKeepIt is the specific half of the
// above that would be easy to break: guardGroup installs its Help closure
// only when the group has no behaviour of its own.
func TestGroupGuard_GroupsWithTheirOwnRunKeepIt(t *testing.T) {
	unguarded := newCostsCmd()
	require.True(t, unguarded.Runnable(), "precondition: costs has its own Run")
	before := unguarded.RunE == nil // costs uses Run, not RunE

	guarded := groupsOf(t, newRootCmd())["ape costs"]
	require.NotNil(t, guarded)
	require.Equal(t, before, guarded.RunE == nil,
		"the guard replaced the group's own behaviour with help")
}

// TestGroupGuard_LeavesAreNotGuarded — a command with no subcommands owns
// its positional arguments (`ape task <skill>`, `ape script <file>`), so a
// blanket NoArgs across the surface would break them.
func TestGroupGuard_LeavesAreNotGuarded(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"task"})
	require.NoError(t, err)
	require.False(t, cmd.HasSubCommands(), "precondition: task is a leaf")

	if cmd.Args != nil {
		require.NoError(t, cmd.Args(cmd, []string{"apex-dev-story"}),
			"the guard must not reject a leaf's own positional argument")
	}
}

// TestGroupGuard_AboardSubtreeIsNotGuarded pins the boundary.
//
// The aboard tree comes from a separate module with its own exit table and
// its own argument contracts. ape guards the node it mounts and stops
// there; reaching into the subtree would be ape legislating for a
// dependency, and a change in aboard's own arg handling would then fail
// here for a reason that has nothing to do with ape.
func TestGroupGuard_AboardSubtreeIsNotGuarded(t *testing.T) {
	root := newRootCmd()
	aboardCmd, _, err := root.Find([]string{"aboard"})
	require.NoError(t, err)
	require.Equal(t, "aboard", aboardCmd.Name())

	require.Error(t, aboardCmd.Args(aboardCmd, []string{"zzunknown"}),
		"the mounted node itself is guarded")

	var checked int
	for _, sub := range aboardCmd.Commands() {
		if !sub.HasSubCommands() {
			continue
		}
		checked++
		if sub.Args == nil {
			continue
		}
		err := sub.Args(sub, []string{"zzunknown"})
		if err != nil {
			require.NotContains(t, err.Error(), "this ape binary does not provide it",
				"%s carries ape's guard; the subtree is aboard's contract", sub.CommandPath())
		}
	}
	t.Logf("aboard sub-groups checked: %d", checked)
}
