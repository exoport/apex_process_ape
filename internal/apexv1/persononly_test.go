package apexv1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// personOnlyCall is an invocation of v, with M1 as its positional argument
// where it takes one.
func personOnlyCall(v PersonOnlyVerb) string {
	words := append([]string{}, v.Path...)
	switch strings.Join(v.Path, " ") {
	case "task":
		words = append(words, "apex-loop", "--args", "M1")
	case "orders accept", "orders revoke":
		words = append(words, "--by", "ann")
	default:
		words = append(words, "M1")
	}
	if v.Flag != "" {
		words = append(words, v.Flag)
		if v.Flag == "--from" {
			words = append(words, "draft.md")
		}
	}
	return strings.Join(words, " ")
}

func preToolBash(t *testing.T, command string) PreToolDecision {
	t.Helper()
	in, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	return PreTool(HookPayload{ToolName: "Bash", ToolInput: in, Cwd: t.TempDir()})
}

// FINDING-V15-1C-PRE-1 (D762, research's P1): RequirePersonFlag reads the
// command's own environment, so a model session's `env -u APE_SESSION -u
// CLAUDECODE ape milestone resume M` passed it. The pre-tool hook runs in
// the model session whatever the command's environment says, and now
// denies the Bash call of every PersonOnly verb: the T1 iterates the list,
// each in the plain form, the env -u form, by a binary's path and inside
// sh -c. The controls: read-only and ordinary verbs are not denied, and a
// person at a terminal still passes RequirePersonFlag.
func TestPreTool_DeniesEveryPersonOnlyVerb(t *testing.T) {
	require.NotEmpty(t, PersonOnly)
	for _, v := range PersonOnly {
		call := personOnlyCall(v)
		for _, form := range []string{
			"ape " + call,
			"env -u APE_SESSION -u CLAUDECODE ape " + call,
			"cd /tmp && env -u CLAUDECODE ~/.local/bin/ape-v15-1b " + call,
			"sh -c 'APE_SESSION= ape " + call + "'",
		} {
			d := preToolBash(t, form)
			require.True(t, d.Deny, "%s: %q must be denied", v.Label, form)
			require.Contains(t, d.Reason, v.Label, "%q names the verb", form)
		}
	}
	require.True(t, preToolBash(t, "env -u CLAUDECODE ape conductor attach --project . --pane %3").Deny, "V8i: attach is listed")
	for _, ok := range []string{
		"ape status --json",
		"ape milestone gate M1 --wait 9m --json",
		"ape task apex-loop --args M1",
		"ape orders show ann",
		"echo ape milestone resume M1 > notes.txt",
		// V8i: the conductor model acks and re-pins; only attach is a person's.
		"ape conductor ack 'ape.ack.x.1.2.3'",
		"ape conductor watcher --repin",
	} {
		require.False(t, preToolBash(t, ok).Deny, "control: %q is not a person-only call", ok)
	}
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	for _, v := range PersonOnly {
		require.NoError(t, RequirePersonFlag(v), "control: a person at a terminal runs %s", v.Label)
	}
}
