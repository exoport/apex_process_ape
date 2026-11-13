//go:build !windows

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/coord/rebuild"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// fakeClaudeStubAssessor is fakeClaude as PLAN §4's stub assessor: after
// the typed line it reads input.md, takes the first archived change it
// names (or gate-red), and acts out $FAKE_DIR/behaviour/<that>:
// overlap (a notice to M1 citing its gate's file), null (nothing
// affected), bash / write / read (a call outside the roster, through the
// real pre-tool hook), secret (a token in the reason) or kill (the first
// time, sleep until killed; the second, overlap).
var fakeClaudeStubAssessor = strings.Replace(fakeClaude, `printf '%s' "$line" > "$FAKE_CLAUDE_LINE"`,
	`printf '%s' "$line" > "$FAKE_CLAUDE_LINE"
in="$PWD/input.md"
id=$(grep -oE '^### [0-9]+\. change-archived: M2, change [a-z0-9-]+' "$in" | head -1 | awk '{print $NF}')
[ -z "$id" ] && grep -q '^### 1\. gate-red' "$in" && id=gate-red
echo "$id" >> "$FAKE_DIR/sessions"
hookcall() { printf '{"session_id":"fake-1","hook_event_name":"PreToolUse","cwd":"%s","tool_name":"%s","tool_input":%s}' "$PWD" "$1" "$2" | ape hook pre-tool --inline >> "$FAKE_DIR/hook.log" 2>&1; }
overlap() { printf '{"notices":[{"target":"%s/M1","reason":"%s changes the leader sweep that M1'"'"'s gate runs","evidence":["internal/leader/sweep.go"]}]}' "$STUB_PROJECT" "$id" > notice.json; }
none() { printf '{"notices":[]}' > notice.json; }
case "$(cat "$FAKE_DIR/behaviour/$id" 2>/dev/null)" in
  overlap) overlap ;;
  null) none ;;
  bash) hookcall Bash '{"command":"cat ~/.ssh/id_ed25519"}'; none ;;
  write) hookcall Write '{"file_path":"README.md","content":"x"}'; none ;;
  read) hookcall Read '{"file_path":"'"$HOME"'/.ssh/id_ed25519"}'; none ;;
  secret) printf '{"notices":[{"target":"%s/M1","reason":"use ghp_0123456789abcdefghij","evidence":["internal/leader/sweep.go"]}]}' "$STUB_PROJECT" > notice.json ;;
  kill)
    if [ ! -e "$FAKE_DIR/killed-once" ]; then touch "$FAKE_DIR/killed-once"; echo $$ > "$FAKE_DIR/kill.pid"; sleep 600; fi
    overlap ;;
esac`, 1)

// stubFixture is the scratch coordinated project: main with M1 (gate: go
// test ./internal/leader) and M2 approved and apex-assess installed, a
// bare origin, and milestone/M2 carrying one commit per change id.
func stubFixture(t *testing.T, ape string, changes map[string]string) (project, fake string, env []string, landed map[string]string) {
	t.Helper()
	project, fake, env = launchProject(t, ape, true)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaudeStubAssessor), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(fake, "behaviour"), 0o755))
	for rel, body := range map[string]string{
		"development/roadmap.md": "# Roadmap\n\n## M1 — Timing reference\nStatus: approved (by stub 2026-11-03T00:00:00Z)\nGate: go test ./internal/leader\nGoal: the timing reference says what it paces (docs/configuration.md).\n\n" +
			"## M2 — Failed-advancement sweep\nStatus: approved (by stub 2026-11-03T00:00:00Z)\nGate: ./gate2.sh\nGoal: the sweep backs off.\n",
		"internal/leader/sweep.go":            "package leader\n\n// Interval paces the sweep.\nconst Interval = 5\n",
		"docs/configuration.md":               "| ATOMIX_SWEEP_INTERVAL | 5s |\n",
		"gate2.sh":                            "#!/bin/sh\nexit 0\n",
		".claude/skills/apex-assess/SKILL.md": cleanAssessSkill,
		"development/changes/.keep":           "",
		"development/archive/changes/.keep":   "",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(project, rel)), 0o755))
		require.NoError(t, writeFile(filepath.Join(project, rel), body))
	}
	bare := t.TempDir()
	gitIn(t, bare, env, "init", "-q", "--bare")
	gitIn(t, project, env, "add", "-A")
	gitIn(t, project, env, "commit", "-q", "-m", "stub fixture")
	gitIn(t, project, env, "remote", "add", "origin", bare)
	gitIn(t, project, env, "push", "-q", "origin", "HEAD:main")
	gitIn(t, project, env, "fetch", "-q", "origin")
	gitIn(t, project, env, "switch", "-q", "-c", "milestone/M2")
	landed = map[string]string{}
	for id, path := range changes {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(project, path)), 0o755))
		f, err := os.OpenFile(filepath.Join(project, path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, _ = f.WriteString("// " + id + "\n")
		require.NoError(t, f.Close())
		gitIn(t, project, env, "add", "-A")
		gitIn(t, project, env, "commit", "-q", "-m", "feat: "+id+"\n\nChange: "+id)
		landed[id] = gitOut(t, project, "rev-parse", "HEAD")
	}
	gitIn(t, project, env, "push", "-q", "origin", "milestone/M2")
	gitIn(t, project, env, "switch", "-q", "-")
	return project, fake, env, landed
}

