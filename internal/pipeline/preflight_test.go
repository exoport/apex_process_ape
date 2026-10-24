package pipeline_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// installPipelines materializes the canonical three pipeline yamls
// under <root>/_apex/pipelines/ so LoadSpec can find them. Mirrors the
// in-package helper in runner_test.go but is duplicated here because
// preflight_test is an external test package (pipeline_test).
func installPipelines(t *testing.T, root string) {
	t.Helper()
	dst := filepath.Join(root, "_apex", "pipelines")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	for _, name := range []string{"design", "governance", "epics"} {
		src := filepath.Join("testdata", "_apex", "pipelines", name+".yaml")
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read fixture %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name+".yaml"), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
}

func TestPreflight_AllPresent(t *testing.T) {
	// governance preflight checks for the sharded directories
	// produced by apex-shard-doc, not the original docs.
	dir := t.TempDir()
	installPipelines(t, dir)
	for _, rel := range []string{
		"development/planning/architecture",
		"development/planning/prd",
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := pipeline.LoadSpec("governance", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Preflight(spec, dir, nil); err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestPreflight_MissingFile(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	spec, err := pipeline.LoadSpec("governance", dir)
	if err != nil {
		t.Fatal(err)
	}
	err = pipeline.Preflight(spec, dir, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var pfe *pipeline.PreflightError
	if !errors.As(err, &pfe) {
		t.Fatalf("expected *PreflightError, got %T: %v", err, err)
	}
	if pfe.Pipeline != "governance" {
		t.Errorf("pipeline: got %q, want governance", pfe.Pipeline)
	}
	if len(pfe.Missing) == 0 {
		t.Error("expected at least one missing path")
	}
}

func TestPreflight_NoRequires(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	spec, err := pipeline.LoadSpec("design", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Preflight(spec, dir, nil); err != nil {
		t.Errorf("design has no requires.files; expected pass, got: %v", err)
	}
}

// installSkillStubs creates `<root>/.claude/skills/<name>/SKILL.md` for
// each name. The file contents don't matter — PreflightSkills only
// stats the path.
func installSkillStubs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		dir := filepath.Join(root, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("stub\n"), 0o644); err != nil {
			t.Fatalf("write SKILL.md for %s: %v", name, err)
		}
	}
}

// setFakeHome points os.UserHomeDir at the given directory portably.
// Linux/macOS read $HOME; Windows reads %USERPROFILE%. Tests that
// want the user-scope skill lookup to resolve against a temp dir need
// both vars set, otherwise the Windows leg of CI falls back to the
// runner's real home and the test becomes non-deterministic.
func setFakeHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// designSkills lists every skill + agent referenced by the design
// fixture. Kept in sync with testdata/_apex/pipelines/design.yaml.
var designSkills = []string{
	"apex-create-prd", "apex-agent-pm",
	"apex-shard-doc",
	"apex-create-ux-design", "apex-agent-ux-designer",
	"apex-create-architecture", "apex-agent-architect",
}

func TestPreflightSkills_AllPresent(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	installSkillStubs(t, dir, designSkills...)
	// Point HOME at an empty dir so the test isn't sensitive to the
	// host's actual ~/.claude/skills/ contents.
	setFakeHome(t, t.TempDir())

	spec, err := pipeline.LoadSpec("design", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.PreflightSkills(spec, dir, nil); err != nil {
		t.Errorf("expected pass with all skills stubbed, got: %v", err)
	}
}

func TestPreflightSkills_MissingSkill(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	// Stub everything except apex-create-ux-design — that's the gap we
	// expect PreflightSkills to surface.
	missing := "apex-create-ux-design"
	var present []string
	for _, s := range designSkills {
		if s != missing {
			present = append(present, s)
		}
	}
	installSkillStubs(t, dir, present...)
	setFakeHome(t, t.TempDir())

	spec, err := pipeline.LoadSpec("design", dir)
	if err != nil {
		t.Fatal(err)
	}
	err = pipeline.PreflightSkills(spec, dir, nil)
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	var pfe *pipeline.PreflightError
	if !errors.As(err, &pfe) {
		t.Fatalf("expected *PreflightError, got %T: %v", err, err)
	}
	if pfe.Kind != pipeline.PreflightKindSkills {
		t.Errorf("kind: got %v, want PreflightKindSkills", pfe.Kind)
	}
	if pfe.Pipeline != "design" {
		t.Errorf("pipeline: got %q, want design", pfe.Pipeline)
	}
	if len(pfe.Missing) != 1 {
		t.Fatalf("expected exactly 1 missing entry, got %d: %v", len(pfe.Missing), pfe.Missing)
	}
	if !strings.Contains(pfe.Missing[0], missing) {
		t.Errorf("missing entry should name the skill %q; got %q", missing, pfe.Missing[0])
	}
	if !strings.Contains(pfe.Missing[0], "create-ux-design") {
		t.Errorf("missing entry should name the stage; got %q", pfe.Missing[0])
	}
}

// TestPreflightSkills_UserScopeFallback confirms a skill missing from
// the project tree but present under HOME satisfies the check — that's
// the lookup order claude itself uses, so ape must match.
func TestPreflightSkills_UserScopeFallback(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	// Stage everything except apex-shard-doc in the project tree …
	var projectScoped []string
	for _, s := range designSkills {
		if s != "apex-shard-doc" {
			projectScoped = append(projectScoped, s)
		}
	}
	installSkillStubs(t, dir, projectScoped...)
	// … and stage apex-shard-doc only in the fake HOME.
	home := t.TempDir()
	installSkillStubs(t, home, "apex-shard-doc")
	setFakeHome(t, home)

	spec, err := pipeline.LoadSpec("design", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.PreflightSkills(spec, dir, nil); err != nil {
		t.Errorf("expected pass with skill in user-scope; got: %v", err)
	}
}

// TestPreflightSkills_AggregatesMultipleMissing ensures the error
// surfaces every missing skill in one shot rather than failing fast on
// the first — important for the typo'd-pipeline case where the author
// wants to fix everything in one edit pass.
func TestPreflightSkills_AggregatesMultipleMissing(t *testing.T) {
	dir := t.TempDir()
	installPipelines(t, dir)
	// Install nothing — every reference should be flagged.
	setFakeHome(t, t.TempDir())

	spec, err := pipeline.LoadSpec("design", dir)
	if err != nil {
		t.Fatal(err)
	}
	err = pipeline.PreflightSkills(spec, dir, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var pfe *pipeline.PreflightError
	if !errors.As(err, &pfe) {
		t.Fatalf("expected *PreflightError, got %T: %v", err, err)
	}
	// design references 7 distinct skills/agents (apex-shard-doc
	// appears three times but is deduped).
	const want = 7
	if len(pfe.Missing) != want {
		t.Errorf("missing count: got %d, want %d (entries: %v)", len(pfe.Missing), want, pfe.Missing)
	}
}

// F-E1-1: a plugin skill (<plugin>:<skill>) has no SKILL.md under
// .claude/skills. With a resolver it is the resolver's to judge, and only
// it: a name without a ':' is still looked up on disk, never asked.
func TestPreflightSkills_PluginSkillResolver(t *testing.T) {
	dir := t.TempDir()
	setFakeHome(t, t.TempDir())
	installSkillStubs(t, dir, "apex-foo")
	var asked []string
	resolve := func(name string) error {
		asked = append(asked, name)
		if name == "apish:nats-expert" {
			return nil
		}
		return errors.New("no plugin nope in plugins: — list the plugin nope in plugins: and run `ape plugins sync`, or fix the skill name")
	}
	step := pipeline.Step{Skill: "apish:nats-expert"}
	require.NoError(t, pipeline.PreflightSkills(pipeline.NewSingleStepSpec("apish:nats-expert", step, nil), dir, resolve))

	err := pipeline.PreflightSkills(pipeline.NewSingleStepSpec("nope:x", pipeline.Step{Skill: "nope:x"}, nil), dir, resolve)
	var pfe *pipeline.PreflightError
	require.ErrorAs(t, err, &pfe)
	require.Equal(t, pipeline.PreflightKindSkills, pfe.Kind)
	require.Equal(t, `pipeline "nope:x": skill missing: "nope:x" (stage nope:x, step 1): no plugin nope in plugins: — list the plugin nope in plugins: and run `+"`ape plugins sync`"+`, or fix the skill name`, err.Error())

	require.NoError(t, pipeline.PreflightSkills(pipeline.NewSingleStepSpec("apex-foo", pipeline.Step{Skill: "apex-foo"}, nil), dir, resolve))
	err = pipeline.PreflightSkills(pipeline.NewSingleStepSpec("apex-bar", pipeline.Step{Skill: "apex-bar"}, nil), dir, resolve)
	require.ErrorContains(t, err, `"apex-bar" (stage apex-bar, step 1): SKILL.md not found in`)
	require.Equal(t, []string{"apish:nats-expert", "nope:x"}, asked, "a name without a ':' never reaches the resolver")

	// No resolver: a plugin skill is looked up on disk, as before.
	err = pipeline.PreflightSkills(pipeline.NewSingleStepSpec("apish:nats-expert", step, nil), dir, nil)
	require.ErrorContains(t, err, `"apish:nats-expert" (stage apish:nats-expert, step 1): SKILL.md not found in`)
}
