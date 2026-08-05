//go:build linux || darwin

package aped

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go"
)

// natsMsg builds the message shape the heartbeat subscriber receives.
func natsMsg(subject, payload string) *nats.Msg {
	return &nats.Msg{Subject: subject, Data: []byte(payload)}
}

// reapRig is a reaper wired to the shared vmm fake with a frozen clock. It does
// not run the NATS subscription — heartbeats are fed in directly, so the
// decision logic is tested without a server in the loop.
type reapRig struct {
	r    *Reaper
	be   *fakeBackend
	log  *strings.Builder
	now  time.Time
	name string
}

func newReapRig(t *testing.T, idleAfter time.Duration, ws workspace.Workspace) *reapRig {
	t.Helper()
	be := newFakeBackend()
	if _, err := be.Create(context.Background(), workspace.CreateRequest{Name: ws.Name}); err != nil {
		t.Fatal(err)
	}
	if err := be.Start(context.Background(), ws.Name); err != nil {
		t.Fatal(err)
	}
	be.idleStop = ws.IdleStop

	rig := &reapRig{be: be, log: &strings.Builder{}, now: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC), name: ws.Name}
	rig.r = NewReaper(ReaperConfig{
		Backend:   be,
		IdleAfter: idleAfter,
		Stderr:    rig.log,
		now:       func() time.Time { return rig.now },
	})
	return rig
}

// beat records a heartbeat as of the rig's current clock.
func (rig *reapRig) beat(busy bool) {
	rig.r.mu.Lock()
	defer rig.r.mu.Unlock()
	st := rig.r.seen[VMToken(rig.name)]
	st.LastSeen = rig.now
	st.Last = sandbox.Heartbeat{V: 1, Busy: busy, CPUPercent: 0.5, UptimeSeconds: 60}
	if busy {
		st.LastBusy = rig.now
		st.Last.CPUPercent = 80
		st.Last.Top = []string{"go"}
	}
	rig.r.seen[VMToken(rig.name)] = st
}

func (rig *reapRig) advance(d time.Duration) { rig.now = rig.now.Add(d) }

func (rig *reapRig) state(t *testing.T) workspace.State {
	t.Helper()
	st, err := rig.be.Inspect(context.Background(), rig.name)
	if err != nil {
		t.Fatal(err)
	}
	return st.State
}

// THE TEST THE PLAN NAMES. A workspace whose agent never started emits nothing,
// and nothing is indistinguishable from idle unless the reaper is written to
// distinguish it. Reading silence as idleness would stop every workspace with a
// broken or absent agent — worse than the problem the reaper solves, and silent.
func TestReaperNeverSeenAHeartbeatMeansUnknownNotIdle(t *testing.T) {
	rig := newReapRig(t, 2*time.Hour, workspace.Workspace{Name: "dev"})

	// Far past the threshold, with no heartbeat ever recorded.
	rig.advance(30 * 24 * time.Hour)
	rig.r.evaluate(context.Background())

	if got := rig.state(t); got != workspace.StateRunning {
		t.Fatalf("state = %s, want running — a workspace with no heartbeat must NEVER be reaped", got)
	}
	if rig.log.Len() != 0 {
		t.Errorf("the reaper logged something for an unknown workspace: %q", rig.log.String())
	}
}

// The same rule at the other end: an agent that WAS reporting and went quiet is
// also unknown. It may have crashed while its workspace kept working.
func TestReaperSilentAgentIsUnknownNotIdle(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	rig.beat(false) // reporting, idle
	// The agent stops reporting, and then a long time passes.
	rig.advance(6 * time.Hour)
	rig.r.evaluate(context.Background())

	if got := rig.state(t); got != workspace.StateRunning {
		t.Fatalf("state = %s, want running — an agent that went silent is unknown, not idle", got)
	}
}

func TestReaperStopsAKnownIdleWorkspace(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	// Busy once, then reporting idle continuously past the threshold.
	rig.beat(true)
	for range 10 {
		rig.advance(10 * time.Minute)
		rig.beat(false)
	}
	rig.r.evaluate(context.Background())

	if got := rig.state(t); got != workspace.StateStopped {
		t.Fatalf("state = %s, want stopped after %s idle", got, time.Hour)
	}
	// Stopped, never destroyed: the workspace is still there to start again.
	if _, err := rig.be.Inspect(context.Background(), "dev"); err != nil {
		t.Errorf("the workspace was destroyed, not stopped: %v", err)
	}
	// Every stop is logged with the evidence that triggered it — a stop with no
	// explanation reads as a crash to whoever finds the workspace down.
	log := rig.log.String()
	for _, want := range []string{"stopped dev", "idle for", "threshold 1h0m0s", "last heartbeat", "ape sandbox start dev"} {
		if !strings.Contains(log, want) {
			t.Errorf("the stop log is missing %q:\n%s", want, log)
		}
	}
}

func TestReaperLeavesABusyWorkspaceAlone(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	// A long build: reporting busy the whole time, nobody attached.
	for range 20 {
		rig.advance(10 * time.Minute)
		rig.beat(true)
	}
	rig.r.evaluate(context.Background())

	if got := rig.state(t); got != workspace.StateRunning {
		t.Fatalf("state = %s, want running — this is the exact case last_used_at got wrong", got)
	}
}

