//go:build linux || darwin

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// testRig is one embedded server + a registered daemon + a client conn.
type testRig struct {
	cli  *nats.Conn
	d    *Daemon
	cfg  *Config
	base string // ape.svc.<name>.<project-slug>
}

// startRig boots an embedded core server, registers a daemon using apeBin as
// the (fake) child binary, and returns a separate client connection.
func startRig(t *testing.T, apeBin string) *testRig {
	t.Helper()
	url := natstest.RunServer(t)

	root := gitRepo(t, t.TempDir())
	cfg := &Config{ProjectRoot: root, Allow: []string{root}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}

	daemonConn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("daemon connect: %v", err)
	}
	t.Cleanup(func() { _ = daemonConn.Drain() })

	d, err := NewDaemon(context.Background(), DaemonConfig{Name: "ape", Config: cfg, Conn: daemonConn, ApeBin: apeBin, ApeVersion: "0.0.0", NatsURL: url})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	svc, err := micro.AddService(daemonConn, micro.Config{Name: "ape", Version: "0.0.0"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if err := d.Register(svc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { d.KillAll(); _ = svc.Stop() }) // reap any sleeper children

	cli, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cli.Drain() })

	return &testRig{cli: cli, d: d, cfg: cfg, base: "ape.svc.ape." + eventing.ProjectSlug(root)}
}

func (r *testRig) req(t *testing.T, endpoint string, payload any) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msg, err := r.cli.Request(r.base+"."+endpoint, data, 3*time.Second)
	if err != nil {
		t.Fatalf("request %s: %v", endpoint, err)
	}
	return msg
}

func errCode(m *nats.Msg) string { return m.Header.Get(micro.ErrorCodeHeader) }

func TestDiscovery(t *testing.T) {
	r := startRig(t, "/bin/true")

	if _, err := r.cli.Request("$SRV.PING", nil, 3*time.Second); err != nil {
		t.Fatalf("$SRV.PING: %v", err)
	}

	msg, err := r.cli.Request("$SRV.INFO", nil, 3*time.Second)
	if err != nil {
		t.Fatalf("$SRV.INFO: %v", err)
	}
	var info micro.Info
	if err := json.Unmarshal(msg.Data, &info); err != nil {
		t.Fatalf("unmarshal info: %v", err)
	}
	if len(info.Endpoints) != 9 {
		t.Fatalf("endpoint count = %d, want 9", len(info.Endpoints))
	}
	subjects := map[string]bool{}
	for _, e := range info.Endpoints {
		subjects[e.Subject] = true
	}
	for _, want := range []string{"pipeline.run", "task.run", "prompt.run", "script.run", "job.status", "job.list", "job.stop", "status", "health"} {
		if !subjects[r.base+"."+want] {
			t.Errorf("INFO missing endpoint subject %s.%s", r.base, want)
		}
	}
}

func TestRejectionCodes(t *testing.T) {
	r := startRig(t, "/bin/true")
	root := r.cfg.ProjectRoot

	// Malformed JSON.
	if m, err := r.cli.Request(r.base+".pipeline.run", []byte("{not json"), 3*time.Second); err != nil {
		t.Fatalf("req: %v", err)
	} else if errCode(m) != CodeValidation {
		t.Errorf("malformed: code = %q, want VALIDATION", errCode(m))
	}

	tests := []struct {
		name     string
		endpoint string
		payload  RunRequest
		want     string
	}{
		{"missing project_root", "pipeline.run", RunRequest{Pipeline: "p"}, CodeValidation},
		{"missing pipeline name", "pipeline.run", RunRequest{ProjectRoot: root}, CodeValidation},
		{"missing skill", "task.run", RunRequest{ProjectRoot: root}, CodeValidation},
		{"project not allowed", "pipeline.run", RunRequest{ProjectRoot: "/not/allowed", Pipeline: "p"}, CodeProjectNotAllowed},
		{"prompt unavailable", "prompt.run", RunRequest{ProjectRoot: root, Prompt: "hi"}, CodeValidation},
		{"script unavailable", "script.run", RunRequest{ProjectRoot: root, ScriptPath: "x.star"}, CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := r.req(t, tc.endpoint, tc.payload)
			if got := errCode(m); got != tc.want {
				t.Fatalf("code = %q, want %q (desc: %q)", got, tc.want, m.Header.Get(micro.ErrorHeader))
			}
		})
	}
}

