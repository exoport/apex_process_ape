package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// hostChecks answer about this machine, not the project: left out where a
// test asserts on a project's rows.
var hostChecks = map[string]bool{
	"claude.binary": true, "git.binary": true, "node.binary": true, "npx.binary": true,
	"python3.binary": true, "png.converter": true, "playwright.host_supported": true,
	"playwright.cache": true, "skills.user": true, "skills.shadowed": true,
	"permissions.home_claude": true, "ape.update_available": true,
	"cost.price_table_coverage": true, "hooks.contract_drift": true, "kvm.available": true,
	"containerd.running": true, "kata.runtime": true, "sandbox.image": true,
	"sandbox.credential-acl": true, "sandbox.ape-delivery": true, "claude.version": true,
	// The ape floor compares this binary's version, a dev build's here.
	"framework.ape_version": true,
}

func doctorProjectRows(t *testing.T, root string) DoctorReport {
	t.Helper()
	env := doctorEnv{ProjectRoot: root, Home: t.TempDir()}
	var checks []doctorCheck
	for _, c := range scopeChecks(allChecks, env, nil) {
		if !hostChecks[c.Name] {
			checks = append(checks, c)
		}
	}
	return runDoctor(context.Background(), checks, env, nil)
}

func doctorRowNames(r DoctorReport) []string {
	out := make([]string, 0, len(r.Checks))
	for _, c := range r.Checks {
		out = append(out, c.Name)
	}
	return out
}

// H27: in a v1 project doctor runs the v1 rows and none of v0's, and a
// healthy project is green under --strict.
func TestDoctor_V1ProjectIsGreenUnderStrict(t *testing.T) {
	root := v1Repo(t)
	// A framework install puts skills there; the v1 layout alone does not.
	skill := filepath.Join(root, ".claude", "skills", "apex-plan", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skill), 0o755))
	require.NoError(t, os.WriteFile(skill, []byte("---\nname: apex-plan\n---\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "skills")

	r := doctorProjectRows(t, root)
	got := doctorRowNames(r)
	for _, want := range []string{"project.check", "hooks.project", "state.gitignore", "settings.shared", "plugins.project", "framework.command_surface"} {
		require.Contains(t, got, want)
	}
	for name := range v0OnlyChecks {
		require.NotContains(t, got, name, "a v0 row in a v1 project")
	}
	for _, c := range r.Checks {
		require.NotContains(t, []DoctorStatus{StatusWarn, StatusFail}, c.Status, "%s: %s", c.Name, c.Message)
	}
	require.False(t, doctorShouldFail(r, true), "--strict on a healthy v1 project")
}

// Each v1 row fails on the thing it guards.
func TestDoctor_V1RowsReportBreakage(t *testing.T) {
	root := v1Repo(t)
	status := func(name string) CheckResult {
		t.Helper()
		for _, c := range doctorProjectRows(t, root).Checks {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("no %s row", name)
		return CheckResult{}
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "project.md"), []byte("# G\n"), 0o644))
	r := status("project.check")
	require.Equal(t, StatusWarn, r.Status, "a non-required FAIL reads WARN")
	require.Contains(t, r.Message, "finding(s)")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{}\n"), 0o644))
	r = status("hooks.project")
	require.Equal(t, StatusWarn, r.Status)
	require.Contains(t, r.Message, "is not registered")

	// The whitelist replaced by a plain rule: the records are ignored, the
	// run records are not.
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "state", ".gitignore"), []byte("*.json\n"), 0o644))
	r = status("state.gitignore")
	require.Equal(t, StatusWarn, r.Status)
	require.Contains(t, r.Message, "milestones.json is ignored")
	require.Contains(t, r.Message, "_apex/state/runs is not ignored")

	// H25d: .claude/ ignored, and settings.json not tracked.
	v1Git(t, root, "rm", "-q", "--cached", ".claude/settings.json")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/\n"), 0o644))
	r = status("settings.shared")
	require.Equal(t, StatusWarn, r.Status)
	require.Contains(t, r.Message, "is ignored by git")
	require.Contains(t, r.Remediation, "un-ignore")
}

// Outside a v1 project the v1 rows do not run; a v0 project gets its v0
// rows and apex.v0 says which ape should be answering.
func TestDoctor_V0ProjectScope(t *testing.T) {
	root := v0Repo(t)
	r := doctorProjectRows(t, root)
	got := doctorRowNames(r)
	for name := range v1OnlyChecks {
		require.NotContains(t, got, name)
	}
	require.Contains(t, got, "registry.drift")
	for _, c := range r.Checks {
		if c.Name == "apex.v0" {
			require.Equal(t, StatusWarn, c.Status)
			require.True(t, strings.Contains(c.Remediation, "pinned v0 ape"), c.Remediation)
			return
		}
	}
	t.Fatal("no apex.v0 row")
}

// --only runs what it names, whatever the project.
func TestDoctor_OnlyBypassesTheScope(t *testing.T) {
	root := v1Repo(t)
	only := map[string]struct{}{"registry.drift": {}}
	checks, err := selectChecks(allChecks, only)
	require.NoError(t, err)
	require.Len(t, scopeChecks(checks, doctorEnv{ProjectRoot: root}, only), 1)
}

// H25d in project check: settings.ignored, action person.
func TestProjectCheck_SettingsIgnored(t *testing.T) {
	root := v1Repo(t)
	v1Git(t, root, "rm", "-q", "--cached", ".claude/settings.json")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/\n"), 0o644))
	stdout, _, err := v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	require.Error(t, err)
	var out struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	var codes []string
	for _, d := range out.Diagnostics {
		codes = append(codes, d.Code)
		if d.Code == "settings.ignored" {
			require.Equal(t, apexv1.ActionPerson, d.Action)
			require.Equal(t, ".claude/settings.json", d.File)
		}
	}
	require.Contains(t, codes, "settings.ignored")
}