// A workspace that has been up and quiet since it started (never once busy) IS
// idle. Requiring a busy sample first would exempt it forever.
func TestReaperReapsAWorkspaceThatWasNeverBusy(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	rig.r.mu.Lock()
	rig.r.seen[VMToken("dev")] = heartbeatState{
		LastSeen: rig.now,
		Last:     sandbox.Heartbeat{V: 1, Busy: false, UptimeSeconds: int64((3 * time.Hour).Seconds())},
	}
	rig.r.mu.Unlock()

	rig.r.evaluate(context.Background())
	if got := rig.state(t); got != workspace.StateStopped {
		t.Fatalf("state = %s, want stopped — 3h of uptime with no work is idle", got)
	}
}

// The per-workspace opt-out.
func TestReaperHonoursTheWorkspaceOptOut(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev", IdleStop: sandbox.IdleStopOff})
	rig.beat(false)
	rig.advance(10 * time.Hour)
	rig.beat(false)
	rig.r.evaluate(context.Background())

	if got := rig.state(t); got != workspace.StateRunning {
		t.Fatalf("state = %s, want running — this workspace set idle_stop: off", got)
	}
}

// A per-workspace threshold narrower than the node's.
func TestReaperHonoursAShorterWorkspaceThreshold(t *testing.T) {
	rig := newReapRig(t, 8*time.Hour, workspace.Workspace{Name: "dev", IdleStop: "30m"})
	rig.beat(true)
	for range 4 {
		rig.advance(10 * time.Minute)
		rig.beat(false)
	}
	rig.r.evaluate(context.Background())

	if got := rig.state(t); got != workspace.StateStopped {
		t.Fatalf("state = %s, want stopped — this workspace asked for 30m", got)
	}
}

// A node with the reaper off must not act, whatever a workspace asks for.
func TestReaperDisabledNodeNeverStops(t *testing.T) {
	rig := newReapRig(t, 0, workspace.Workspace{Name: "dev", IdleStop: "1m"})
	rig.beat(false)
	rig.advance(10 * time.Hour)
	rig.beat(false)
	rig.r.evaluate(context.Background())

	if got := rig.state(t); got != workspace.StateRunning {
		t.Fatalf("state = %s, want running — a node with --idle-stop 0 stops nothing", got)
	}
}

// After a stop, the reaper must forget what it knew. Otherwise a `start` would
// be met with the idleness that got the workspace stopped and it would be
// stopped again before its agent said a word.
func TestReaperForgetsAfterStopping(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	rig.beat(true)
	for range 10 {
		rig.advance(10 * time.Minute)
		rig.beat(false)
	}
	rig.r.evaluate(context.Background())

	if _, _, known := rig.r.Observed("dev"); known {
		t.Error("the reaper kept a stopped workspace's idle history; a restart would be reaped immediately")
	}
}

// A stopped workspace is skipped: the registry says a workspace exists, not that
// it holds memory, and "reaped" a workspace that was already down would be a
// false record of memory reclaimed.
func TestReaperSkipsAnAlreadyStoppedWorkspace(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	rig.beat(true)
	for range 10 {
		rig.advance(10 * time.Minute)
		rig.beat(false)
	}
	if err := rig.be.Stop(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	rig.log.Reset()
	rig.r.evaluate(context.Background())

	if strings.Contains(rig.log.String(), "stopped dev") {
		t.Errorf("the reaper claimed to stop an already-stopped workspace:\n%s", rig.log.String())
	}
}

// The subject is what the NATS server authorized; the payload is a claim. A
// guest must not be able to get ANOTHER workspace stopped by putting its name in
// a heartbeat body.
func TestReaperAttributesHeartbeatsBySubjectNotPayload(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	// A compromised "evil" workspace claiming to be "dev", published (as the server
	// would only ever allow) on its OWN subject.
	rig.r.onHeartbeat(natsMsg(sandbox.HeartbeatSubject(VMToken("evil")),
		`{"v":1,"workspace":"vm-dev","busy":true}`))

	if _, _, known := rig.r.Observed("dev"); known {
		t.Error("a heartbeat was attributed to the workspace its PAYLOAD named, not the one its subject did")
	}
	if _, _, known := rig.r.Observed("evil"); !known {
		t.Error("the heartbeat was not attributed to the workspace whose subject carried it")
	}
}

func TestReaperIgnoresMalformedHeartbeats(t *testing.T) {
	rig := newReapRig(t, time.Hour, workspace.Workspace{Name: "dev"})
	rig.r.onHeartbeat(natsMsg("ape.metrics.vm-dev.cost", `{"v":1}`))     // not a heartbeat subject
	rig.r.onHeartbeat(natsMsg(sandbox.HeartbeatSubject("vm-dev"), `{{`)) // not JSON
	if _, _, known := rig.r.Observed("dev"); known {
		t.Error("a malformed or off-subject message was recorded as a heartbeat")
	}
}