func TestJobNotFound(t *testing.T) {
	r := startRig(t, "/bin/true")
	for _, ep := range []string{"job.status", "job.stop"} {
		m := r.req(t, ep, JobIDRequest{JobID: "nope"})
		if errCode(m) != CodeNotFound {
			t.Errorf("%s unknown id: code = %q, want NOT_FOUND", ep, errCode(m))
		}
	}
}

func TestDispatchLifecycle(t *testing.T) {
	// Fast child that echoes the injected job id to its (logged) stdout.
	bin := fakeApe(t, `echo "jobid=$APE_JOB_ID"
exit 0
`)
	r := startRig(t, bin)

	// Subscribe to the event stream before submitting so we can assert the
	// daemon publishes svc lifecycle events carrying the job id.
	evCh := make(chan *nats.Msg, 64)
	sub, err := r.cli.Subscribe("ape.evt.>", func(msg *nats.Msg) { evCh <- msg })
	if err != nil {
		t.Fatalf("subscribe events: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	_ = r.cli.Flush()

	m := r.req(t, "task.run", RunRequest{ProjectRoot: r.cfg.ProjectRoot, Skill: "apex-shard-doc"})
	if c := errCode(m); c != "" {
		t.Fatalf("task.run rejected: %s / %s", c, m.Header.Get(micro.ErrorHeader))
	}
	var reply RunReply
	if err := json.Unmarshal(m.Data, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if !reply.Accepted || reply.JobID == "" {
		t.Fatalf("reply = %+v, want accepted with a job id", reply)
	}

	// The daemon publishes job-accepted then job-end on
	// ape.evt.<user>.<project>.svc.<job_id>.<event>, both carrying run_id.
	waitForSvcEvent(t, evCh, reply.JobID, "job-accepted")
	waitForSvcEvent(t, evCh, reply.JobID, "job-end")

	info := pollJobTerminal(t, r, reply.JobID)
	if info.State != StateDone {
		t.Fatalf("state = %q, want done", info.State)
	}
	if info.ExitCode == nil || *info.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", info.ExitCode)
	}

	// The child received APE_JOB_ID (proves the daemon injection → child
	// events carry the job id).
	logPath := filepath.Join(r.cfg.ProjectRoot, "_output", "ape", "service", reply.JobID+".log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read job log: %v", err)
	}
	if !strings.Contains(string(data), "jobid="+reply.JobID) {
		t.Fatalf("job log missing injected APE_JOB_ID:\n%s", data)
	}

	// Key released after exit.
	if held := r.d.adm.HeldKeys(); len(held) != 0 {
		t.Fatalf("key not released after job end: %v", held)
	}
}

func TestExclusivityOverNATS(t *testing.T) {
	bin := fakeApe(t, "sleep 5\n") // holds its key until stopped
	r := startRig(t, bin)
	root := r.cfg.ProjectRoot

	// Exclusive job on the default key → accepted.
	jobA := acceptedJobID(t, r, "task.run", RunRequest{ProjectRoot: root, Skill: "s"})

	// Second exclusive on the same key → BUSY_EXCLUSIVE.
	if m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "s"}); errCode(m) != CodeBusyExclusive {
		t.Errorf("2nd exclusive same key: code = %q, want BUSY_EXCLUSIVE", errCode(m))
	}
	// Nonexclusive into held-exclusive → BUSY_EXCLUSIVE.
	if m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "s", Nonexclusive: true}); errCode(m) != CodeBusyExclusive {
		t.Errorf("nonexclusive into held-exclusive: code = %q, want BUSY_EXCLUSIVE", errCode(m))
	}
	// Different key → accepted (keys independent).
	if m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "s", ExclusivityKey: "chore"}); errCode(m) != "" {
		t.Errorf("different key rejected: %s", errCode(m))
	}
	// Held-shared then exclusive → BUSY_KEY.
	if m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "s", Nonexclusive: true, ExclusivityKey: "shared"}); errCode(m) != "" {
		t.Fatalf("first shared rejected: %s", errCode(m))
	}
	if m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "s", ExclusivityKey: "shared"}); errCode(m) != CodeBusyKey {
		t.Errorf("exclusive into held-shared: code = %q, want BUSY_KEY", errCode(m))
	}

	// Stop job A → default key frees → a new exclusive job is accepted.
	if m := r.req(t, "job.stop", JobIDRequest{JobID: jobA}); errCode(m) != "" {
		t.Fatalf("job.stop rejected: %s", errCode(m))
	}
	info := pollJobTerminal(t, r, jobA)
	if info.State != StateStopped {
		t.Fatalf("stopped job state = %q, want stopped", info.State)
	}
	if m := r.req(t, "task.run", RunRequest{ProjectRoot: root, Skill: "s"}); errCode(m) != "" {
		t.Errorf("exclusive after stop rejected: %s", errCode(m))
	}
}

