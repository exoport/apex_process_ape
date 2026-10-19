package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

func TestInit_Greenfield(t *testing.T) {
	needSh(t)
	root := t.TempDir()
	gitEnv(t)
	run(t, root, "init", "-q")
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	for _, want := range []string{
		"_apex/config.yaml", "development/project.md", "development/roadmap.md", "AGENTS.md", "CLAUDE.md",
		"development/specs/", "development/changes/", "development/archive/", "development/decisions/", "development/briefs/", "_apex/state/", "_apex/state/.gitignore", "_apex/state/project.json",
	} {
		require.Contains(t, res.Created, want)
	}
	require.Len(t, res.HooksAdded, 5) // UserPromptSubmit too (E2)
	p, err := Open(root)
	require.NoError(t, err)
	require.Equal(t, "apex: project init", subjects(t, p)[0])
	clean(t, p, "project init")
	require.Equal(t, "@AGENTS.md\n", read(t, p, "CLAUDE.md"))
	require.True(t, strings.HasPrefix(read(t, p, "AGENTS.md"), "# AGENTS.md\n\n"+
		"Project context: read @development/project.md before planning or changing anything.\n\n"+
		"Talk to APEX: type `/apex-orchestrator` in Claude Code.\n\n<!--"),
		"D82: the project-context line, plain text, right under the title; A13: then the operator's line")
	_, err = p.OperatorInterfaceCheck()
	require.NoError(t, err, "a project rc.7 init made leaves seq-07 nothing to do")
	require.FileExists(t, filepath.Join(root, filepath.FromSlash("development/specs/.gitkeep")))
	// Backlog 26: a whitelist, so anything ape adds there later is ignored
	// by default; init commits it, and project.json stays local.
	require.Equal(t, "*\n!.gitignore\n!milestones.json\n!decisions.json\n", read(t, p, "_apex/state/.gitignore"))
	committed := run(t, root, "show", "--name-only", "--format=", "HEAD")
	require.Contains(t, committed, "_apex/state/.gitignore")
	require.NotContains(t, committed, "project.json")
	require.NoDirExists(t, filepath.Join(root, "development", ".apex"), "the old state folder is not made")
	cfg, err := LoadConfig(root)
	require.NoError(t, err)
	require.Equal(t, filepath.Base(root), cfg.ProjectName)
	require.Equal(t, "development", cfg.DevelopmentFolder)
	// D65: a new config starts with the model defaults.
	require.Equal(t, map[string]SkillModel{
		"apex-apply":   {Model: "sonnet", Effort: "high"},
		"apex-loop":    {Model: "sonnet", Effort: "high"},
		"apex-review":  {Model: "opus", Effort: "medium"},
		"apex-plan":    {Model: "opus", Effort: "medium"},
		"apex-change":  {Model: "opus", Effort: "medium"},
		"apex-accept":  {Model: "opus", Effort: "medium"},
		"apex-release": {Model: "sonnet", Effort: "high"},
	}, cfg.Models, "D93: apex-accept and apex-release too; Q96: the loop's row is apex-loop's, the operator has none")
	require.Empty(t, res.Warnings)
	require.Contains(t, committed, "_apex/config.yaml")

	// The skeleton is not a finished plan.
	got := findingMessages(p.Check())
	require.Contains(t, got, "`## Vision` is empty")
	require.Contains(t, got, "no milestones")

	// A second run creates nothing and adds no hook twice.
	res, err = Init(ctx(), root, false)
	require.NoError(t, err)
	require.Empty(t, res.Created)
	require.Empty(t, res.HooksAdded)
}