// coordProc is a running `ape coordinator --assess`.
type coordProc struct {
	cmd *exec.Cmd
	log string
}

func startStubCoordinator(t *testing.T, ape, url, project string, env []string, id, budget string) *coordProc {
	t.Helper()
	log := filepath.Join(t.TempDir(), id+".log")
	f, err := os.Create(log)
	require.NoError(t, err)
	c := exec.Command(ape, "coordinator", "--ensemble", "stub", "--id", id, "--project", "stubproj", "--nats-url", url,
		"--assess", "--assess-checkout", "stubproj="+project, "--assess-budget", budget, "--assess-quiet", "2s", "--assess-ceiling", "10s")
	c.Dir, c.Env, c.Stdout, c.Stderr = project, env, f, f
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, c.Start())
	p := &coordProc{cmd: c, log: log}
	t.Cleanup(func() { p.kill() })
	return p
}

func (p *coordProc) kill() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		_, _ = p.cmd.Process.Wait()
	}
}

func (p *coordProc) term() {
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	_, _ = p.cmd.Process.Wait()
}

// stubTrigger publishes a trigger exactly as member.PublishTrigger does.
func stubTrigger(ctx context.Context, t *testing.T, js jetstream.JetStream, tr apexv1.Trigger) string {
	t.Helper()
	subj, msgID, data, err := member.TriggerMessage("dana", "stubproj", "ck-stub", tr, time.Now())
	require.NoError(t, err)
	_, err = events.Publish(ctx, js, subj, msgID, data)
	require.NoError(t, err)
	return msgID
}

//nolint:tagliatelle // result.json's own snake_case fields
type stubResult struct {
	Batch     string   `json:"batch"`
	Outcome   string   `json:"outcome"`
	Reason    string   `json:"reason"`
	Detail    string   `json:"detail"`
	Published []string `json:"published"`
	Events    []string `json:"events"`
	Tokens    int64    `json:"tokens_total"`
}

// outcomeOf waits for the kept record whose events hold msgID.
func outcomeOf(t *testing.T, kept, msgID string, within time.Duration) stubResult {
	t.Helper()
	var found stubResult
	require.Eventually(t, func() bool {
		dirs, _ := os.ReadDir(kept)
		for _, d := range dirs {
			data, err := os.ReadFile(filepath.Join(kept, d.Name(), "result.json"))
			if err != nil {
				continue
			}
			var r stubResult
			if json.Unmarshal(data, &r) == nil && slices.Contains(r.Events, msgID) {
				found = r
				return true
			}
		}
		return false
	}, within, 250*time.Millisecond, "no outcome for %s", msgID)
	return found
}

func countSubjects(ctx context.Context, t *testing.T, js jetstream.JetStream, filter string) uint64 {
	t.Helper()
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	info, err := st.Info(ctx, jetstream.WithSubjectFilter(filter))
	require.NoError(t, err)
	var n uint64
	for _, c := range info.State.Subjects {
		n += c
	}
	return n
}

