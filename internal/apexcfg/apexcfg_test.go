package apexcfg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// baseConfig is the framework's own _apex/config.yaml template, verbatim
// — the shape every real project starts from.
const baseConfig = `config_schema_version: "1"
project_name: my-project
extensions: []
user_name: Boss
communication_language: English
document_output_language: English
user_skill_level: intermediate
apex_folder: _apex
output_folder: _output
development_folder: development
docs_folder: docs
implementation_folder: development/implementation
planning_folder: development/planning
governance_repository_path: ""
governance_folder: development/governance
governance_staleness: warn
functionality_folder: development/functionality
`

// fixedClock is the injected wall-clock: a local-time instant whose
// date and timestamp renderings differ from their UTC equivalents, so a
// UTC regression shows up as a failing assertion rather than passing by
// coincidence.
func fixedClock() time.Time {
	return time.Date(2026, 8, 21, 23, 30, 45, 0, time.FixedZone("ART", -3*60*60))
}

// writeProject drops a project at dir with the given base config and an
// optional local overlay ("" means no local file at all).
func writeProject(t *testing.T, dir, base, local string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, DirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, DirName, BaseFile), []byte(base), 0o644))
	if local != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, DirName, LocalFile), []byte(local), 0o644))
	}
}

func TestResolve_BaseOnly(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, baseConfig, "")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)

	require.False(t, res.LocalOverlayApplied, "an absent local overlay is a normal outcome")
	require.Empty(t, res.LocalPath)
	require.Empty(t, res.OverlaidKeys)
	require.Equal(t, "my-project", res.ProjectName)
	require.Equal(t, "development/governance", res.GovernanceFolder)
	require.Equal(t, filepath.Join(root, "development", "governance", "adrs"), res.Paths.ADRs)
	require.Equal(t, "2026-08-21", res.Date)
	require.Equal(t, "20260821233045", res.Timestamp)
}

// TestResolve_EveryOverlayKey is the completeness lock: every variable
// the framework's On Activation block lists must survive the round trip
// with its configured value, not a zero one.
//
// The seventeen canonical variables are asserted non-empty. The declared
// optional one is asserted PRESENT IN THE PAYLOAD but empty, because
// baseConfig does not declare it and `ape` supplies no default — the
// framework applies that. An empty value here is the contract, not a
// gap: see Config's EvidenceFolder comment.
func TestResolve_EveryOverlayKey(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, baseConfig, "")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)

	got := map[string]any{
		"config_schema_version":      res.ConfigSchemaVersion,
		"project_name":               res.ProjectName,
		"extensions":                 res.Extensions,
		"user_name":                  res.UserName,
		"communication_language":     res.CommunicationLanguage,
		"document_output_language":   res.DocumentOutputLanguage,
		"user_skill_level":           res.UserSkillLevel,
		"apex_folder":                res.ApexFolder,
		"output_folder":              res.OutputFolder,
		"development_folder":         res.DevelopmentFolder,
		"docs_folder":                res.DocsFolder,
		"implementation_folder":      res.ImplementationFolder,
		"planning_folder":            res.PlanningFolder,
		"governance_repository_path": res.GovernanceRepositoryPath,
		"governance_folder":          res.GovernanceFolder,
		"governance_staleness":       res.GovernanceStaleness,
		"functionality_folder":       res.FunctionalityFolder,
		"evidence_folder":            res.EvidenceFolder,
	}
	require.Len(t, got, 18,
		"seventeen canonical framework variables plus its one declared optional one")
	for _, key := range OverlayKeys() {
		_, ok := got[key]
		require.True(t, ok, "overlay key %q has no field in the resolved payload", key)
	}
	// Non-list values are all non-empty in the template except the
	// deliberately-blank governance_repository_path — and the two optional
	// keys, which baseConfig does not declare and `ape` must not default.
	for key, v := range got {
		s, isStr := v.(string)
		if !isStr || key == "governance_repository_path" {
			continue
		}
		if IsOptionalKey(key) {
			require.Empty(t, s,
				"optional key %q must resolve empty when the project does not declare it — "+
					"the framework owns its default, and defaulting it here would make "+
					"`ape config resolve` assert a value nobody chose", key)
			continue
		}
		require.NotEmpty(t, s, "key %q resolved empty", key)
	}
}