func TestInit_BrownfieldKeepsWhatExists(t *testing.T) {
	needSh(t)
	root := t.TempDir()
	gitEnv(t)
	run(t, root, "init", "-q")
	files := map[string]string{
		"CLAUDE.md":             "# Existing instructions\n",
		"AGENTS.md":             "# Existing map\n",
		"_apex/config.yaml":     "project_name: greeter\ndevelopment_folder: docs/dev\n",
		".claude/settings.json": `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo bye"}]}]}}`,
	}
	for rel, c := range files {
		path := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(c), 0o644))
	}
	res, err := Init(ctx(), root, true)
	require.NoError(t, err)
	require.True(t, res.Brownfield)
	require.Contains(t, res.Kept, "CLAUDE.md")
	require.Contains(t, res.Kept, "AGENTS.md")
	require.Contains(t, res.Created, "docs/dev/project.md", "development_folder from config is honoured")
	p, err := Open(root)
	require.NoError(t, err)
	require.Equal(t, "# Existing instructions\n@AGENTS.md\n", read(t, p, "CLAUDE.md"),
		"item 15: an existing CLAUDE.md gets the import line appended, and nothing else")
	require.Equal(t, []string{"_apex/config.yaml", "CLAUDE.md", "AGENTS.md"}, res.Amended)
	require.Equal(t, []string{"_apex/config.yaml"}, res.AmendedUncommitted, "D86: an untracked config is amended, not committed")
	require.Empty(t, res.Warnings)
	require.Equal(t, "# Existing map\n\nProject context: read @docs/dev/project.md before planning or changing anything.\n", read(t, p, "AGENTS.md"),
		"D82: an existing AGENTS.md gets the line under its title, naming the configured folder, and nothing else")
	require.Contains(t, read(t, p, "docs/dev/project.md"), "# greeter")
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(read(t, p, "_apex/state/project.json")), &rec))
	require.Equal(t, true, rec["brownfield"])

	var settings map[string]any
	require.NoError(t, json.Unmarshal([]byte(read(t, p, ".claude/settings.json")), &settings))
	require.Equal(t, "opus", settings["model"])
	hooks, ok := settings["hooks"].(map[string]any)
	require.True(t, ok)
	stop, ok := hooks["Stop"].([]any)
	require.True(t, ok)
	require.Len(t, stop, 2, "the existing Stop hook is kept and ape's is added")
	require.NotContains(t, findingMessages(p.Check()), "CLAUDE.md: must import the map")
	require.NotContains(t, findingMessages(p.Check()), "does not import")
	res2, err := Init(ctx(), root, true)
	require.NoError(t, err)
	require.Empty(t, res2.Amended, "idempotent")
	require.Empty(t, res2.Warnings, "the appended block names every skill")
	require.Equal(t, "# Existing instructions\n@AGENTS.md\n", read(t, p, "CLAUDE.md"))

	// Init commits only what it wrote: the repo's own files stay untracked.
	committed := run(t, root, "show", "--name-only", "--format=", "HEAD")
	require.Contains(t, committed, ".claude/settings.json")
	require.Contains(t, committed, "docs/dev/project.md")
	require.Contains(t, committed, "CLAUDE.md", "the one-line amendment is committed with the skeleton")
	require.Contains(t, committed, "AGENTS.md", "so is the project-context line")
	require.NotContains(t, committed, "_apex/config.yaml")
	require.Equal(t, files["_apex/config.yaml"]+modelDefaultsYAML, read(t, p, "_apex/config.yaml"),
		"D86: an existing config without models: gets the defaults appended, its own text kept")
}

// An existing _apex/config.yaml with its own models: and timeouts: is kept
// byte for byte (eval Q6). Committed before init, it is the baseline the
// config.changed guard judges from: the epoch is the init commit. Left
// uncommitted, init does not commit it (init commits only what it wrote),
// so the person's later commit of it is judged like any other: its
// guarded blocks (retries:, timeouts:) are config.changed until a person accepts
// them; models: is not guarded.
func TestInit_ExistingConfigBlocksAreKept(t *testing.T) {
	const cfg = "project_name: greeter\ndevelopment_folder: development\nmodels:\n  apex-apply: opus\nretries: 2\ntimeouts:\n  gate: 1h\n"
	setup := func(t *testing.T, commitFirst bool) *Project {
		t.Helper()
		needSh(t)
		root := t.TempDir()
		gitEnv(t)
		asPerson(t)
		run(t, root, "init", "-q", "-b", "main")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte(cfg), 0o644))
		if commitFirst {
			run(t, root, "add", "-A")
			run(t, root, "commit", "-q", "-m", "chore: apex config")
		}
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Contains(t, res.Kept, "_apex/config.yaml")
		require.NotContains(t, res.Amended, "_apex/config.yaml", "D86: a models: block is left alone")
		// D86: each skill init has a default for and models: does not
		// name is a warning, apex-apply's own entry aside.
		require.Len(t, res.Warnings, 6)
		for i, skill := range []string{"apex-loop", "apex-review", "apex-plan", "apex-change", "apex-accept", "apex-release"} {
			require.Equal(t, "_apex/config.yaml models: has no "+skill+" entry — "+skill+" runs on claude's default model; "+
				"add one to models: to choose its model and effort (see ape project init --help)", res.Warnings[i])
		}
		p, err := Open(root)
		require.NoError(t, err)
		require.Equal(t, cfg, read(t, p, "_apex/config.yaml"))
		return p
	}
	t.Run("committed before init: baseline", func(t *testing.T) {
		p := setup(t, true)
		require.Empty(t, p.configFindings(ctx(), HeadSHA(ctx(), p.Root)))
		require.NotContains(t, findingMessages(p.Check()), "guarded config keys")
	})
	t.Run("uncommitted at init: judged when committed", func(t *testing.T) {
		p := setup(t, false)
		require.NotContains(t, run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "_apex/config.yaml")
		epoch := HeadSHA(ctx(), p.Root)
		run(t, p.Root, "add", "_apex/config.yaml")
		run(t, p.Root, "commit", "-q", "-m", "chore: apex config")
		fs := p.configFindings(ctx(), epoch)
		require.Len(t, fs, 1)
		require.Contains(t, fs[0].Message, "guarded config keys (retries, timeouts) changed")
	})
}

func TestMergeHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	added, err := MergeHooks(path)
	require.NoError(t, err)
	require.Equal(t, []string{
		"SessionStart: ape hook session-start", "Stop: ape hook stop", "SubagentStop: ape hook stop", "PreToolUse: ape hook pre-tool",
		"UserPromptSubmit: ape hook user-prompt",
	}, added)
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &s))
	require.Equal(t, PreToolMatcher, s.Hooks["PreToolUse"][0].Matcher)
	require.Equal(t, StopHookTimeout, s.Hooks["Stop"][0].Hooks[0].Timeout)
	require.Equal(t, StopHookTimeout, s.Hooks["SubagentStop"][0].Hooks[0].Timeout)
	require.Equal(t, "command", s.Hooks["SessionStart"][0].Hooks[0].Type)
	require.Len(t, s.Hooks, 5, "nothing else")

	added, err = MergeHooks(path)
	require.NoError(t, err)
	require.Empty(t, added)

	require.NoError(t, os.WriteFile(path, []byte("{nope"), 0o644))
	_, err = MergeHooks(path)
	require.ErrorContains(t, err, "not valid JSON")
}

func TestStatus(t *testing.T) {
	p := startedWave(t)
	// Planning happens on the base branch; the started change's status is
	// read from its branch (D38).
	run(t, p.Root, "switch", "-q", "main")
	addChange(t, p, "add-other", waveChange("add-other"))
	commitAll(t, p, "plan: add-other")
	_, err := p.Block(ctx(), "add-other", "needs a decision")
	require.NoError(t, err)
	require.Equal(t, StatusDraft, state(t, p, "add-wave").Status, "main holds add-wave's draft")

	// Two task manifests, one before the approval and one after.
	writeManifest(t, p, "apex-plan", "20261014-100000-aaaa", "2026-10-14T10:00:00Z", 1.25)
	writeManifest(t, p, "apex-orchestrator", "20261015-130000-bbbb", "2026-10-15T13:00:00Z", 2.5)

	r := p.Status()
	require.True(t, r.Initialized)
	require.Equal(t, "M1", r.Milestone.ID)
	require.InDelta(t, 3.75, r.Spend.TotalUSD, 0.001)
	require.Equal(t, 2, r.Spend.Runs)
	require.InDelta(t, 2.5, r.Milestone.SpentUSD, 0.001, "spend since approval at 12:00Z")
	require.Len(t, r.Changes, 2)
	require.Equal(t, "main", r.Branch)
	require.Equal(t, StatusInProgress, r.Changes[1].Status, "add-wave's status comes from change/add-wave")
	require.Equal(t, "change/add-wave", r.Changes[1].Branch)
	require.Empty(t, r.Changes[0].Branch, "a draft never started has no branch")
	require.Contains(t, r.Next, "apply add-wave")
	require.Contains(t, r.Next, "on change/add-wave (`ape change start add-wave` checks it out)")

	brief := r.Brief()
	require.LessOrEqual(t, strings.Count(brief, "\n"), 25)
	require.Contains(t, brief, "Blocked:\n  add-other")
	require.Contains(t, brief, "needs a decision")
	require.Contains(t, brief, "Spend: $3.75 over 2 runs")
	require.Contains(t, brief, "APEX status — "+r.Project+" (on main)")
}

