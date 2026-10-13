//go:build !windows

package pipeline //nolint:testpackage // white-box reads internal manifestWriter side effects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Same harness as runner_commit_test.go: a passive bash REPL shim, with
// the "work" each step does simulated by its WaitStepDone writing a file.
// That makes the tree dirty only while the run is under way, which is
// what a real stage leaves for the commit boundary to pick up.

// spawnCountingShim is claudeREPLShim plus one line per launch in a log,
// with the argv it was given, so a test can count stage spawns and see
// the --model each one got.
func spawnCountingShim(t *testing.T) (shim, log string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	dir := t.TempDir()
	shim = filepath.Join(dir, "claude-shim.sh")
	log = filepath.Join(dir, "spawns.log")
	body := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nPS1='❯ '\nexport PS1\nexec bash --noprofile --norc\n"
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim, log
}

type extRun struct {
	root     string
	manifest Manifest
	spawns   []string
	typed    []string // stage/stepIdx of every step that was typed
	waited   []string // stage/stepIdx of every WaitStepDone
	subjects []string // git log subjects, oldest first, without "init"
}

// runExtgates runs the extgates fixture against a project enabling
// enabled, under git, with every typed step writing out/<stage>-<idx>.md.
func runExtgates(t *testing.T, enabled []string, opts RunOptions) (extRun, error) {
	t.Helper()
	root := extProject(t, enabled)
	for _, rel := range []string{"always.md", "development/functionality/keep.md", "development/patterns-only/keep.md"} {
		seedFile(t, root, rel)
	}
	initGitRepo(t, root)
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	stubSpecSkills(t, root, spec)

	shim, log := spawnCountingShim(t)
	var (
		mu  sync.Mutex
		res = extRun{root: root}
	)
	opts.ProjectRoot = root
	opts.ClaudeBin = shim
	opts.ApeVersion = "0.10.0-test"
	opts.OnInteractiveStepStart = func(info InteractiveStepInfo) {
		mu.Lock()
		defer mu.Unlock()
		res.typed = append(res.typed, info.Stage+"/"+itoa(info.StepIdx))
	}
	opts.WaitStepDone = func(_ context.Context, stage string, idx int) error {
		mu.Lock()
		res.waited = append(res.waited, stage+"/"+itoa(idx))
		mu.Unlock()
		seedFile(t, root, "out/"+stage+"-"+itoa(idx)+".md")
		return nil
	}
	runErr := Run(context.Background(), spec, opts)

	res.manifest = loadLatestManifest(t, root, "extgates")
	if data, err := os.ReadFile(log); err == nil {
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			// The manifest's claude_version probe is not a stage spawn.
			if line != "--version" {
				res.spawns = append(res.spawns, line)
			}
		}
	}
	out, err := exec.Command("git", "-C", root, "log", "--reverse", "--format=%s").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	for s := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if s != "init" && !strings.HasPrefix(s, "test:") {
			res.subjects = append(res.subjects, s)
		}
	}
	return res, runErr
}

func itoa(i int) string { return string(rune('0' + i)) }

func stageRec(t *testing.T, m Manifest, name string) StageRecord {
	t.Helper()
	for i := range m.Stages {
		if m.Stages[i].Name == name {
			return m.Stages[i]
		}
	}
	t.Fatalf("stage %q not in manifest", name)
	return StageRecord{}
}

func committedFiles(t *testing.T, root, subject string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "log", "--format=", "--name-only", "--grep=^"+subject+"$").Output()
	if err != nil {
		t.Fatalf("git log --grep %s: %v", subject, err)
	}
	return strings.Fields(string(out))
}

