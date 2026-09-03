package apecmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/registry"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/exoport/apex_process_ape/internal/story"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The APEX framework is a separate repository on a separate release cadence,
// and it consumes `ape` through three surfaces that no unit test of a single
// package covers:
//
//  1. The COMMANDS its skills invoke, with their flags and exit codes.
//  2. The FILES both sides agree on — `_apex/config.yaml`'s variables, the
//     four record registries, `sprint-status.yaml`'s row vocabulary.
//  3. The DELIVERY PATH — `ape sandbox framework materialize` puts a pinned
//     framework ref where aped mounts it, and the workspace bootstraps off
//     that mount.
//
// A package test proves the package. These prove the join, and they are
// deliberately written against shapes taken from a real project rather than
// shapes invented alongside the code — because a fixture written by the
// author of the code agrees with the code, which is exactly how a command
// can ship 100% wrong and 100% green.
//
// If you are here because one of these failed while you were working on
// something else: that is the point. The thing you changed is load-bearing
// for the framework, and the failure message says which half.

// fixtureProject is the committed real-shaped project — see
// testdata/apexproject/README.md for the four shapes it carries that a
// hand-built fixture does not.
func fixtureProject(t *testing.T) *apexcfg.Resolved {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "apexproject"))
	require.NoError(t, err)
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)
	return cfg
}

// TestContract_RealProjectIsClean is the single test that would have caught
// both shipped false-positive bugs. Every read-only project-data command has
// to be SILENT on a healthy project: a checker that cries wolf on every run
// is one an operator learns to ignore, which costs more than not having it.
func TestContract_RealProjectIsClean(t *testing.T) {
	cfg := fixtureProject(t)

	t.Run("registry verify finds nothing", func(t *testing.T) {
		report, err := registry.Verify(cfg, registry.VerifyOptions{})
		require.NoError(t, err)
		require.Empty(t, report.Findings, "findings on a healthy project: %+v", report.Findings)
		require.Equal(t, 4, report.Summary.Families, "all four families are enabled in the fixture")
		require.Positive(t, report.Summary.Records)
	})

	t.Run("story verify finds nothing", func(t *testing.T) {
		report, err := story.VerifyCorpus(cfg)
		require.NoError(t, err)
		require.Empty(t, report.Findings, "findings on a healthy project: %+v", report.Findings)
		require.Equal(t, 4, report.Summary.StoriesChecked)
		require.Equal(t, 5, report.Summary.FilesScanned,
			"the retrospective has no frontmatter and is not a story, but it was still looked at")
	})

	t.Run("sprint check finds nothing", func(t *testing.T) {
		report, err := sprint.RunCheck(cfg)
		require.NoError(t, err)
		require.Empty(t, report.Findings, "divergences on a healthy project: %+v", report.Findings)
		require.Equal(t, 4, report.Summary.StoryRows)
		require.Equal(t, 4, report.Summary.StoriesOnDisk)
		require.Equal(t, 2, report.Summary.EpicRows)
		require.Equal(t, 2, report.Summary.RetroRows)
	})
}

// TestContract_TrackerRowsKeyOnTheStoryKeyNotTheStoryID states the join
// explicitly, so a future refactor cannot quietly reintroduce it. The two
// identifiers are different strings on every real project and only one of
// them is what the tracker rows on.
func TestContract_TrackerRowsKeyOnTheStoryKeyNotTheStoryID(t *testing.T) {
	cfg := fixtureProject(t)

	heads, err := story.ScanHeads(cfg.Paths.Implementation)
	require.NoError(t, err)
	tracker, err := sprint.Load(cfg.Paths.SprintStatus)
	require.NoError(t, err)

	rowKeys := map[string]bool{}
	for _, r := range tracker.Rows {
		rowKeys[r.Key] = true
	}

	checked := 0
	for _, h := range heads {
		if !h.IsStory() {
			continue
		}
		checked++
		key := sprint.StoryKeyFromPath(h.Path)
		require.True(t, rowKeys[key], "no tracker row for story key %q", key)
		require.NotEqual(t, key, h.StoryID(),
			"the fixture must keep the two identifiers DIFFERENT (%q); a fixture where they "+
				"coincide cannot tell a correct join from the broken one", key)
		require.False(t, rowKeys[h.StoryID()],
			"story_id %q must not be a tracker row key — joining on it is the bug", h.StoryID())
	}
	require.Equal(t, 4, checked)
}