func TestStatus_BriefStaysUnder25Lines(t *testing.T) {
	p := fixture(t, baseFiles())
	for i := range 30 {
		id := "add-thing-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		addChange(t, p, id, waveChange(id))
	}
	brief := p.Status().Brief()
	require.LessOrEqual(t, strings.Count(brief, "\n"), 25, brief)
	require.Contains(t, brief, "… 18 more")
}

func TestStatus_NextSteps(t *testing.T) {
	require.Equal(t, "run `ape project init`", nextStep(&StatusReport{}))
	require.Contains(t, nextStep(&StatusReport{Initialized: true}), "apex-plan")
	require.Contains(t, nextStep(&StatusReport{Initialized: true, Milestone: &MilestoneStatus{ID: "M1", Status: MilestoneProposed}}), "ape milestone approve M1")
	m := &MilestoneStatus{ID: "M1", Status: MilestoneApproved}
	for status, want := range map[string]string{
		StatusDraft: "ape change start c1", StatusInProgress: "apex-apply", StatusVerified: "apex-review", StatusReviewed: "ape change archive c1",
	} {
		r := &StatusReport{Initialized: true, Milestone: m, Changes: []ChangeStatus{{ID: "c1", Milestone: "M1", Status: status}}}
		require.Contains(t, nextStep(r), want, status)
	}
	require.Contains(t, nextStep(&StatusReport{Initialized: true, Milestone: m}), "apex-change")
	m.GateLast = &GateRun{Exit: 0, SHA: "abcdef1234"}
	require.Contains(t, nextStep(&StatusReport{Initialized: true, Milestone: m}), "ape milestone close M1")
}

func writeManifest(t *testing.T, p *Project, skill, runID, started string, usd float64) {
	t.Helper()
	dir := filepath.Join(runlog.TasksRoot(p.Root), skill, runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	m := "run_id: " + runID + "\nstarted_at: " + started + "\ntotals:\n  cost_usd: " + strconv.FormatFloat(usd, 'f', -1, 64) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(m), 0o644))
}

// A guarded key the Config struct does not hold (tools:, before the tools
// slot) is compared as a raw block all the same.
func TestConfigChanged_GuardsAKeyOutsideTheStruct(t *testing.T) {
	p := fixture(t, baseFiles())
	epoch := HeadSHA(ctx(), p.Root)
	write(t, p, "_apex/config.yaml", "tools:\n  review:\n    - {name: lint, skill: lint-review, when: always}\n")
	commitAll(t, p, "chore: a review tool")
	fs := p.configFindings(ctx(), epoch)
	require.Len(t, fs, 1)
	require.Contains(t, fs[0].Message, "guarded config keys (tools) changed")
	require.Empty(t, p.guardedConfigDiff(ctx(), epoch, epoch, configYAML, guardedConfigKeys), "absent on both sides: equal")

	write(t, p, "_apex/config.yaml", "tools:\n  review:\n    - {name: lint, skill: lint-review, when: always}\nhuman_review: {delegate: apex-accept}\n")
	commitAll(t, p, "chore: a delegate")
	fs = p.configFindings(ctx(), epoch)
	require.Len(t, fs, 2)
	require.Contains(t, fs[0].Message, "guarded config keys (human_review) changed")
}

// D82: the project-context line goes after the first H1 outside code, or
// at the very top; a document that already carries the reference is left
// alone.
func TestWithProjectContext(t *testing.T) {
	line := projectContextLine("development") + "\n"
	for name, c := range map[string]struct{ in, want string }{
		"empty":            {"", line},
		"no title":         {"Build with make.\n", line + "\nBuild with make.\n"},
		"no title, blank":  {"\nBuild with make.\n", line + "\nBuild with make.\n"},
		"title only":       {"# Map\n", "# Map\n\n" + line},
		"no final newline": {"# Map", "# Map\n\n" + line},
		"title and blank":  {"# Map\n\n## Commands\n", "# Map\n\n" + line + "\n## Commands\n"},
		"title then text":  {"# Map\nSome text.\n", "# Map\n\n" + line + "\nSome text.\n"},
		"H2 first":         {"## Commands\n# Map\nx\n", "## Commands\n# Map\n\n" + line + "\nx\n"},
		"H1 in a fence":    {"```\n# not a title\n```\n# Map\n", "```\n# not a title\n```\n# Map\n\n" + line},
	} {
		got := withProjectContext(c.in, "development")
		require.Equal(t, c.want, got, name)
		require.True(t, hasProjectContext(got, "development"), name)
	}
}

