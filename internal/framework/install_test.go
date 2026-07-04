package framework_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/diegosz/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeFramework builds a self-contained framework repo at root, in the
// released layout (.claude/ and _apex/ at the repo root):
//   - _apex/pipelines/{design,governance,epics}.yaml
//   - _apex/config.yaml (template)
//   - _apex/config.local.example.yaml
//   - .claude/skills/apex-foo/SKILL.md
//   - .claude/skills/apex-bar/SKILL.md
//   - .claude/skills/non-apex/SKILL.md  (must not be touched)
//
// The repo is git-initialized on main with one commit, optionally tagged.
func fakeFramework(t *testing.T, root, tag string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}

	mkfile := func(rel, body string) {
		full := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}

	// Pipelines
	mkfile("_apex/pipelines/design.yaml", "name: design\nstages:\n  s1:\n    chain:\n      - skill: apex-foo\n")
	mkfile("_apex/pipelines/governance.yaml", "name: governance\nstages:\n  s1:\n    chain:\n      - skill: apex-bar\n")
	mkfile("_apex/pipelines/epics.yaml", "name: epics\nstages:\n  s1:\n    chain:\n      - skill: apex-foo\n")

	// Config templates
	mkfile("_apex/config.yaml", "config_schema_version: \"1\"\nproject_name: my-project\nextensions: []\nuser_name: Boss\n")
	mkfile("_apex/config.local.example.yaml", "# local-only\ngovernance_repository_path: /tmp/gov\n")

	// Skills (two apex-*, one non-apex)
	mkfile(".claude/skills/apex-foo/SKILL.md", "# apex-foo")
	mkfile(".claude/skills/apex-bar/SKILL.md", "# apex-bar")
	mkfile(".claude/skills/non-apex/SKILL.md", "# non-apex")

	ctx := context.Background()
	mustRun := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	mustRun("init", "-b", "main")
	mustRun("config", "user.email", "test@example.invalid")
	mustRun("config", "user.name", "Test")
	mustRun("config", "commit.gpgsign", "false")
	mustRun("add", ".")
	mustRun("commit", "-m", "init")
	if tag != "" {
		mustRun("tag", tag)
	}
}

func newProject(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func staticBootstrap(name string, exts ...string) framework.Bootstrapper {
	return framework.StaticBootstrapper{Values: framework.BootstrapValues{ProjectName: name, Extensions: exts}}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 9, 18, 0, 0, 0, time.UTC)
}

func TestUpdate_FreshInstall_HappyPath(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	res, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw,
		ProjectRoot:   proj,
		NoFetch:       true,
		ApeVersion:    "0.0.6",
		Bootstrapper:  staticBootstrap("greeter", "ext-adrs", "ext-features"),
		Now:           fixedNow,
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// Skills installed
	require.Equal(t, 2, res.Summary.SkillsInstalled)
	require.Equal(t, 0, res.Summary.SkillsRemoved)
	for _, rel := range []string{
		filepath.Join(".claude", "skills", "apex-foo", "SKILL.md"),
		filepath.Join(".claude", "skills", "apex-bar", "SKILL.md"),
	} {
		_, err := os.Stat(filepath.Join(proj, rel))
		require.NoError(t, err, "missing %s", rel)
	}
	// Non-apex skills are NOT installed (nor present).
	_, err = os.Stat(filepath.Join(proj, ".claude", "skills", "non-apex"))
	require.True(t, os.IsNotExist(err), "non-apex skills must not be installed")

	// Pipelines installed
	require.Equal(t, 3, res.Summary.PipelinesInstalled)
	for _, name := range []string{"design.yaml", "governance.yaml", "epics.yaml"} {
		_, err := os.Stat(filepath.Join(proj, "_apex", "pipelines", name))
		require.NoError(t, err, "missing pipeline %s", name)
	}

	// Config seeded with the bootstrap values
	require.True(t, res.Summary.ConfigSeeded)
	require.True(t, res.Summary.ConfigLocalSeeded)
	cfg, err := os.ReadFile(filepath.Join(proj, framework.ProjectConfig))
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(cfg, &parsed))
	require.Equal(t, "greeter", parsed["project_name"])
	require.ElementsMatch(t, []any{"ext-adrs", "ext-features"}, parsed["extensions"])
	// Other template fields preserved.
	require.Equal(t, "Boss", parsed["user_name"])

	// Metadata file written
	meta, err := framework.ReadMetadata(proj)
	require.NoError(t, err)
	require.Equal(t, "v0.0.71", meta.Framework.VersionTag)
	require.Equal(t, "main", meta.Framework.GitBranch)
	require.Equal(t, "0.0.6", meta.Ape.Version)
	require.Equal(t, fixedNow(), meta.InstalledAt)
	require.Equal(t, 2, meta.Sources.Skills.Count)
	require.Equal(t, 3, meta.Sources.Pipelines.Count)
	require.True(t, meta.Sources.Config.Seeded)
	require.Equal(t, "greeter", meta.Sources.Config.ProjectName)
	require.ElementsMatch(t, []string{"ext-adrs", "ext-features"}, meta.Sources.Config.Extensions)
}