// TestContract_CapabilityIndexHasNoFileField pins the one family whose
// schema differs. capability-index-schema.json defines no `file` property,
// so a verifier that demands one reports a finding per capability forever
// and a sync that writes one produces an index the framework's own schema
// rejects.
func TestContract_CapabilityIndexHasNoFileField(t *testing.T) {
	byName := map[string]registry.Family{}
	for _, f := range registry.Families {
		byName[f.Name] = f
	}
	require.False(t, byName["capabilities"].HasFileField,
		"capabilities locate their record by id + slug")
	for _, name := range []string{"adrs", "patterns", "features"} {
		require.True(t, byName[name].HasFileField, "%s entries carry a file:", name)
	}

	// And the fixture index really has none, so the assertion above is about
	// the world and not only about a struct field.
	cfg := fixtureProject(t)
	data, err := os.ReadFile(filepath.Join(cfg.Paths.Capabilities, registry.IndexFileName))
	require.NoError(t, err)
	require.NotContains(t, string(data), "file:",
		"the capability index fixture must stay file:-free — that is the shape under test")
}

// TestContract_ConfigVariablesMatchTheFrameworkTemplate keeps the resolver
// and the framework's own `_apex/config.yaml` in step. A variable added on
// the framework side and missing here resolves as empty, and a folder
// variable that resolves as empty sends a writer to the wrong directory.
func TestContract_ConfigVariablesMatchTheFrameworkTemplate(t *testing.T) {
	cfg := fixtureProject(t)
	requireSameConfigKeys(t, cfg.ConfigPath)
}

// requireSameConfigKeys asserts the top-level keys of a config template are
// exactly the variables apexcfg resolves — no more, no fewer.
func requireSameConfigKeys(t *testing.T, templatePath string) {
	t.Helper()
	data, err := os.ReadFile(templatePath)
	require.NoError(t, err)

	inTemplate := topLevelYAMLKeys(t, data)
	resolves := map[string]bool{}
	for _, key := range apexcfg.OverlayKeys() {
		resolves[key] = true
	}

	// Direction 1, unchanged in force: a template variable ape does not
	// resolve reads as empty, and an empty folder variable sends a writer
	// to the wrong directory.
	for _, key := range inTemplate {
		require.True(t, resolves[key],
			"%s declares %q but apexcfg.OverlayKeys does not resolve it — an unresolved "+
				"folder variable reads as empty, and an empty folder variable sends a "+
				"writer to the wrong directory", templatePath, key)
	}

	// Direction 2, relaxed for the framework's DECLARED OPTIONAL variables
	// only. Each of those ships with a documented default its consumers
	// apply when the resolver omits the key, so a template that never
	// mentions one is correct rather than drifted — which is exactly the
	// state `model_profile` and `evidence_folder` are in until the
	// framework adds them to its own template. Every other key ape
	// resolves must still be declared, or the two lists have drifted.
	declared := map[string]bool{}
	for _, key := range inTemplate {
		declared[key] = true
	}
	var missing []string
	for _, key := range apexcfg.OverlayKeys() {
		if declared[key] || apexcfg.IsOptionalKey(key) {
			continue
		}
		missing = append(missing, key)
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"apexcfg.OverlayKeys resolves these mandatory variables that %s does not declare: %v",
		templatePath, missing)
}

func topLevelYAMLKeys(t *testing.T, data []byte) []string {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal(data, &doc))
	require.NotEmpty(t, doc.Content, "empty YAML document")
	root := doc.Content[0]
	require.Equal(t, yaml.MappingNode, root.Kind, "config template root is a mapping")
	keys := make([]string, 0, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		keys = append(keys, root.Content[i].Value)
	}
	return keys
}

// TestContract_LiveConfigTemplate is the same check against the framework
// repo itself. Opt-in, because CI has no sibling checkout — but when a
// developer has one, this is what catches a template change on the day it
// lands rather than on the day a skill writes to the wrong folder.
//
//	APEX_FRAMEWORK_REPO=/path/to/apex_process_framework go test ./internal/apecmd/ -run TestContract_LiveConfigTemplate
func TestContract_LiveConfigTemplate(t *testing.T) {
	root := frameworkSubtreeRoot(t)
	requireSameConfigKeys(t, filepath.Join(root, filepath.FromSlash(framework.SubtreeConfig)))
}