// Q38: the reference counts, not the sentence — but only outside code,
// and only the configured folder's project.md.
func TestHasProjectContext(t *testing.T) {
	for doc, want := range map[string]bool{
		"Project context: read @development/project.md before planning or changing anything.\n": true,
		"Read @development/project.md first.\n":                                                 true,
		"- vision: @development/project.md\n":                                                   true,
		"(see @development/project.md)\n":                                                       true,
		"Read `@development/project.md` first.\n":                                               false,
		"Read ``x @development/project.md y`` first.\n":                                         false,
		"```\n@development/project.md\n```\n":                                                   false,
		"Read development/project.md first.\n":                                                  false,
		"Read @docs/dev/project.md first.\n":                                                    false,
		"Read @development/project.mdx first.\n":                                                false,
		"Mail x@development/project.md.\n":                                                      false,
		"Read `code` and @development/project.md.\n":                                            true,
		"An unclosed ` then @development/project.md\n":                                          true,
	} {
		require.Equal(t, want, hasProjectContext(doc, "development"), doc)
	}
}

// D82: project check requires the reference (agents.no-project-context, a
// person's), and re-running init adds it: a project initialised before
// rc.4 is fixed by init, once.
func TestProjectCheck_AgentsProjectContext(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "AGENTS.md", "# AGENTS.md\n\n## Commands\n- `make test`\n")
	commitAll(t, p, "chore: an rc.3 map")
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1)
	require.Equal(t, "agents.no-project-context", fs[0].Code)
	require.Equal(t, ActionPerson, ActionFor(fs[0].Code))
	require.Equal(t, "AGENTS.md", fs[0].File)
	require.Contains(t, fs[0].Message, `add the line "Project context: read @development/project.md before planning or changing anything."`)
	require.Contains(t, fs[0].Fix, "`ape project init` adds it")

	res, err := Init(ctx(), p.Root, false)
	require.NoError(t, err)
	require.Equal(t, []string{"AGENTS.md"}, res.Amended)
	require.Equal(t, "# AGENTS.md\n\n"+projectContextLine("development")+"\n\n## Commands\n- `make test`\n", read(t, p, "AGENTS.md"))
	require.Contains(t, run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "AGENTS.md")
	require.NotContains(t, findingMessages(p.Check()), "does not import")

	res, err = Init(ctx(), p.Root, false)
	require.NoError(t, err)
	require.Empty(t, res.Amended, "idempotent")
	require.Empty(t, res.Commit)
}

