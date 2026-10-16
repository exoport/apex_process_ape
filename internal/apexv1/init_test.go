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
	require.Len(t, res.HooksAdded, 4)
	p, err := Open(root)
	require.NoError(t, err)
	require.Equal(t, "apex: project init", subjects(t, p)[0])
	clean(t, p, "project init")
	require.Equal(t, "@AGENTS.md\n", read(t, p, "CLAUDE.md"))
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
	require.Equal(t, []string{"CLAUDE.md"}, res.Amended)
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
	res2, err := Init(ctx(), root, true)
	require.NoError(t, err)
	require.Empty(t, res2.Amended, "idempotent")
	require.Equal(t, "# Existing instructions\n@AGENTS.md\n", read(t, p, "CLAUDE.md"))

	// Init commits only what it wrote: the repo's own files stay untracked.
	committed := run(t, root, "show", "--name-only", "--format=", "HEAD")
	require.Contains(t, committed, ".claude/settings.json")
	require.Contains(t, committed, "docs/dev/project.md")
	require.Contains(t, committed, "CLAUDE.md", "the one-line amendment is committed with the skeleton")
	require.NotContains(t, committed, "_apex/config.yaml")
}

func TestMergeHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	added, err := MergeHooks(path)
	require.NoError(t, err)
	require.Equal(t, []string{
		"SessionStart: ape hook session-start", "Stop: ape hook stop", "SubagentStop: ape hook stop", "PreToolUse: ape hook pre-tool",
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
	require.Len(t, s.Hooks, 4, "nothing else")

	added, err = MergeHooks(path)
	require.NoError(t, err)
	require.Empty(t, added)

	require.NoError(t, os.WriteFile(path, []byte("{nope"), 0o644))
	_, err = MergeHooks(path)
	require.ErrorContains(t, err, "not valid JSON")
}

func TestStatus(t *testing.T) {
	p := startedWave(t)
	addChange(t, p, "add-other", waveChange("add-other"))
	_, err := p.Block(ctx(), "add-other", "needs a decision")
	require.NoError(t, err)

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
	require.Contains(t, r.Next, "apply add-wave")

	brief := r.Brief()
	require.LessOrEqual(t, strings.Count(brief, "\n"), 25)
	require.Contains(t, brief, "Blocked:\n  add-other")
	require.Contains(t, brief, "needs a decision")
	require.Contains(t, brief, "Spend: $3.75 over 2 runs")
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