// frameworkSubtreeRoot resolves APEX_FRAMEWORK_REPO to the directory the
// `_apex/` and `.claude/` subtrees actually sit under, skipping when there
// is no checkout to read.
//
// Two layouts are in circulation and a test that knows only one fails on
// the other for a reason that has nothing to do with what it asserts. The
// RELEASED layout puts them at the repo root — the shape a project consumes,
// and the one framework.Subtree* constants target. The BUILD repo nests them
// under `framework/`, and that is where the framework authors work, so it is
// the checkout a developer is most likely to have on hand.
//
// Resolved in one place because the alternative is each gate rediscovering
// it: `check-framework` pointed at a build checkout failed here on its first
// run while the sibling gate beside it passed, purely because one had learned
// about both layouts and the other had not.
func frameworkSubtreeRoot(t *testing.T) string {
	t.Helper()
	repo := strings.TrimSpace(os.Getenv("APEX_FRAMEWORK_REPO"))
	if repo == "" {
		t.Skip("set APEX_FRAMEWORK_REPO to a framework checkout to compare against the live template")
	}
	for _, root := range []string{repo, filepath.Join(repo, "framework")} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(framework.SubtreeConfig))); err == nil {
			return root
		}
	}
	t.Skipf("no %s under %s (released layout) or its framework/ subdirectory (build layout)",
		framework.SubtreeConfig, repo)
	return ""
}

// TestContract_MigrationPathsAreDisjointFromTheInstallWriteSet derives the
// install side from the framework package's OWN constants rather than a
// list copied into a test.
//
// The disjointness is what makes `ape framework update` order-independent:
// the install dirties `.claude/` and `_apex/`, the migration writes under
// `development/`, and because the two sets cannot touch, the migration's
// path-scoped clean gate means the same thing before and after the install.
// A copied list cannot catch the regression this exists to catch — the day
// someone teaches the install to write under `development/`, the constant
// changes and the copy does not.
func TestContract_MigrationPathsAreDisjointFromTheInstallWriteSet(t *testing.T) {
	root := t.TempDir()
	installWrites := []string{
		framework.ProjectSkillsDir,
		framework.ProjectPipelinesDir,
		framework.ProjectConfig,
		framework.ProjectConfigLocalExample,
		framework.ProjectMetadata,
		framework.ProjectOperatingRules,
		framework.ProjectTerminalContracts,
		framework.ProjectClaudeMd,
	}

	cfgPath := filepath.Join(root, apexcfg.DirName)
	require.NoError(t, os.MkdirAll(cfgPath, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgPath, apexcfg.BaseFile), []byte(realProjectConfig), 0o644,
	))
	cfg, err := apexcfg.ResolveAt(root, nil)
	require.NoError(t, err)

	for _, m := range migrationPaths(cfg.Paths.Deferred, cfg.Paths.DeferredLegacy) {
		require.NotEmpty(t, m)
		for _, rel := range installWrites {
			i := filepath.Join(root, filepath.FromSlash(rel))
			require.False(t, within(m, i),
				"migration path %s sits under install path %s — the clean gate's scoping breaks", m, i)
			require.False(t, within(i, m),
				"install path %s sits under migration path %s — the clean gate's scoping breaks", i, m)
		}
	}
}

// within reports whether child is at or below parent.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// TestContract_FrameworkUpdateWritesNoCommits is the lock on the no-commit
// rule. `ape framework update` has never written a commit into the user's
// project, and the whole review story for a migration — one `git diff`, the
// operator groups it however they like — depends on that staying true. This
// fails against any implementation that starts committing, which is the only
// way to state a property whose evidence is an ABSENCE.
func TestContract_FrameworkUpdateWritesNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fwRepo := t.TempDir()
	fakeFrameworkForContract(t, fwRepo)

	root := newTestProject(t, realProjectConfig)
	gitInit(t, root)
	writeLegacyLedger(t, root, legacyLedgerFixture)
	gitCommitAll(t, root, "project baseline")

	before := gitLog(t, root)

	repo, cwd := fwRepo, root
	setup := newFrameworkSetupCmd(&repo, &cwd)
	_ = runCmdAllowError(t, setup, "--no-fetch", "--no-bootstrap")
	update := newFrameworkUpdateCmd(&repo, &cwd)
	out := runCmdAllowError(t, update, "--no-fetch")

	require.Equal(t, before, gitLog(t, root),
		"ape framework update wrote a commit into the project.\n"+
			"It never has, and the migration's reviewability depends on it not starting.\n"+
			"update output:\n%s", out)

	// And the migration really did run, or the assertion above is vacuous.
	require.False(t, pendingMigrations(root)[0].Pending,
		"the migration must have run for the no-commit assertion to mean anything")
	require.FileExists(t, filepath.Join(root, framework.ProjectMetadata),
		"the install must have run too")
}

// --- the sandbox delivery path -------------------------------------------
//
// A workspace gets the framework as a read-only mount rather than a baked
// image layer, and bootstraps off that mount. Three constants and one flag
// pair carry the whole handoff, they live in three different packages, and
// nothing else ties them together.