// D86: an existing config without a top-level models: gets init's
// defaults appended, byte for byte after its own text. The amendment is
// committed with the skeleton only when the config was tracked and clean;
// a person's untracked or edited config is amended and left for them.
func TestInit_AppendsModelDefaultsToExistingConfig(t *testing.T) {
	setup := func(t *testing.T, cfg string, commit bool) (string, *Project) {
		t.Helper()
		needSh(t)
		root := t.TempDir()
		gitEnv(t)
		asPerson(t)
		run(t, root, "init", "-q", "-b", "main")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte(cfg), 0o644))
		if commit {
			run(t, root, "add", "-A")
			run(t, root, "commit", "-q", "-m", "chore: apex config")
		}
		return root, &Project{Root: root}
	}
	allSeven := func(t *testing.T, root string) {
		t.Helper()
		c, err := LoadConfig(root)
		require.NoError(t, err)
		require.Len(t, c.Models, 7)
		require.Equal(t, SkillModel{Model: "opus", Effort: "medium"}, c.Models["apex-accept"])
		require.Equal(t, SkillModel{Model: "sonnet", Effort: "high"}, c.Models["apex-release"])
	}

	t.Run("tracked and clean: committed with the skeleton", func(t *testing.T) {
		const cfg = "project_name: greeter\n# a comment, kept\nretries: 1   # odd spacing, kept\n"
		root, p := setup(t, cfg, true)
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, []string{"_apex/config.yaml"}, res.Amended)
		require.Empty(t, res.AmendedUncommitted)
		require.Empty(t, res.Warnings)
		require.Equal(t, cfg+modelDefaultsYAML, read(t, p, "_apex/config.yaml"))
		allSeven(t, root)
		require.Contains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), "_apex/config.yaml")
		require.Equal(t, "apex: project init", subjects(t, p)[0])
		clean(t, p, "project init")

		res, err = Init(ctx(), root, true)
		require.NoError(t, err)
		require.Empty(t, res.Amended, "idempotent")
		require.Empty(t, res.Warnings)
	})
	t.Run("tracked but dirty: amended, not committed", func(t *testing.T) {
		root, p := setup(t, "project_name: greeter\n", true)
		dirty := "project_name: greeter\nretries: 2\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte(dirty), 0o644))
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, []string{"_apex/config.yaml"}, res.Amended)
		require.Equal(t, []string{"_apex/config.yaml"}, res.AmendedUncommitted)
		require.Equal(t, dirty+modelDefaultsYAML, read(t, p, "_apex/config.yaml"))
		require.NotContains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), "_apex/config.yaml")
		require.Equal(t, "M _apex/config.yaml", run(t, root, "status", "--porcelain"), "the person's edit, and the block, stay theirs to commit")
	})
	t.Run("staged: amended, not committed", func(t *testing.T) {
		root, _ := setup(t, "project_name: greeter\n", true)
		require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte("project_name: other\n"), 0o644))
		run(t, root, "add", "_apex/config.yaml")
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, []string{"_apex/config.yaml"}, res.AmendedUncommitted)
		require.NotContains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), "_apex/config.yaml")
	})
	t.Run("no final newline", func(t *testing.T) {
		root, p := setup(t, "project_name: greeter", false)
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, []string{"_apex/config.yaml"}, res.AmendedUncommitted)
		require.Equal(t, "project_name: greeter\n"+modelDefaultsYAML, read(t, p, "_apex/config.yaml"))
		allSeven(t, root)
	})
	t.Run("empty file", func(t *testing.T) {
		root, p := setup(t, "", false)
		_, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, modelDefaultsYAML, read(t, p, "_apex/config.yaml")) //nolint:testifylint // byte-exact, not only YAML-equal
	})
	t.Run("models: in a comment or a string is not a models: block", func(t *testing.T) {
		const cfg = "# models: are set below\nnotify: |\n  models: echo\n"
		root, p := setup(t, cfg, false)
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Equal(t, []string{"_apex/config.yaml"}, res.Amended)
		require.Equal(t, cfg+modelDefaultsYAML, read(t, p, "_apex/config.yaml"))
		c, err := LoadConfig(root)
		require.NoError(t, err)
		require.Equal(t, "models: echo\n", c.Notify, "the block scalar ends where the appended block starts")
	})
	// YAML reads the first document only: a block appended after a flow
	// mapping or a "---" parses, and is never read.
	for name, cfg := range map[string]string{"flow mapping": "{project_name: greeter}\n", "second document": "project_name: greeter\n---\n"} {
		t.Run("an append YAML would not read is restored, with a warning: "+name, func(t *testing.T) {
			root, p := setup(t, cfg, true)
			res, err := Init(ctx(), root, true)
			require.NoError(t, err)
			require.Empty(t, res.Amended)
			require.Equal(t, []string{"_apex/config.yaml has no models: block, and appending init's defaults would not be read " +
				"(the file's first YAML document ends before them) — it is restored as it was; add a models: block by hand (see ape project init --help)"}, res.Warnings)
			require.Equal(t, cfg, read(t, p, "_apex/config.yaml"))
			require.NotContains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), "_apex/config.yaml")
		})
	}
	t.Run("an empty models: names no skill", func(t *testing.T) {
		const cfg = "project_name: greeter\nmodels:\n"
		root, p := setup(t, cfg, false)
		res, err := Init(ctx(), root, true)
		require.NoError(t, err)
		require.Empty(t, res.Amended)
		require.Len(t, res.Warnings, 7)
		require.Equal(t, cfg, read(t, p, "_apex/config.yaml"))
	})
}

// D86 outside a git repo: the config is amended, and nothing is committed.
func TestInit_AppendsModelDefaultsOutsideGit(t *testing.T) {
	root := t.TempDir()
	asPerson(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte("project_name: greeter\n"), 0o644))
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	require.Equal(t, []string{"_apex/config.yaml"}, res.Amended)
	require.Equal(t, []string{"_apex/config.yaml"}, res.AmendedUncommitted)
	require.Empty(t, res.Commit)
	require.Equal(t, "project_name: greeter\n"+modelDefaultsYAML, read(t, &Project{Root: root}, "_apex/config.yaml"))
}

func TestModelDefaultSkills(t *testing.T) {
	require.Equal(t, []string{"apex-apply", "apex-loop", "apex-review", "apex-plan", "apex-change", "apex-accept", "apex-release"}, modelDefaultSkills())
}
