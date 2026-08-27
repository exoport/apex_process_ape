package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// project builds a directory isProjectRoot accepts, carrying the given
// manifest body (empty = no manifest at all).
func commandSurfaceProject(t *testing.T, manifest string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	if manifest != "" {
		require.NoError(t, os.WriteFile(
			filepath.Join(root, framework.ProjectApeCommands), []byte(manifest), 0o600,
		))
	}
	return root
}

func runCommandSurface(t *testing.T, root string) CheckResult {
	t.Helper()
	return checkCommandSurface(context.Background(), doctorEnv{ProjectRoot: root})
}

// A framework older than v0.11.0 ships no manifest. That is version skew,
// not a fault, and must not fail a run.
func TestCommandSurface_AbsentManifestIsASkip(t *testing.T) {
	t.Parallel()
	res := runCommandSurface(t, commandSurfaceProject(t, ""))
	require.Equal(t, StatusSkip, res.Status)
	require.Contains(t, res.Message, "predates the command-surface contract")
}

func TestCommandSurface_AllProvided(t *testing.T) {
	t.Parallel()
	res := runCommandSurface(t, commandSurfaceProject(t, `
required_commands:
  - ape story fields
  - ape doctor
  - ape framework update
`))
	require.Equal(t, StatusOK, res.Status)
	require.Contains(t, res.Message, "3 required command(s) provided")
}

// The whole difference, never the first miss: an operator on an old binary
// wants one line naming everything, not a bisect.
func TestCommandSurface_ReportsEveryMissingCommand(t *testing.T) {
	t.Parallel()
	res := runCommandSurface(t, commandSurfaceProject(t, `
required_commands:
  - ape story fields
  - ape totally bogus
  - ape doctor
  - ape another missing
`))
	require.Equal(t, StatusFail, res.Status, "a framework whose commands are absent is broken, not degraded")
	require.Contains(t, res.Message, "ape totally bogus")
	require.Contains(t, res.Message, "ape another missing")
	require.Contains(t, res.Message, "2 of 4")
}

// cobra.Find returns the deepest command it reached plus the leftover
// arguments, so a parent that exists without the child comes back as a
// partial match. Counting that as present is the failure this check exists
// to prevent.
func TestCommandSurface_PartialMatchIsMissing(t *testing.T) {
	t.Parallel()
	res := runCommandSurface(t, commandSurfaceProject(t, `
required_commands:
  - ape story definitelynotasubcommand
`))
	require.Equal(t, StatusFail, res.Status,
		"`ape story` exists but the subcommand does not — that is missing, not present")
	require.Contains(t, res.Message, "definitelynotasubcommand")
}

// The manifest carries two lists and only one of them is ape's question.
// `sanctioned` is strictly smaller — it lints skills, so it omits the
// operator-facing commands the binary nonetheless owes. Reading it would
// under-declare the surface.
func TestCommandSurface_IgnoresTheSanctionedList(t *testing.T) {
	t.Parallel()
	res := runCommandSurface(t, commandSurfaceProject(t, `
required_commands:
  - ape doctor
sanctioned:
  - ape totally bogus
family_commands:
  families: [adr, adrs]
  verbs: [update]
exempt_skills:
  apex-orchestrator: >-
    a folded string the reader must not trip on
forbidden_flags:
  - --strict
`))
	require.Equal(t, StatusOK, res.Status,
		"only required_commands is ape's to check; the rest belongs to the framework's own linter")
}

func TestCommandSurface_UnparseableManifestWarns(t *testing.T) {
	t.Parallel()
	res := runCommandSurface(t, commandSurfaceProject(t, "required_commands: [unclosed\n"))
	require.Equal(t, StatusWarn, res.Status,
		"cannot verify is not the same as broken, and a malformed framework file is not the user's fault")
	require.Contains(t, res.Message, framework.ProjectApeCommands)
}

func TestCommandSurface_EmptyListIsASkip(t *testing.T) {
	t.Parallel()
	res := runCommandSurface(t, commandSurfaceProject(t, "required_commands: []\n"))
	require.Equal(t, StatusSkip, res.Status)
}

func TestCommandSurface_OutsideAProjectIsInfo(t *testing.T) {
	t.Parallel()
	res := checkCommandSurface(context.Background(), doctorEnv{ProjectRoot: t.TempDir()})
	require.Equal(t, StatusInfo, res.Status)
}

// The entries are written the way an operator types them. Both spellings
// must resolve, in case a future manifest drops the binary name.
func TestCommandPath_DropsTheBinaryName(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"story", "fields"}, framework.CommandPath("ape story fields"))
	require.Equal(t, []string{"story", "fields"}, framework.CommandPath("story fields"))
	require.Equal(t, []string{"doctor"}, framework.CommandPath("  ape   doctor  "))
	require.Empty(t, framework.CommandPath("ape"))
	require.Empty(t, framework.CommandPath(""))
}

// The gate this check exists for. The framework's real shipped manifest is
// the authority on what the surface is; this pins that ape resolves the
// SHAPES it uses — multi-level paths, bare top-level commands, and the
// plural registry aliases whose whole point is that shipped prose names
// both spellings.
func TestCommandSurface_ResolvesTheShippedManifestShapes(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{
		"ape memory index",    // two levels
		"ape doctor",          // one level
		"ape framework setup", // nested group
		"ape costs run",       // nested group
		"ape adr update",      // singular family
		"ape adrs update",     // plural alias — both must answer
		"ape capabilities update",
		"ape deferred migrate",
		"ape sprint check",
	} {
		require.Empty(t, missingCommands([]string{entry}),
			"the framework ships %q in required_commands and this binary must provide it", entry)
	}
}

// Deliberately absent from the manifest, and it must stay that way:
// apex-orchestrator/SKILL.md states "There is no `ape pipeline dev` — never
// invent one." Pinned so nobody adds it to satisfy a check.
func TestCommandSurface_NoPipelineDev(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, missingCommands([]string{"ape pipeline dev"}),
		"the BUILD loop is skill-driven; a resolvable `ape pipeline dev` means something went wrong")
}

// The manifest the framework actually ships must resolve in full against
// this binary. Skipped when no framework checkout is available.
func TestCommandSurface_AgainstRealManifest(t *testing.T) {
	t.Parallel()
	// Same resolver as the other framework-checkout gates, so build and
	// released layouts behave identically across all of them.
	path := filepath.Join(frameworkSubtreeRoot(t), framework.SubtreeApeCommands)
	if _, err := os.Stat(path); err != nil {
		t.Skip("framework checkout ships no ape-commands.yaml (predates the contract)")
	}
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	root := commandSurfaceProject(t, string(body))
	res := runCommandSurface(t, root)
	require.Equal(t, StatusOK, res.Status,
		"this binary does not provide everything the framework requires: %s", res.Message)
}