// TestContract_SandboxFrameworkHandoff pins the guest-side bootstrap line the
// docs and `ape sandbox framework --help` both tell an operator to type:
//
//	ape framework setup --no-fetch --repo /opt/apex-framework
//
// Renaming either flag, or moving the mount, breaks a documented workflow
// silently — the docs are prose and prose does not fail a build.
func TestContract_SandboxFrameworkHandoff(t *testing.T) {
	repo, cwd := "", ""
	setup := newFrameworkSetupCmd(&repo, &cwd)
	require.NotNil(t, setup.Flags().Lookup("no-fetch"),
		"the guest has no credentials and no network to the framework remote")

	parent := newFrameworkCmd()
	require.NotNil(t, parent.PersistentFlags().Lookup("repo"),
		"--repo is how a workspace names the read-only mount")

	require.Equal(t, "/opt/apex-framework", sandbox.FrameworkDest,
		"the mount destination is baked into docs, help text and aped's mount table")

	// The destination must stay reserved: a project that could mount over it
	// would choose which framework its own workspace installs from.
	_, err := sandbox.MergeUserMounts(nil, []workspace.MountSpec{
		{Source: t.TempDir(), Dest: sandbox.FrameworkDest},
	})
	require.Error(t, err, "a user mount must never be able to shadow the framework mount")

	// The materialize command's default root is what aped's --framework-root
	// defaults to; a mismatch means `ape sandbox up` cannot find a ref that
	// `ape sandbox framework materialize` just wrote.
	require.Equal(t, "/srv/apex-framework", DefaultFrameworkRoot)
}

// TestContract_ProjectDataResolvesInsideAWorkspaceMount checks the one thing
// the guest layout changes about path resolution: a project is mounted at
// /workspace/<name>, so the walk-up for `_apex/config.yaml` starts deep and
// must stop AT the project rather than escaping into the mount root — where,
// on a multi-repo workspace, it could find a sibling repo's config.
func TestContract_ProjectDataResolvesInsideAWorkspaceMount(t *testing.T) {
	// Simulate /workspace with two repos, each its own project.
	mountRoot := t.TempDir()
	for _, name := range []string{"app", "infra"} {
		dir := filepath.Join(mountRoot, name, apexcfg.DirName)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, apexcfg.BaseFile),
			[]byte(strings.Replace(realProjectConfig, "project_name: axon", "project_name: "+name, 1)), 0o644))
	}
	deep := filepath.Join(mountRoot, "app", "development", "implementation")
	require.NoError(t, os.MkdirAll(deep, 0o755))

	res, err := apexcfg.Resolve(deep, nil)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(mountRoot, "app"), res.Root)
	require.Equal(t, "app", res.ProjectName, "a sibling repo's config must never win")
}

// TestContract_SandboxCapacityStillReportsUnknownMemory guards a coupling
// that already bit once: `ape memory check` reuses humanBytes, which
// `ape sandbox capacity` owns, and generalising it for a 0-byte
// team-memory.md turned "the node did not report its memory" into "the node
// has 0 B of memory" three lines above a verdict that carefully separates
// the two.
func TestContract_SandboxCapacityStillReportsUnknownMemory(t *testing.T) {
	require.Equal(t, "-", memBytes(0), "an unreported capacity reading is not a measurement of zero")
	require.Equal(t, "-", memBytes(-1))
	require.Equal(t, "8.0 GiB", memBytes(8<<30))

	// memory check's side of the same helper: zero IS a measurement there.
	require.Equal(t, "0 B", humanBytes(0), "a 0-byte team-memory.md is a real reading")

	cmd := newSandboxCapacityCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	printCapacityHuman(cmd, workspace.Capabilities{})
	require.Regexp(t, `memory\s+- total, - available`, buf.String(),
		"an empty capability report must not read as a box with no RAM")
}

// --- helpers -------------------------------------------------------------

func runCmdAllowError(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		buf.WriteString("\nerror: " + err.Error())
	}
	return buf.String()
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

func gitLog(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "log", "--format=%H %s")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out)
}

// fakeFrameworkForContract is the minimum tree framework.Setup accepts.
func fakeFrameworkForContract(t *testing.T, root string) {
	t.Helper()
	mk := func(rel, body string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
	mk(framework.SubtreePipelines+"/design.yaml",
		"name: design\nstages:\n  s1:\n    chain:\n      - skill: apex-foo\n")
	mk(framework.SubtreeConfig, realProjectConfig)
	mk(framework.SubtreeConfigLocalExample, "# local-only\n")
	mk(framework.SubtreeSkills+"/apex-foo/SKILL.md", "# apex-foo\n")
	gitInit(t, root)
	gitCommitAll(t, root, "framework baseline")
}
