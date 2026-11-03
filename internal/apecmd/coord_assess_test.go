package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
)

// fakePublisher records what a batch published.
type fakePublisher struct {
	mu      sync.Mutex
	notices []coordinator.Notice
	failed  []string
	fenced  bool
}

func (f *fakePublisher) Notices(_ context.Context, batch, project string, _ []string, ns []coordinator.Notice) (published, dup []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fenced {
		return nil, nil, coordinator.ErrFenced
	}
	f.notices = append(f.notices, ns...)
	for _, n := range ns {
		_, ms, _ := strings.Cut(n.Target, "/")
		published = append(published, coordinator.NoticeID(batch, project, ms))
	}
	return published, nil, nil
}

func (f *fakePublisher) Failed(_ context.Context, _, _, reason string, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, reason)
	return nil
}

// assessCheckout is a v1 project with M1 and M2 approved, main pushed to
// a bare origin, and a docs file M2's change edits on milestone/M2.
func assessCheckout(t *testing.T) (root, landed string) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	for rel, body := range map[string]string{
		"development/project.md":   "# P\n",
		"development/roadmap.md":   "# Roadmap\n\n## M1 — Timing\nStatus: approved (by t 2026-11-01T00:00:00Z)\nGate: ./gate1.sh\nGoal: g\n\n## M2 — Sweep\nStatus: approved (by t 2026-11-01T00:00:00Z)\nGate: ./gate2.sh\nGoal: g\n",
		"_apex/state/.gitignore":   "*\n!.gitignore\n",
		"internal/leader/sweep.go": "package leader\n",
		"docs/configuration.md":    "| var | pace |\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	bare := t.TempDir()
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	c := exec.Command("git", "init", "-q", "--bare", bare)
	require.NoError(t, c.Run())
	git("remote", "add", "origin", bare)
	git("push", "-q", "origin", "main")
	// M2's change b-2 adds docs/pace.md, on milestone/M2 only.
	git("switch", "-q", "-c", "milestone/M2")
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "pace.md"), []byte("back-off pace\n"), 0o644))
	git("add", "-A")
	git("commit", "-q", "-m", "feat: back off\n\nChange: b-2")
	landed = git("rev-parse", "HEAD")
	git("push", "-q", "origin", "milestone/M2")
	git("switch", "-q", "main")
	return root, landed
}

func assessBatch(id string) coordinator.Batch {
	return coordinator.Batch{ID: id, Events: []coordinator.TriggerEvent{{
		MsgID: "atomix/ck/plan-approved/M2/" + id, Subject: "ape.evt.dana.atomix.project.M2.plan-approved",
		Data: []byte(`{"event":"plan-approved","milestone":"M2","key":"` + id + `"}`),
	}}}
}

func readResult(t *testing.T, dir string) assessResult {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	require.NoError(t, err)
	var r assessResult
	require.NoError(t, json.Unmarshal(data, &r))
	return r
}