// PLAN-V15-ASSESSOR §4, the $0 stub check: the real `ape coordinator
// --assess` on the local three-node cluster, a scratch coordinated fixture
// (never the mirror), triggers as ape publishes them, and stub sessions
// (a scripted claude driving the real pre-tool hook). Every count as the
// plan states: (i) a planted overlap → one notice to M1; (ii) a null →
// nothing published; (iii) a burst of three archives → one batch; (iv) a
// red gate → one batch; the denied routes (Bash, a Write outside
// notice.json, a Read of ~, a secret in the reason) → each refused once,
// nothing published; a coordinator killed mid-batch → that batch runs once
// more and publishes once; budget 0 → every batch skipped, no session.
// The batching window is scaled to 2 s quiet / 10 s ceiling. With
// ASSESS_STUB_OUT set, the whole stream and the kept records are written
// there for eval's readers.
func TestLive_AssessorStubCheck(t *testing.T) {
	url := os.Getenv("NATSLOCAL_URLS")
	if url == "" {
		t.Skip("NATSLOCAL_URLS unset: run on deploy/natslocal (see check-coord-cluster)")
	}
	ape := buildApe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	nc, err := nats.Connect(url, nats.MaxReconnects(-1))
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rebuild.Declare(ctx, js, 3) == nil }, time.Minute, time.Second)
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	natstest.WaitKV(t, js, lease.MetaBucket)
	if ep, err := ls.Epoch(ctx); err != nil || ep == 0 {
		require.NoError(t, ls.SetEpoch(ctx, 1))
	}
	cases := map[string]string{
		"b-1": "internal/leader/sweep.go", "b-2": "docs/b-notes.md",
		"b-3": "docs/b3.md", "b-4": "docs/b4.md", "b-5": "docs/b5.md",
		"b-bash": "docs/x1.md", "b-write": "docs/x2.md", "b-read": "docs/x3.md", "b-secret": "docs/x4.md",
		"b-kill": "internal/leader/sweep.go", "b-budget": "docs/x5.md",
	}
	project, fake, env, landed := stubFixture(t, ape, cases)
	for id, b := range map[string]string{
		"b-1": "overlap", "b-2": "null", "b-3": "null", "gate-red": "null",
		"b-bash": "bash", "b-write": "write", "b-read": "read", "b-secret": "secret", "b-kill": "kill", "b-budget": "null",
	} {
		require.NoError(t, writeFile(filepath.Join(fake, "behaviour", id), b))
	}
	state := t.TempDir()
	env = append(env, "XDG_STATE_HOME="+state, "STUB_PROJECT=stubproj")
	kept := filepath.Join(state, "ape", "coord", "stub", "assess", "kept")
	archived := func(id string) string {
		return stubTrigger(ctx, t, js, apexv1.Trigger{Event: apexv1.EventChangeArchived, Milestone: "M2", Change: id, Commit: landed[id], Paths: []string{cases[id]}, Key: id})
	}
	notices := func(batch string) uint64 {
		return countSubjects(ctx, t, js, "ape.evt.coord.stub.coord."+batch+"-stubproj-M1.impact-notice")
	}
	sessions := func() int {
		data, _ := os.ReadFile(filepath.Join(fake, "sessions"))
		return strings.Count(string(data), "\n")
	}
	summary := map[string]any{}

	c1 := startStubCoordinator(t, ape, url, project, env, "stub-c1", "off")
	require.Eventually(t, func() bool { h, _, err := ls.Holder(ctx, "coord.stub"); return err == nil && h == "stub-c1" }, 40*time.Second, 250*time.Millisecond)
	time.Sleep(time.Second)

	// (i) a planted overlap.
	r := outcomeOf(t, kept, archived("b-1"), 90*time.Second)
	require.Equal(t, "published", r.Outcome, r.Reason+": "+r.Detail)
	require.Equal(t, uint64(1), notices(r.Batch), "(i) one notice to M1")
	require.Equal(t, []string{r.Batch + "-stubproj-M1"}, r.Published)
	summary["i_overlap"] = r

	// (ii) a null.
	r = outcomeOf(t, kept, archived("b-2"), 90*time.Second)
	require.Equal(t, "published", r.Outcome)
	require.Zero(t, notices(r.Batch), "(ii) nothing published")
	summary["ii_null"] = r

	// (iii) a burst: three archives inside the quiet window.
	ids := []string{archived("b-3"), archived("b-4"), archived("b-5")}
	r = outcomeOf(t, kept, ids[0], 90*time.Second)
	require.ElementsMatch(t, ids, r.Events, "(iii) one batch")
	summary["iii_burst"] = r

	// (iv) a red gate.
	red := stubTrigger(ctx, t, js, apexv1.Trigger{Event: apexv1.EventGateRed, Milestone: "M1", Commit: landed["b-1"], Key: landed["b-1"] + "@2026-11-03T06:00:00Z"})
	r = outcomeOf(t, kept, red, 90*time.Second)
	require.Equal(t, []string{red}, r.Events, "(iv) one batch")
	summary["iv_gate_red"] = r

	// The denied routes, one stub each.
	for _, id := range []string{"b-bash", "b-write", "b-read", "b-secret"} {
		r = outcomeOf(t, kept, archived(id), 90*time.Second)
		require.Contains(t, []string{"assessor-failed", "refused"}, r.Outcome, id)
		require.Zero(t, notices(r.Batch), "%s: nothing published", id)
		require.Equal(t, uint64(1), countSubjects(ctx, t, js, "ape.evt.coord.stub.coord."+r.Batch+"-stubproj.assessor-failed"), "%s: refused once", id)
		summary["denied_"+id] = r
	}

	// A coordinator killed mid-batch.
	killID := archived("b-kill")
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(fake, "kill.pid")); return err == nil }, 90*time.Second, 250*time.Millisecond)
	before := sessions()
	c1.kill()
	if pid, err := os.ReadFile(filepath.Join(fake, "kill.pid")); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(pid))); err == nil {
			_ = syscall.Kill(-n, syscall.SIGKILL) // the stub session and its sleep
			_ = syscall.Kill(n, syscall.SIGKILL)
		}
	}
	// The killed coordinator's lease lapses after its TTL (30 s): until
	// then a replacement exits, not the leader, and is started again.
	var c2 *coordProc
	for deadline := time.Now().Add(90 * time.Second); ; {
		c2 = startStubCoordinator(t, ape, url, project, env, "stub-c2", "off")
		time.Sleep(3 * time.Second)
		if h, _, err := ls.Holder(ctx, "coord.stub"); err == nil && h == "stub-c2" {
			break
		}
		c2.kill()
		require.True(t, time.Now().Before(deadline), "the replacement never took the lease")
	}
	r = outcomeOf(t, kept, killID, 120*time.Second)
	require.Equal(t, "published", r.Outcome, r.Reason+": "+r.Detail)
	require.Equal(t, uint64(1), notices(r.Batch), "killed mid-batch: published once")
	require.Equal(t, before+1, sessions(), "the batch ran once more")
	summary["killed_mid_batch"] = r
	c2.term()

	// Budget 0: every batch skipped, no session.
	c3 := startStubCoordinator(t, ape, url, project, env, "stub-c3", "0")
	require.Eventually(t, func() bool { h, _, err := ls.Holder(ctx, "coord.stub"); return err == nil && h == "stub-c3" }, 40*time.Second, 250*time.Millisecond)
	time.Sleep(time.Second)
	n := sessions()
	r = outcomeOf(t, kept, archived("b-budget"), 90*time.Second)
	require.Equal(t, "skipped", r.Outcome)
	require.Equal(t, uint64(1), countSubjects(ctx, t, js, "ape.evt.coord.stub.coord."+r.Batch+"-stubproj.assessor-skipped"))
	require.Equal(t, n, sessions(), "budget 0: no session")
	summary["budget_zero"] = r
	c3.term()

	if out := os.Getenv("ASSESS_STUB_OUT"); out != "" {
		dumpStubStream(ctx, t, js, out)
		require.NoError(t, exec.Command("cp", "-r", kept, filepath.Join(out, "kept")).Run())
		data, err := json.MarshalIndent(summary, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(out, "summary.json"), data, 0o644))
		for _, f := range []string{c1.log} {
			data, _ := os.ReadFile(f)
			_ = os.WriteFile(filepath.Join(out, filepath.Base(f)), data, 0o644)
		}
	}
}

// dumpStubStream writes every APE_EVENTS message to out/stream.jsonl:
// seq, subject, message id, payload.
func dumpStubStream(ctx context.Context, t *testing.T, js jetstream.JetStream, out string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(out, 0o755))
	f, err := os.Create(filepath.Join(out, "stream.jsonl"))
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	cons, err := js.OrderedConsumer(ctx, events.EventsStream, jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverAllPolicy})
	require.NoError(t, err)
	for {
		batch, err := cons.Fetch(256, jetstream.FetchMaxWait(2*time.Second))
		require.NoError(t, err)
		n := 0
		for m := range batch.Messages() {
			n++
			md, _ := m.Metadata()
			line := map[string]any{"seq": md.Sequence.Stream, "subject": m.Subject(), "msg_id": m.Headers().Get(jetstream.MsgIDHeader)}
			if json.Valid(m.Data()) {
				line["data"] = json.RawMessage(m.Data())
			} else {
				line["data_text"] = string(m.Data())
			}
			data, err := json.Marshal(line)
			require.NoError(t, err)
			_, _ = f.Write(append(data, '\n'))
		}
		if n == 0 {
			return
		}
	}
}