func TestSetupThenUpdate_UpdatePreservesConfig(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	// Setup seeds config + writes framework.yaml.
	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: staticBootstrap("first", "ext-adrs"), Now: fixedNow,
	})
	require.NoError(t, err)

	// Update refreshes skills + pipelines but must NOT touch config.yaml
	// or alter the project_name + extensions recorded in framework.yaml.
	res, err := framework.Update(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Now: fixedNow,
	})
	require.NoError(t, err)
	require.False(t, res.Summary.ConfigSeeded, "update must not seed config")
	require.False(t, res.Summary.ConfigLocalSeeded, "update must not seed config.local.example")

	cfg, _ := os.ReadFile(filepath.Join(proj, framework.ProjectConfig))
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(cfg, &parsed))
	require.Equal(t, "first", parsed["project_name"], "update must not change project_name")

	// framework.yaml's ConfigSource must still record the Setup-time bootstrap.
	meta, err := framework.ReadMetadata(proj)
	require.NoError(t, err)
	require.True(t, meta.Sources.Config.Seeded)
	require.Equal(t, "first", meta.Sources.Config.ProjectName)
	require.ElementsMatch(t, []string{"ext-adrs"}, meta.Sources.Config.Extensions)
}

func TestSetup_RefusesIfAlreadyInstalled(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	// Initial setup succeeds.
	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: staticBootstrap("first", "ext-adrs"), Now: fixedNow,
	})
	require.NoError(t, err)

	// Second setup without --force is refused.
	_, err = framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: staticBootstrap("second", "ext-features"), Now: fixedNow,
	})
	require.Error(t, err)
	var aie *framework.AlreadyInstalledError
	require.ErrorAs(t, err, &aie)
	require.Contains(t, err.Error(), "framework already installed")
	require.Contains(t, err.Error(), "ape framework update")
}

func TestSetup_ForceBypassesAlreadyInstalled(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: staticBootstrap("first", "ext-adrs"), Now: fixedNow,
	})
	require.NoError(t, err)

	// --force re-bootstraps; project_name moves from "first" to "second".
	_, err = framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true, Force: true,
		ApeVersion: "0.0.6", Bootstrapper: staticBootstrap("second", "ext-features"), Now: fixedNow,
	})
	require.NoError(t, err)
	meta, err := framework.ReadMetadata(proj)
	require.NoError(t, err)
	require.Equal(t, "second", meta.Sources.Config.ProjectName)
}

func TestUpdate_RefusesIfNotInstalled(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	_, err := framework.Update(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Now: fixedNow,
	})
	require.Error(t, err)
	var nie *framework.NotInstalledError
	require.ErrorAs(t, err, &nie)
	require.Contains(t, err.Error(), "framework metadata not found")
	require.Contains(t, err.Error(), "ape framework setup")
}

func TestUpdate_RemovesStaleApexSkills(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "")
	proj := newProject(t)

	// Pre-populate the project with a stale apex skill that doesn't exist
	// in the framework anymore.
	stale := filepath.Join(proj, ".claude", "skills", "apex-removed")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "SKILL.md"), []byte("stale"), 0o644))

	// Also a non-apex skill that must be left alone.
	preserve := filepath.Join(proj, ".claude", "skills", "keep-me")
	require.NoError(t, os.MkdirAll(preserve, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(preserve, "SKILL.md"), []byte("user"), 0o644))

	res, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, res.Summary.SkillsRemoved, 1)
	require.Contains(t, res.Summary.SkillsRemovedPaths, filepath.Join(".claude", "skills", "apex-removed"))

	_, err = os.Stat(stale)
	require.True(t, os.IsNotExist(err), "stale apex-* skill must be removed")

	_, err = os.Stat(preserve)
	require.NoError(t, err, "non-apex skill must be preserved")
}

func TestUpdate_Bootstrap_Cancelled_DoesNotSeedButProceeds(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	res, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.NoError(t, err)
	require.False(t, res.Summary.ConfigSeeded)
	require.Equal(t, 2, res.Summary.SkillsInstalled, "skills install must proceed even when config bootstrap is skipped")

	_, err = os.Stat(filepath.Join(proj, framework.ProjectConfig))
	require.True(t, os.IsNotExist(err))
}