// TestResolve_OptionalKeysCarryDeclaredValues is the other half: when a
// project DOES declare it, the value survives the round trip verbatim.
// It is passed through as written — no path is derived from it, because
// that resolution is the framework's.
func TestResolve_OptionalKeysCarryDeclaredValues(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root,
		baseConfig+"evidence_folder: development/governance/evidence\n", "")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)

	require.Equal(t, "development/governance/evidence", res.EvidenceFolder)
}

// TestApplyLocal_OptionalKeysOverridable is the point of the request: a
// project can override the key in config.local.yaml. Before it joined
// OverlayKeys the overlay loop skipped it silently, so a local value was
// read by nothing.
func TestApplyLocal_OptionalKeysOverridable(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root,
		baseConfig+"evidence_folder: evidence\n",
		"evidence_folder: development/governance/evidence\n")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)

	require.Equal(t, "development/governance/evidence", res.EvidenceFolder)
	require.Contains(t, res.OverlaidKeys, "evidence_folder")
}

// TestApplyLocal_KeyWise is the overlay's core contract: a key present
// in the local file replaces the base value, and every key absent from
// it keeps the base value. A decode-into-the-same-struct would blank the
// absent ones, which is the bug this asserts against.
func TestApplyLocal_KeyWise(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, baseConfig, `governance_repository_path: /srv/governance
user_name: Diego
`)

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)

	require.True(t, res.LocalOverlayApplied)
	require.Equal(t, filepath.Join(root, DirName, LocalFile), res.LocalPath)
	require.Equal(t, []string{"user_name", "governance_repository_path"}, res.OverlaidKeys,
		"overlaid keys are reported in OverlayKeys order, not file order")

	require.Equal(t, "/srv/governance", res.GovernanceRepositoryPath, "overlaid")
	require.Equal(t, "Diego", res.UserName, "overlaid")
	require.Equal(t, "my-project", res.ProjectName, "untouched by the overlay")
	require.Equal(t, "development/governance", res.GovernanceFolder, "untouched by the overlay")
	require.Equal(t, "development", res.DevelopmentFolder, "untouched by the overlay")
}

func TestApplyLocal_EmptyFileIsApplied(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, baseConfig, "# nothing but a comment\n")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)
	require.True(t, res.LocalOverlayApplied, "the file exists, so the overlay ran")
	require.Empty(t, res.OverlaidKeys, "it replaced nothing")
	require.Equal(t, "my-project", res.ProjectName)
}

// TestResolve_MalformedLocalIsFatal locks the owner's call: a broken
// override is a loud failure, never a silent fall-back to base values.
func TestResolve_MalformedLocalIsFatal(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, baseConfig, "user_name: [unterminated\n")

	_, err := Resolve(root, fixedClock)
	require.Error(t, err)
	var me *MalformedError
	require.ErrorAs(t, err, &me)
	require.Contains(t, me.Path, LocalFile, "the error names the file that failed")
}

func TestResolve_MalformedLocalWrongTypeNamesTheKey(t *testing.T) {
	root := t.TempDir()
	// A mapping where a string is expected: parses as YAML, fails to
	// decode into the field. The key has to reach the message or the
	// operator cannot find it in a 17-line file.
	writeProject(t, root, baseConfig, "user_name:\n  first: Diego\n")

	_, err := Resolve(root, fixedClock)
	require.Error(t, err)
	require.Contains(t, err.Error(), "user_name")
}

func TestResolve_MalformedBaseIsFatal(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "project_name: [oops\n", "")

	_, err := Resolve(root, fixedClock)
	var me *MalformedError
	require.ErrorAs(t, err, &me)
	require.Contains(t, me.Path, BaseFile)
}

func TestResolve_NoConfigAnywhere(t *testing.T) {
	root := t.TempDir()
	_, err := Resolve(root, fixedClock)
	require.ErrorIs(t, err, ErrNotFound)
}

// TestFind_WalksUpFromNestedCwd covers the real invocation shape: a
// skill runs `ape config resolve` from wherever it happens to be.
func TestFind_WalksUpFromNestedCwd(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, baseConfig, "")
	nested := filepath.Join(root, "development", "implementation", "deep")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	res, err := Resolve(nested, fixedClock)
	require.NoError(t, err)
	require.Equal(t, root, res.Root, "three levels down still finds the root")
}

