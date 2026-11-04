package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/coord/rebuild"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// fakePublisher records what a batch published.
type fakePublisher struct {
	mu      sync.Mutex
	notices []coordinator.Notice
	failed  []string
	skipped []coordinator.Skip
	metrics []coordinator.AssessMetrics
	prior   []string
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

func (f *fakePublisher) Skipped(_ context.Context, s coordinator.Skip) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skipped = append(f.skipped, s)
	return nil
}

func (f *fakePublisher) Published(context.Context, string, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prior, nil
}

func (f *fakePublisher) Metrics(_, _ string, m coordinator.AssessMetrics) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics = append(f.metrics, m)
	return nil
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
		budget:    -1,
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

// PLAN-V15-ASSESSOR step 7, the token line (D2, Q5): a session over its
// cap (ape task's --max-tokens 1.5M, termination max_tokens) ends limit,
// with nothing published but assessor-failed, and counts at least the cap;
// past the stage budget, or with the usage guard against a launch, a batch
// is skipped (assessor-skipped, the stage's first marked first) and no
// session runs; each session's tokens go to the ledger and to ape.metrics.
// The control: under the budget, a batch runs.
func TestAssessRunner_TokenLine(t *testing.T) {
	require.Contains(t, strings.Join(assessTaskArgs("/b", "/c"), " "), "--max-duration 10m0s --max-tokens 1500000")
	checkout, _ := assessCheckout(t)
	dir := t.TempDir()
	sessions := 0
	var answer func(folder string) []byte
	var sessionErr error
	usageAgainst := ""
	r := &assessRunner{
		dir: dir, checkouts: map[string]string{"atomix": checkout}, budget: 2_000_000, logf: t.Logf,
		session: func(_ context.Context, folder, _ string) ([]byte, error) {
			sessions++
			return answer(folder), sessionErr
		},
		usageSkip: func(string) (string, bool) { return usageAgainst, usageAgainst != "" },
	}
	envelope := func(folder string, tokens int, termination string) []byte {
		man := filepath.Join(folder, "_apex", "state", "runs", "tasks", "apex-assess", "latest", "manifest.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(man), 0o700))
		body := "status: completed\n"
		if termination != "" {
			body = "status: failed\ntermination:\n  kind: " + termination + "\n  message: stopped\n"
		}
		require.NoError(t, os.WriteFile(man, []byte(body), 0o600))
		rel, _ := filepath.Rel(folder, man)
		// json.Marshal, not a hand-built string: on Windows rel has
		// backslashes, which are escapes inside a JSON string.
		relJSON, err := json.Marshal(rel)
		require.NoError(t, err)
		return []byte(`{"success":` + map[bool]string{true: "true", false: "false"}[termination == ""] + `,"usage":{"input_tokens":` + strconv.Itoa(tokens) + `},"manifest_path":` + string(relJSON) + `}`)
	}

	pub := &fakePublisher{}
	answer = func(folder string) []byte {
		require.NoError(t, os.WriteFile(filepath.Join(folder, apexv1.AssessNotice), []byte(`{"notices":[]}`), 0o600))
		return envelope(folder, 300_000, "")
	}
	r.onBatch(context.Background(), assessBatch("t-1"), pub)
	res := readResult(t, r.keptDir("t-1", "atomix"))
	require.Equal(t, assessPublished, res.Outcome, "control: under the budget it runs")
	require.Equal(t, int64(300_000), res.Tokens)
	require.Len(t, pub.metrics, 1)
	require.Equal(t, int64(300_000), pub.metrics[0].TokensTotal)
	require.FileExists(t, filepath.Join(r.keptDir("t-1", "atomix"), "manifest.yaml"))

	pub = &fakePublisher{}
	answer = func(folder string) []byte {
		require.NoError(t, os.WriteFile(filepath.Join(folder, apexv1.AssessNotice), []byte(`{"notices":[{"target":"atomix/M1","reason":"r","evidence":["internal/leader/sweep.go"]}]}`), 0o600))
		return envelope(folder, 0, "max_tokens")
	}
	sessionErr = errors.New("exit status 1")
	r.onBatch(context.Background(), assessBatch("t-2"), pub)
	res = readResult(t, r.keptDir("t-2", "atomix"))
	require.Equal(t, assessLimit, res.Outcome)
	require.Empty(t, pub.notices, "a session over its cap publishes no notice")
	require.Len(t, pub.failed, 1)
	require.Contains(t, pub.failed[0], "max_tokens")
	require.Equal(t, int64(1_500_000), res.Tokens, "counted at the cap at least")
	require.Equal(t, int64(1_800_000), r.ledger().Tokens)

	// 1.8M of 2M: one more runs, and takes the ensemble past its budget.
	pub = &fakePublisher{}
	answer = func(folder string) []byte {
		require.NoError(t, os.WriteFile(filepath.Join(folder, apexv1.AssessNotice), []byte(`{"notices":[]}`), 0o600))
		return envelope(folder, 400_000, "")
	}
	sessionErr = nil
	r.onBatch(context.Background(), assessBatch("t-3"), pub)
	require.Equal(t, assessPublished, readResult(t, r.keptDir("t-3", "atomix")).Outcome)
	before := sessions
	for i, id := range []string{"t-4", "t-5"} {
		pub = &fakePublisher{}
		r.onBatch(context.Background(), assessBatch(id), pub)
		res = readResult(t, r.keptDir(id, "atomix"))
		require.Equal(t, assessSkipped, res.Outcome, id)
		require.Equal(t, "budget", res.Reason)
		require.Len(t, pub.skipped, 1, "published once")
		require.Equal(t, i == 0, pub.skipped[0].First, "the stage's first skip is marked first")
		require.Equal(t, int64(2_200_000), pub.skipped[0].Spent)
	}
	require.Equal(t, before, sessions, "a skipped batch runs no session")

	r.budget = -1
	usageAgainst = "usage guard: the 5-hour window is at 97%"
	pub = &fakePublisher{}
	r.onBatch(context.Background(), assessBatch("t-6"), pub)
	res = readResult(t, r.keptDir("t-6", "atomix"))
	require.Equal(t, assessSkipped, res.Outcome)
	require.Equal(t, "usage", res.Reason)
	require.Equal(t, before, sessions)
}

// Step 8 on the runner's side: a predecessor killed mid-session left the
// batch's folder (and a denial) and no record; the replayed batch runs
// afresh and publishes. The control: a recorded batch is still not run
// again (TestAssessRunner_OutcomesAndKeptRecords).
func TestAssessRunner_ReplayAfterAKill(t *testing.T) {
	checkout, _ := assessCheckout(t)
	dir := t.TempDir()
	r := &assessRunner{
		dir: dir, checkouts: map[string]string{"atomix": checkout}, budget: -1, logf: t.Logf,
		session: func(_ context.Context, folder, _ string) ([]byte, error) {
			require.NoError(t, os.WriteFile(filepath.Join(folder, apexv1.AssessNotice), []byte(`{"notices":[]}`), 0o600))
			return []byte(`{"success":true}`), nil
		},
	}
	stale := filepath.Join(dir, "k-1-atomix")
	require.NoError(t, os.MkdirAll(filepath.Join(stale, "half"), 0o700))
	require.NoError(t, os.WriteFile(apexv1.AssessDeniedPath(stale), []byte(`{"tool":"Bash"}`+"\n"), 0o600))
	b := assessBatch("k-1")
	b.Replay = true
	pub := &fakePublisher{}
	r.onBatch(context.Background(), b, pub)
	res := readResult(t, r.keptDir("k-1", "atomix"))
	require.Equal(t, assessPublished, res.Outcome, "%s %s", res.Reason, res.Detail)
	require.Empty(t, res.Denials, "the dead session's denial is not this run's")
}

// --assess-budget: tokens, 0 (nothing allowed: PLAN §4's "budget 0 →
// every batch skipped"), off (no budget).
func TestAssessBudgetFlag(t *testing.T) {
	for v, want := range map[string]int64{"20M": 20_000_000, "0": 0, "off": -1, "1.5M": 1_500_000} {
		got, err := assessBudget(v)
		require.NoError(t, err, v)
		require.Equal(t, want, got, v)
	}
	_, err := assessBudget("lots")
	require.Error(t, err)
	checkout, _ := assessCheckout(t)
	ran := false
	r := &assessRunner{
		dir: t.TempDir(), checkouts: map[string]string{"atomix": checkout}, budget: 0, logf: t.Logf,
		session: func(context.Context, string, string) ([]byte, error) { ran = true; return nil, nil },
	}
	pub := &fakePublisher{}
	r.onBatch(context.Background(), assessBatch("z-1"), pub)
	require.False(t, ran, "budget 0: no session")
	require.Len(t, pub.skipped, 1)
	require.True(t, pub.skipped[0].First)
}

// Found by the §4 stub check: on a host whose state dir does not exist yet
// (the coordinator's first batch), the runner makes it. The T1: a dir
// that does not exist → the batch runs and publishes.
func TestAssessRunner_MakesItsStateDir(t *testing.T) {
	checkout, _ := assessCheckout(t)
	dir := filepath.Join(t.TempDir(), "state", "ape", "coord", "ens", "assess")
	r := &assessRunner{
		dir: dir, checkouts: map[string]string{"atomix": checkout}, budget: -1, logf: t.Logf,
		session: func(_ context.Context, folder, _ string) ([]byte, error) {
			require.NoError(t, os.WriteFile(filepath.Join(folder, apexv1.AssessNotice), []byte(`{"notices":[]}`), 0o600))
			return []byte(`{"success":true}`), nil
		},
	}
	r.onBatch(context.Background(), assessBatch("s-1"), &fakePublisher{})
	res := readResult(t, r.keptDir("s-1", "atomix"))
	require.Equal(t, assessPublished, res.Outcome, "%s %s", res.Reason, res.Detail)
}

// D764+: an ensemble named like another's assessor scope is refused, by
// ape coordinator as by the coordination block; the scope is the one the
// coordinator records under. The control: an ordinary name runs.
func TestCoordinator_RefusesAnAssessorScopeName(t *testing.T) {
	require.Equal(t, "shop"+member.AssessorScopeSuffix, coordinator.AssessorScope("shop"))
	_, _, err := v1Exec(t, newCoordinatorCmd, "--ensemble", "shop-assessor", "--nats-url", "nats://127.0.0.1:1")
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.ErrorIs(t, err, member.ErrReservedEnsemble)
	_, _, err = v1Exec(t, newCoordinatorCmd, "--ensemble", "shop", "--nats-url", "nats://127.0.0.1:1")
	require.Error(t, err, "control: past the name check (no NATS there)")
	require.NotErrorIs(t, err, member.ErrReservedEnsemble)
}

// S8-1 end to end (research's T1): c1's batch publishes its notice to M1
// and c1 stops before recording the batch done; c2 replays the batch, and
// since its answer went out, records it done and runs no session — so the
// new session's different answer (M2) never appears. The control is
// TestAssessRunner_ReplayAfterAKill and §4's kill case: a batch killed
// before any publish runs once more.
func TestAssessor_ReplayAfterAPublishRunsNoSession(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, rebuild.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	holds := func(id string) func() bool {
		return func() bool { h, _, err := ls.Holder(ctx, "coord.ens"); return err == nil && h == id }
	}

	published := make(chan string, 1)
	c1ctx, stop1 := context.WithCancel(ctx)
	c1done := make(chan struct{})
	go func() {
		defer close(c1done)
		_ = coordinator.Run(c1ctx, nc, coordinator.Config{
			Ensemble: "ens", ID: "c1", TTL: 3 * time.Second, Renew: 500 * time.Millisecond, Projects: []string{"atomix"},
			Assessor: &coordinator.AssessorConfig{Quiet: 200 * time.Millisecond, Ceiling: 2 * time.Second, OnBatch: func(_ context.Context, b coordinator.Batch, p coordinator.Publisher) {
				if _, _, err := p.Notices(ctx, b.ID, "atomix", nil, []coordinator.Notice{{Target: "atomix/M1", Reason: "first answer", Evidence: []string{"internal/leader/sweep.go"}}}); err == nil {
					published <- b.ID
				}
				<-c1ctx.Done() // killed before the batch is recorded done
			}},
		})
	}()
	require.Eventually(t, holds("c1"), 10*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	// Of M9, so both answers (M1, then M2) would be valid targets.
	_, err = js.Publish(ctx, "ape.evt.dana.atomix.project.M9.plan-approved", []byte(`{"event":"plan-approved","milestone":"M9","key":"k1"}`), jetstream.WithMsgID("atomix/ck/plan-approved/M9/k1"))
	require.NoError(t, err)
	var batch string
	select {
	case batch = <-published:
	case <-time.After(10 * time.Second):
		t.Fatal("c1 ran no batch")
	}
	stop1()
	<-c1done

	checkout, _ := assessCheckout(t)
	sessions := 0
	r := &assessRunner{
		dir: t.TempDir(), checkouts: map[string]string{"atomix": checkout}, budget: -1, logf: t.Logf,
		session: func(_ context.Context, folder, _ string) ([]byte, error) {
			sessions++
			require.NoError(t, os.WriteFile(filepath.Join(folder, apexv1.AssessNotice), []byte(`{"notices":[{"target":"atomix/M2","reason":"a second answer","evidence":["internal/leader/sweep.go"]}]}`), 0o600))
			return []byte(`{"success":true}`), nil
		},
	}
	ran := make(chan string, 4)
	c2ctx, stop2 := context.WithCancel(ctx)
	defer stop2()
	go func() {
		_ = coordinator.Run(c2ctx, nc, coordinator.Config{
			Ensemble: "ens", ID: "c2", TTL: 3 * time.Second, Renew: 500 * time.Millisecond, Projects: []string{"atomix"},
			Assessor: &coordinator.AssessorConfig{Quiet: 200 * time.Millisecond, Ceiling: 2 * time.Second, OnBatch: func(c context.Context, b coordinator.Batch, p coordinator.Publisher) {
				r.onBatch(c, b, p)
				ran <- b.ID
			}},
		})
	}()
	select {
	case id := <-ran:
		require.Equal(t, batch, id, "c2 replays c1's batch")
	case <-time.After(15 * time.Second):
		t.Fatal("c2 replayed nothing")
	}
	res := readResult(t, r.keptDir(batch, "atomix"))
	require.Equal(t, assessAnswered, res.Outcome)
	require.Zero(t, sessions, "its answer went out: no session")
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	_, err = st.GetLastMsgForSubject(ctx, "ape.evt.coord.ens.coord."+batch+"-atomix-M2.impact-notice")
	require.ErrorIs(t, err, jetstream.ErrMsgNotFound, "the second answer never appears")
	m, err := st.GetLastMsgForSubject(ctx, "ape.evt.coord.ens.coord."+batch+"-atomix-M1.impact-notice")
	require.NoError(t, err)
	require.Contains(t, string(m.Data), "first answer")
	require.Eventually(t, func() bool {
		_, err := st.GetLastMsgForSubject(ctx, "ape.evt.coord.ens-assessor.coord."+batch+".assessor-done")
		return err == nil
	}, 5*time.Second, 100*time.Millisecond, "the batch is recorded done")
}