func TestUpdate_RefusesDirtyFrameworkWithoutForce(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	// Make framework dirty.
	require.NoError(t, os.WriteFile(filepath.Join(fw, "dirt.txt"), []byte("x"), 0o644))
	proj := newProject(t)

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.Error(t, err)
	var fve *framework.ValidationError
	require.ErrorAs(t, err, &fve)
	require.Equal(t, "framework_dirty", fve.Code)
}

func TestUpdate_ForceBypassesDirtyFramework(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	require.NoError(t, os.WriteFile(filepath.Join(fw, "dirt.txt"), []byte("x"), 0o644))
	proj := newProject(t)

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true, Force: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.NoError(t, err)
}

func TestUpdate_RefusesModifiedProjectSkillsWithoutForce(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")

	// Project is its own git repo with a committed apex-foo skill,
	// then locally modified.
	proj := newProject(t)
	mustRun := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = proj
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	mustRun("init", "-b", "main")
	mustRun("config", "user.email", "test@example.invalid")
	mustRun("config", "user.name", "Test")
	mustRun("config", "commit.gpgsign", "false")
	skillFile := filepath.Join(proj, ".claude", "skills", "apex-foo", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillFile), 0o755))
	require.NoError(t, os.WriteFile(skillFile, []byte("v1"), 0o644))
	mustRun("add", ".")
	mustRun("commit", "-m", "add apex-foo")
	require.NoError(t, os.WriteFile(skillFile, []byte("v2 — local edit"), 0o644))

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.Error(t, err)
	var pse *framework.ProjectSkillsModifiedError
	require.ErrorAs(t, err, &pse)
	require.NotEmpty(t, pse.Paths)
}

func TestUpdate_UntrackedProjectApexSkill_ProceedsWithoutForce(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")

	proj := newProject(t)
	mustRun := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = proj
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	mustRun("init", "-b", "main")
	mustRun("config", "user.email", "test@example.invalid")
	mustRun("config", "user.name", "Test")
	mustRun("config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(proj, "README.md"), []byte("hi"), 0o644))
	mustRun("add", ".")
	mustRun("commit", "-m", "init")
	// Untracked apex-* leftover from a prior install — must be safe to clobber.
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".claude", "skills", "apex-old"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".claude", "skills", "apex-old", "SKILL.md"), []byte("u"), 0o644))

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.NoError(t, err)
}

func TestUpdate_RefusesMissingFrameworkSubtree(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	// No fakeFramework call — repo has no _apex/pipelines etc.
	proj := newProject(t)

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.Error(t, err)
	var fve *framework.ValidationError
	require.ErrorAs(t, err, &fve)
	require.Equal(t, "framework_layout_invalid", fve.Code)
}

func TestStatus_NoFrameworkRepoArg_ReturnsInstalledOnly(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.NoError(t, err)

	res, err := framework.Status(ctx, framework.StatusOptions{ProjectRoot: proj, NoFetch: true})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Nil(t, res.Current)
	require.Nil(t, res.Drift)
	require.Equal(t, "v0.0.71", res.Installed.Framework.VersionTag)
}

func TestStatus_DetectsDriftAfterFrameworkAdvance(t *testing.T) {
	ctx := context.Background()
	fw := t.TempDir()
	fakeFramework(t, fw, "v0.0.71")
	proj := newProject(t)

	_, err := framework.Setup(ctx, &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "0.0.6", Bootstrapper: framework.NoopBootstrapper{}, Now: fixedNow,
	})
	require.NoError(t, err)

	// Advance the framework: new commit + new tag.
	require.NoError(t, os.WriteFile(filepath.Join(fw, "newfile.txt"), []byte("x"), 0o644))
	mustRun := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = fw
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	mustRun("add", ".")
	mustRun("commit", "-m", "advance")
	mustRun("tag", "v0.0.72")

	res, err := framework.Status(ctx, framework.StatusOptions{ProjectRoot: proj, FrameworkRepo: fw, NoFetch: true})
	require.NoError(t, err)
	require.NotNil(t, res.Current)
	require.NotNil(t, res.Drift)
	require.True(t, res.Drift.HashDrift)
	require.True(t, res.Drift.TagDrift)
	require.NotEmpty(t, res.Drift.Notes)
}

func TestStatus_NoMetadata_ActionableError(t *testing.T) {
	ctx := context.Background()
	proj := newProject(t)
	_, err := framework.Status(ctx, framework.StatusOptions{ProjectRoot: proj})
	require.Error(t, err)
	require.Contains(t, err.Error(), "framework metadata not found")
	require.Contains(t, err.Error(), "ape framework setup")
	require.NotContains(t, err.Error(), "no such file or directory")
}