// TestFind_TerminatesAtFilesystemRoot is the no-infinite-loop guard. The
// walk from a real absolute path with no config above it must return
// ErrNotFound rather than spinning on filepath.Dir's fixed point.
func TestFind_TerminatesAtFilesystemRoot(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := Find(t.TempDir())
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrNotFound)
	case <-time.After(10 * time.Second):
		t.Fatal("Find did not terminate walking up to the filesystem root")
	}
}

func TestDeriveExt(t *testing.T) {
	cases := []struct {
		name string
		exts []string
		want Ext
	}{
		{"none", nil, Ext{}},
		{"empty", []string{}, Ext{}},
		{"adrs only", []string{ExtADRs}, Ext{ADRs: true}},
		{
			"all four",
			[]string{ExtADRs, ExtPatterns, ExtCapabilities, ExtFeatures},
			Ext{ADRs: true, Patterns: true, Capabilities: true, Features: true},
		},
		{"unknown ignored", []string{"ext-nonsense", ExtFeatures}, Ext{Features: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, deriveExt(tc.exts))
		})
	}
}

func TestDerivePaths_FullLayout(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, baseConfig, "")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)

	j := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	require.Equal(t, j("_apex"), res.Paths.Apex)
	require.Equal(t, j("_output"), res.Paths.Output)
	require.Equal(t, j("development"), res.Paths.Development)
	require.Equal(t, j("development", "implementation"), res.Paths.Implementation)
	require.Equal(t, j("development", "governance", "adrs"), res.Paths.ADRs)
	require.Equal(t, j("development", "governance", "patterns"), res.Paths.Patterns)
	require.Equal(t, j("development", "functionality", "features"), res.Paths.Features)
	require.Equal(t, j("development", "functionality", "capabilities"), res.Paths.Capabilities)
	require.Equal(t, j("development", "team-memory.md"), res.Paths.TeamMemory)
	require.Equal(t, j("development", "implementation", "sprint-status.yaml"), res.Paths.SprintStatus)
	require.Equal(t, j("development", "implementation", "deferred-work.md"), res.Paths.DeferredLegacy)

	// The deferred store sits under development_folder, deliberately
	// OUTSIDE implementation_folder — ten skills glob
	// {implementation_folder}/**/*.md across 17 sites.
	require.Equal(t, j("development", "deferred"), res.Paths.Deferred)
	require.NotContains(t, res.Paths.Deferred, filepath.Join("development", "implementation"))
}

// TestDerivePaths_UnconfiguredFolderYieldsEmpty keeps "not configured"
// distinguishable from "configured as the project root" — a checker that
// treats the two alike would scan the whole repo.
func TestDerivePaths_UnconfiguredFolderYieldsEmpty(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, "config_schema_version: \"1\"\nproject_name: bare\n", "")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)
	require.Empty(t, res.Paths.Governance)
	require.Empty(t, res.Paths.ADRs)
	require.Empty(t, res.Paths.TeamMemory)
	require.Equal(t, filepath.Join(root, DirName), res.Paths.Apex, "apex_folder falls back to the fixed dir name")
}

func TestDerivePaths_AbsoluteFolderValue(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "elsewhere")
	writeProject(t, root, baseConfig, "development_folder: "+filepath.ToSlash(external)+"\n")

	res, err := Resolve(root, fixedClock)
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(external), res.Paths.Development,
		"an absolute folder value is honoured, not re-joined under the root")
}

// TestOverlayKeys_MatchesDecodeInto is the drift guard between the two
// lists that have to agree: every advertised overlay key must have a
// decode target, or an override would be silently ignored.
func TestOverlayKeys_MatchesDecodeInto(t *testing.T) {
	for _, key := range OverlayKeys() {
		var cfg Config
		var node yaml.Node
		require.NoError(t, node.Encode("x"))
		err := decodeInto(&cfg, key, node)
		// extensions is a list, so a scalar fails to decode — that is a
		// type error, not an unknown-key error, which is what we assert.
		if err != nil {
			require.NotContains(t, err.Error(), "unknown overlay key",
				"overlay key %q has no decode target", key)
		}
	}
	var cfg Config
	var node yaml.Node
	require.NoError(t, node.Encode("x"))
	err := decodeInto(&cfg, "not_a_key", node)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown overlay key")
}

func TestMalformedError_Unwrap(t *testing.T) {
	sentinel := errors.New("boom")
	me := &MalformedError{Path: "/x/_apex/config.yaml", Err: sentinel}
	require.ErrorIs(t, me, sentinel)
	require.Contains(t, me.Error(), "/x/_apex/config.yaml")
}
