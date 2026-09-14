package apecmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// The connection flags used to be bound to package variables, which every
// newSandboxCmd call re-bound: two trees shared one value, so a flag parsed into
// one tree was what the other tree read, and two trees built at once failed
// -race. The second tree is the control — it is built BEFORE the first one
// parses, which is exactly the order a shared variable gets wrong.
//
// Every runnable verb under the group is walked rather than one or a list,
// because each of them dials aped through these flags — including verbs under
// a nested group, and any added later.
func TestSandboxFlags_ReachEveryVerbAndStayInTheirTree(t *testing.T) {
	t.Parallel()

	parsed, _, err := newRootCmd().Find([]string{"sandbox"})
	require.NoError(t, err)
	untouched, _, err := newRootCmd().Find([]string{"sandbox"})
	require.NoError(t, err)

	want := map[string]string{
		sandboxFlagNode:      "node-a",
		sandboxFlagNatsURL:   "nats://node-a:4222",
		sandboxFlagNatsCreds: "/etc/aped/operator.creds",
	}
	args := make([]string, 0, 2*len(want))
	for name, value := range want {
		args = append(args, "--"+name, value)
	}

	verbs := runnableVerbs(parsed)
	require.NotEmpty(t, verbs, "fixture: the sandbox group must have runnable verbs")
	for path, verb := range verbs {
		require.NoError(t, verb.ParseFlags(args), path)
		for name, value := range want {
			require.Equal(t, value, sandboxFlag(verb, name), "%s --%s", path, name)
		}
	}

	for path, verb := range runnableVerbs(untouched) {
		for name := range want {
			require.Empty(t, sandboxFlag(verb, name),
				"%s --%s: a value parsed into another tree must not reach this one", path, name)
		}
	}
}

// A command outside the group has none of the flags, and reads them as unset
// rather than panicking.
func TestSandboxFlag_OutsideTheGroupIsUnset(t *testing.T) {
	t.Parallel()

	version, _, err := newRootCmd().Find([]string{"version"})
	require.NoError(t, err)
	require.Empty(t, sandboxFlag(version, sandboxFlagNode))
}

func runnableVerbs(group *cobra.Command) map[string]*cobra.Command {
	out := map[string]*cobra.Command{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Runnable() {
				out[sub.CommandPath()] = sub
			}
			walk(sub)
		}
	}
	walk(group)
	return out
}