// PLAN-V15-ASSESSOR step 5, the coordinator's runner: a batch folder per
// batch and project, one session, its notice.json judged and published
// through the coordinator, and the batch's record kept (D762+): input.md,
// notice.json, the denials, the session's envelope and result.json, the
// snapshot dropped. Outcomes: published; refused (a secret in a reason:
// assessor-failed with the rule, never the text); assessor-failed (a
// denial, kept; no notice.json); fenced. The controls: a second batch
// does not overwrite the first's record, and a batch id already recorded
// is not run again.
func TestAssessRunner_OutcomesAndKeptRecords(t *testing.T) {
	checkout, landed := assessCheckout(t)
	dir := t.TempDir()
	var answer func(folder string)
	r := &assessRunner{
		dir:       dir,
		checkouts: map[string]string{"atomix": checkout},
		session: func(_ context.Context, folder, _ string) ([]byte, error) {
			// What ape task's launch builds beside the folder.
			skill := filepath.Join(folder+".plugin", "skills", "apex-assess", "SKILL.md")
			require.NoError(t, os.MkdirAll(filepath.Dir(skill), 0o700))
			require.NoError(t, os.WriteFile(skill, []byte("---\nname: apex-assess\n---\n"), 0o600))
			answer(folder)
			return []byte(`{"success":true}`), nil
		},
		logf: t.Logf,
	}
	write := func(body string) func(string) {
		return func(folder string) {
			require.NoError(t, os.WriteFile(filepath.Join(folder, apexv1.AssessNotice), []byte(body), 0o600))
		}
	}

	pub := &fakePublisher{}
	answer = write(`{"notices":[{"target":"atomix/M1","reason":"M2's sweep change alters the pace M1 documents","evidence":["internal/leader/sweep.go"]}]}`)
	r.onBatch(context.Background(), assessBatch("b-1"), pub)
	kept1 := r.keptDir("b-1", "atomix")
	res := readResult(t, kept1)
	require.Equal(t, assessPublished, res.Outcome)
	require.Equal(t, []string{"b-1-atomix-M1"}, res.Published)
	require.Equal(t, "28b13871baa3f128f320c8a84871465344afb814f1ca0cdadd8b8425d9878a6f", res.SkillSHA256, "the plugin SKILL.md's sha256 is kept")
	require.Len(t, pub.notices, 1)
	for _, f := range []string{"input.md", "notice.json", "task.json", "result.json"} {
		require.FileExists(t, filepath.Join(kept1, f))
	}
	require.NoDirExists(t, filepath.Join(dir, "b-1-atomix"), "the snapshot is dropped")
	require.NoDirExists(t, filepath.Join(dir, "b-1-atomix.plugin"))

	pub = &fakePublisher{}
	answer = write(`{"notices":[{"target":"atomix/M1","reason":"token ghp_abcdefghijklmnop","evidence":["internal/leader/sweep.go"]}]}`)
	r.onBatch(context.Background(), assessBatch("b-2"), pub)
	res = readResult(t, r.keptDir("b-2", "atomix"))
	require.Equal(t, assessRefused, res.Outcome)
	require.Empty(t, pub.notices, "nothing of a refused batch is published")
	require.Len(t, pub.failed, 1)
	require.Equal(t, "notice.json refused: a reason matches a secret pattern", pub.failed[0])
	require.NotContains(t, pub.failed[0], "ghp_")

	pub = &fakePublisher{}
	answer = func(folder string) {
		apexv1.AssessGuard(apexv1.HookPayload{ToolName: "Bash", Cwd: folder, ToolInput: json.RawMessage(`{"command":"id"}`)}, folder)
		write(`{"notices":[]}`)(folder)
	}
	r.onBatch(context.Background(), assessBatch("b-3"), pub)
	kept3 := r.keptDir("b-3", "atomix")
	res = readResult(t, kept3)
	require.Equal(t, assessFailed, res.Outcome)
	require.Contains(t, res.Reason, "outside its roster or folder")
	require.FileExists(t, filepath.Join(kept3, "denied.jsonl"), "a batch with a denial keeps it")
	require.Len(t, pub.failed, 1)

	pub = &fakePublisher{}
	answer = func(string) {}
	r.onBatch(context.Background(), assessBatch("b-4"), pub)
	require.Equal(t, assessFailed, readResult(t, r.keptDir("b-4", "atomix")).Outcome, "no notice.json is a failure, never nothing")

	pub = &fakePublisher{fenced: true}
	answer = write(`{"notices":[{"target":"atomix/M1","reason":"r","evidence":["docs/configuration.md"]}]}`)
	r.onBatch(context.Background(), assessBatch("b-5"), pub)
	require.Equal(t, assessFenced, readResult(t, r.keptDir("b-5", "atomix")).Outcome)

	// D762+: evidence a diff in the input changes, though main lacks it.
	pub = &fakePublisher{}
	answer = write(`{"notices":[{"target":"atomix/M1","reason":"b-2 documents a pace M1 relies on","evidence":["docs/pace.md"]}]}`)
	archived := coordinator.Batch{ID: "b-6", Events: []coordinator.TriggerEvent{{
		MsgID: "atomix/ck/change-archived/M2/b-2", Subject: "ape.evt.dana.atomix.project.M2.change-archived",
		Data: []byte(`{"event":"change-archived","milestone":"M2","change":"b-2","commit":"` + landed + `","key":"b-2"}`),
	}}}
	r.onBatch(context.Background(), archived, pub)
	res = readResult(t, r.keptDir("b-6", "atomix"))
	require.Equal(t, assessPublished, res.Outcome, "a diff-only evidence path publishes: %s %s", res.Reason, res.Detail)
	require.Equal(t, []string{"docs/pace.md"}, res.Folder.DiffPaths)

	// The controls.
	first, err := os.ReadFile(filepath.Join(kept1, "result.json"))
	require.NoError(t, err)
	pub = &fakePublisher{}
	ran := false
	answer = func(string) { ran = true }
	r.onBatch(context.Background(), assessBatch("b-1"), pub)
	require.False(t, ran, "a batch id already recorded is not run again")
	again, err := os.ReadFile(filepath.Join(kept1, "result.json"))
	require.NoError(t, err)
	require.Equal(t, string(first), string(again), "the first batch's record is not overwritten")
	entries, err := os.ReadDir(filepath.Join(dir, "kept"))
	require.NoError(t, err)
	require.Len(t, entries, 6, "one record per batch")
}