func TestStatusAndHealth(t *testing.T) {
	r := startRig(t, "/bin/true")

	var st StatusReply
	if err := json.Unmarshal(r.req(t, "status", struct{}{}).Data, &st); err != nil {
		t.Fatalf("status unmarshal: %v", err)
	}
	if st.Name != "ape" || st.ProjectRoot != r.cfg.ProjectRoot || len(st.Allowlist) == 0 {
		t.Fatalf("status = %+v", st)
	}
	if st.Versions.Ape != "0.0.0" {
		t.Errorf("status ape version = %q, want 0.0.0", st.Versions.Ape)
	}

	var h HealthReply
	if err := json.Unmarshal(r.req(t, "health", struct{}{}).Data, &h); err != nil {
		t.Fatalf("health unmarshal: %v", err)
	}
	// project_root exists here, so that check must pass regardless of the
	// host's claude/disk state.
	if !h.Checks["project_root"] {
		t.Fatalf("health project_root check should pass: %+v", h.Checks)
	}
	if _, ok := h.Checks["claude_bin"]; !ok {
		t.Errorf("health should report a claude_bin check")
	}
}

func TestGracefulDrainPrimitives(t *testing.T) {
	bin := fakeApe(t, "sleep 1\n")
	r := startRig(t, bin)
	root := r.cfg.ProjectRoot

	jobID := acceptedJobID(t, r, "task.run", RunRequest{ProjectRoot: root, Skill: "s"})

	// Begin drain: stop accepting new work.
	if err := r.d.StopAccepting(); err != nil {
		t.Fatalf("StopAccepting: %v", err)
	}
	// New requests are no longer served (no responder). Poll briefly in case
	// the server-side unsubscribe settles just after StopAccepting returns.
	payload := mustJSON(t, RunRequest{ProjectRoot: root, Skill: "s"})
	rejected := false
	for range 40 {
		if _, err := r.cli.Request(r.base+".task.run", payload, 200*time.Millisecond); err != nil {
			rejected = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !rejected {
		t.Fatal("expected new requests to be rejected after StopAccepting")
	}

	// In-flight job finishes; WaitJobs returns once it does.
	done := make(chan struct{})
	go func() { r.d.WaitJobs(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitJobs did not return after the in-flight job finished")
	}
	if info, _ := r.d.reg.Get(jobID); info.State != StateDone {
		t.Fatalf("drained job state = %q, want done", info.State)
	}
}

// TestRunServesAndDrains drives the full Run entry point against an embedded
// server: it loads config from disk, connects, registers the micro service,
// serves, and drains cleanly when the context is cancelled (the SIGTERM
// path's ctx-cancellation twin). This is the PLAN-14 acceptance for the
// top-level daemon lifecycle.
func TestRunServesAndDrains(t *testing.T) {
	url := natstest.RunServer(t)

	root := gitRepo(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "_apex"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("project_root: %s\nallow:\n  - %s\n", root, root)
	if err := os.WriteFile(filepath.Join(root, "_apex", "service.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{
			Name:        "ape",
			ProjectRoot: root,
			NatsURL:     url,
			ApeVersion:  "0.0.0",
			Stderr:      io.Discard,
		})
	}()

	// Wait for the service to register, then confirm discovery works.
	cli, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cli.Drain() }()

	// Poll the real status endpoint until it answers — this proves both
	// discovery and the composed endpoint subjects are live.
	base := "ape.svc.ape." + eventing.ProjectSlug(root)
	deadline := time.Now().Add(5 * time.Second)
	var up bool
	for time.Now().Before(deadline) {
		if _, err := cli.Request(base+".status", []byte("{}"), 200*time.Millisecond); err == nil {
			up = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !up {
		cancel()
		t.Fatal("service did not become ready within 5s")
	}
	// $SRV discovery answers too.
	if _, err := cli.Request("$SRV.PING.ape", nil, 2*time.Second); err != nil {
		cancel()
		t.Fatalf("$SRV.PING: %v", err)
	}

	// Cancelling the context triggers the graceful drain; Run returns nil.
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run returned %v, want nil after clean drain", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// waitForSvcEvent asserts a svc-kind lifecycle event for jobID with the given
// event token arrives on the stream, on the right subject and carrying
// run_id == jobID in its payload.
func waitForSvcEvent(t *testing.T, evCh <-chan *nats.Msg, jobID, event string) {
	t.Helper()
	wantSuffix := ".svc." + jobID + "." + event
	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-evCh:
			if !strings.HasSuffix(msg.Subject, wantSuffix) {
				continue
			}
			var payload struct {
				RunID string `json:"run_id"` //nolint:tagliatelle // snake_case matches the NATS wire payload
				Event string `json:"event"`
			}
			if err := json.Unmarshal(msg.Data, &payload); err != nil {
				t.Fatalf("event %s: bad payload: %v", msg.Subject, err)
			}
			if payload.RunID != jobID || payload.Event != event {
				t.Fatalf("event %s: run_id=%q event=%q, want %q/%q", msg.Subject, payload.RunID, payload.Event, jobID, event)
			}
			return
		case <-deadline:
			t.Fatalf("did not receive svc %q event for job %s within 3s", event, jobID)
		}
	}
}

// pollJobTerminal polls job.status until the job leaves the running state.
func pollJobTerminal(t *testing.T, r *testRig, jobID string) JobInfo {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var reply JobStatusReply
		if err := json.Unmarshal(r.req(t, "job.status", JobIDRequest{JobID: jobID}).Data, &reply); err != nil {
			t.Fatalf("job.status unmarshal: %v", err)
		}
		if reply.State != StateRunning {
			return reply.JobInfo
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s never left running state", jobID)
	return JobInfo{}
}

func acceptedJobID(t *testing.T, r *testRig, endpoint string, payload RunRequest) string {
	t.Helper()
	m := r.req(t, endpoint, payload)
	if c := errCode(m); c != "" {
		t.Fatalf("%s rejected: %s / %s", endpoint, c, m.Header.Get(micro.ErrorHeader))
	}
	var reply RunReply
	if err := json.Unmarshal(m.Data, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if !reply.Accepted {
		t.Fatalf("%s not accepted", endpoint)
	}
	return reply.JobID
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