func TestRun_ExtensionGates(t *testing.T) {
	res, err := runExtgates(t, []string{"ext-adrs", "ext-capabilities"}, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := res.manifest

	if m.Extensions == nil || !slices.Equal(m.Extensions.Enabled, []string{"ext-adrs", "ext-capabilities"}) || m.Extensions.Ignored {
		t.Errorf("manifest extensions = %+v", m.Extensions)
	}

	// Every stage is in the manifest, in spec order — a skipped one never
	// vanishes.
	names := make([]string, 0, len(m.Stages))
	for i := range m.Stages {
		names = append(names, m.Stages[i].Name)
	}
	if want := []string{"prep", "patterns-only", "adrs", "refresh", "tail-skip", "all-gated"}; !slices.Equal(names, want) {
		t.Fatalf("stages = %v, want %v", names, want)
	}

	// Stage skip: skipped whole, a skipped record per step, no metrics.
	po := stageRec(t, m, "patterns-only")
	if po.Status != StatusSkipped || po.SkipReason != SkipReasonExtension || !slices.Equal(po.MissingExtensions, []string{"ext-patterns"}) {
		t.Errorf("patterns-only = %+v", po)
	}
	if len(po.Steps) != 2 {
		t.Fatalf("patterns-only steps = %d, want 2", len(po.Steps))
	}
	for _, st := range po.Steps {
		if st.Status != StatusSkipped || st.EventsPath != "" || st.CostUSD != 0 || st.NumTurns != 0 || st.CommitStatus != "" ||
			!slices.Equal(st.MissingExtensions, []string{"ext-patterns"}) {
			t.Errorf("patterns-only step = %+v", st)
		}
	}

	// All steps skipped by step gates: a skipped stage, missing ids the
	// union of its steps'.
	ag := stageRec(t, m, "all-gated")
	if ag.Status != StatusSkipped || !slices.Equal(ag.MissingExtensions, []string{"ext-patterns", "ext-features"}) {
		t.Errorf("all-gated = %+v", ag)
	}
	if got := ag.Steps[1].MissingExtensions; !slices.Equal(got, []string{"ext-features", "ext-patterns"}) {
		t.Errorf("all-gated step 2 missing = %v, want its own gate's ids in spec order", got)
	}

	// Step skip inside a running stage.
	rf := stageRec(t, m, "refresh")
	if rf.Status != StatusCompleted || rf.Steps[0].Status != StatusSkipped || rf.Steps[1].Status != StatusCompleted {
		t.Errorf("refresh = %+v", rf)
	}
	if !slices.Equal(rf.Steps[0].MissingExtensions, []string{"ext-features"}) {
		t.Errorf("refresh step 1 missing = %v", rf.Steps[0].MissingExtensions)
	}

	// Never typed, never waited on: only the steps that run.
	wantTyped := []string{"prep/0", "adrs/0", "refresh/1", "tail-skip/0"}
	if !slices.Equal(res.typed, wantTyped) || !slices.Equal(res.waited, wantTyped) {
		t.Errorf("typed %v waited %v, want %v", res.typed, res.waited, wantTyped)
	}

	// One spawn per stage that runs; none for a stage skipped whole or
	// whose every step is gated off.
	if len(res.spawns) != 4 {
		t.Fatalf("spawns = %d (%v), want 4", len(res.spawns), res.spawns)
	}
	// The stage launches on its first RUNNING step's model.
	haiku, _, _, _, _ := mustSpec(t, res.root).Effective("refresh", 1) //nolint:dogsled // only the model is wanted
	opus, _, _, _, _ := mustSpec(t, res.root).Effective("refresh", 0)  //nolint:dogsled // only the model is wanted
	if !strings.Contains(res.spawns[2], "--model "+haiku) || strings.Contains(res.spawns[2], opus) {
		t.Errorf("refresh spawn argv = %q, want --model %s", res.spawns[2], haiku)
	}
	if rf.Steps[1].Model != haiku {
		t.Errorf("refresh step 2 ran on %q, want %q", rf.Steps[1].Model, haiku)
	}

	// Commits: a skipped stage fires none; prep's commit:false work
	// carries into the next boundary that fires; a stage whose last step
	// is skipped commits on the last step that ran.
	if want := []string{"specs: adrs", "specs: refresh", "specs: tail"}; !slices.Equal(res.subjects, want) {
		t.Errorf("commits = %v, want %v", res.subjects, want)
	}
	if files := committedFiles(t, res.root, "specs: adrs"); !slices.Contains(files, "out/prep-0.md") || !slices.Contains(files, "out/adrs-0.md") {
		t.Errorf("specs: adrs carries %v, want prep's work too", files)
	}
	ts := stageRec(t, m, "tail-skip")
	if ts.Steps[0].CommitStatus != CommitStatusCommitted || ts.Steps[1].CommitStatus != "" {
		t.Errorf("tail-skip commit statuses = %q, %q", ts.Steps[0].CommitStatus, ts.Steps[1].CommitStatus)
	}

	if m.Totals.StepsRun != 4 || m.Totals.StepsSkipped != 6 || m.Totals.StagesSkipped != 2 || m.Totals.CommitsMade != 3 {
		t.Errorf("totals = %+v", m.Totals)
	}

	report, err := os.ReadFile(filepath.Join(filepath.Dir(latestManifestPath(t, res.root)), "pipeline-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- skipped: extension: needs ext-patterns", "skipped (extension: needs ext-features)", "| steps skipped | 6 |", "- extensions: `ext-adrs`, `ext-capabilities`"} {
		if !strings.Contains(string(report), want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

func TestRun_ExtensionGates_FromSkippedStage(t *testing.T) {
	res, err := runExtgates(t, []string{"ext-adrs", "ext-capabilities"}, RunOptions{FromStage: "patterns-only"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.manifest.Stages[0]; got.Name != "patterns-only" || got.Status != StatusSkipped {
		t.Errorf("first stage = %s/%s, want patterns-only/skipped", got.Name, got.Status)
	}
	if res.typed[0] != "adrs/0" {
		t.Errorf("first typed step = %s, want adrs/0", res.typed[0])
	}
}

func TestRun_ExtensionGates_Ignored(t *testing.T) {
	res, err := runExtgates(t, []string{"ext-adrs"}, RunOptions{IgnoreExtensions: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := res.manifest
	if m.Extensions == nil || !m.Extensions.Ignored || !slices.Equal(m.Extensions.Enabled, []string{"ext-adrs"}) {
		t.Errorf("extensions = %+v", m.Extensions)
	}
	if m.Totals.StepsSkipped != 0 || m.Totals.StagesSkipped != 0 || m.Totals.StepsRun != 10 || len(res.spawns) != 6 {
		t.Errorf("ignored run: totals %+v, %d spawns", m.Totals, len(res.spawns))
	}
}

func TestRun_ExtensionGates_NoConfigRefuses(t *testing.T) {
	root := extProject(t, nil)
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatal(err)
	}
	err = runInteractive(t, root, spec, RunOptions{NoCommit: true})
	if err == nil || !strings.Contains(err.Error(), "--ignore-extensions") {
		t.Fatalf("Run err = %v, want the unresolved-extensions refusal", err)
	}
}

func mustSpec(t *testing.T, root string) *Spec {
	t.Helper()
	spec, err := LoadSpec("extgates", root)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func latestManifestPath(t *testing.T, root string) string {
	t.Helper()
	latest := filepath.Join(root, "_output", "ape", "pipelines", "extgates", "latest")
	target, err := os.Readlink(latest)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(filepath.Dir(latest), target, "manifest.yaml")
}
