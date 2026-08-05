//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/errdefs"
)

// agentKillSignal terminates a supervised agent when supervision is cancelled.
// SIGTERM, not SIGKILL: the agent's only cleanup is draining its NATS
// connection, and letting it flush a final heartbeat costs nothing.
const agentKillSignal = syscall.SIGTERM

// Launching + supervising the in-guest agent (PLAN-24 D6).
//
// # Why aped launches it, and not the image
//
// PLAN-18 D6 specified a best-effort background launch from the image's
// entrypoint.sh, before `exec "$@"`. That file lives in the SEPARATE public
// ape-sandbox repository, so following it literally would cost a cross-repo
// change, a new image version, a digest re-pin and a policy update before the
// agent could be tested even once — and would leave the agent unsupervised: if
// it died, nothing would notice until the reaper started calling a live
// workspace idle.
//
// Launching it here reverses that decision. It costs the agent starting AFTER
// the workload rather than before (nothing depends on the ordering — the agent
// only observes), and it makes re-launch this daemon's bookkeeping. Revisit the
// entrypoint only if something ever needs the agent running before the workload.
//
// # The gate
//
// The agent starts only when the workspace has BOTH APE_NATS_CREDS and
// APE_NATS_URL. No credentials means no agent and a workspace that boots
// perfectly well without one — the property PLAN-18 D6 wanted, kept.

// agentArgv is the command aped runs inside the guest. The binary is the one
// THIS daemon delivered read-only at /opt/ape/bin (PLAN-23), verified by reading
// its build info, so it matches the daemon by construction.
func agentArgv() []string { return []string{path.Join(ApeBinDest, "ape"), "sandbox-agent"} }

// Agent supervision timing. The backoff is generous because a failing agent is
// not an emergency: the reaper is built to treat a missing heartbeat as unknown,
// so the cost of a slow restart is a workspace that is not auto-stopped, which
// is the safe direction.
const (
	agentRestartDelay = 10 * time.Second
	agentMaxDelay     = 5 * time.Minute
	// agentMaxConsecutiveFailures ends supervision after this many launches that
	// failed to start at all. A workspace whose image or delivered `ape` cannot run
	// the agent will never start it, and retrying forever would churn the guest and
	// the log for nothing.
	agentMaxConsecutiveFailures = 5
)

// agentSupervisor keeps one in-guest agent alive per workspace.
type agentSupervisor struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
	stderr  io.Writer
}

func newAgentSupervisor(stderr io.Writer) *agentSupervisor {
	if stderr == nil {
		stderr = io.Discard
	}
	return &agentSupervisor{running: map[string]context.CancelFunc{}, stderr: stderr}
}

// stop cancels a workspace's agent supervision. Idempotent — called from Stop
// and Destroy, and Destroy after a Stop is normal.
func (s *agentSupervisor) stop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.running[id]; ok {
		cancel()
		delete(s.running, id)
	}
}

// stopAll cancels every supervised agent (driver shutdown).
func (s *agentSupervisor) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cancel := range s.running {
		cancel()
		delete(s.running, id)
	}
}

// startAgent launches and supervises the in-guest agent for a workspace, if that
// workspace has the per-VM credentials the agent needs.
//
// Everything here is best-effort and returns nothing: an agent that will not
// start must never fail the workspace operation that triggered it. A workspace
// with no agent is a workspace whose idleness is unknown, which the reaper
// handles by leaving it alone.
//
// The supervision goroutine deliberately does NOT use the caller's context: it
// outlives the request that started the workspace, and is bound instead to a
// per-workspace context cancelled by stop/destroy.
func (d *containerdDriver) startAgent(id string) {
	if d.agents == nil {
		return
	}
	d.agents.mu.Lock()
	if _, already := d.agents.running[id]; already {
		d.agents.mu.Unlock()
		return // a Start on a running workspace must not stack a second agent
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.agents.running[id] = cancel
	d.agents.mu.Unlock()

	go func() {
		defer d.agents.stop(id)
		d.superviseAgent(ctx, id)
	}()
}

// superviseAgent runs the agent, re-launching it when it dies, until ctx is
// cancelled or the workspace's task is gone.
func (d *containerdDriver) superviseAgent(ctx context.Context, id string) {
	nsctx := d.nsctx(ctx)
	delay := agentRestartDelay
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		code, err := d.runAgentOnce(nsctx, id)
		switch {
		case err == nil:
			// The agent exited on its own. That is either a shutdown we are about to
			// notice via ctx, or a crash — either way, back off and try again.
			failures = 0
			delay = agentRestartDelay
			fmt.Fprintf(d.agents.stderr, "! aped agent %s: exited with code %d; restarting in %s\n", id, code, delay)
		case isAgentTerminal(err):
			// No task, or no credentials: this workspace is not going to run an agent
			// until something else changes. Say so once and stop.
			fmt.Fprintf(d.agents.stderr, "  aped agent %s: not running an agent (%v)\n", id, err)
			return
		default:
			failures++
			if failures >= agentMaxConsecutiveFailures {
				fmt.Fprintf(d.agents.stderr,
					"! aped agent %s: could not start after %d attempts (%v) — giving up. "+
						"This workspace's idleness is UNKNOWN, so it will not be auto-stopped.\n",
					id, failures, err)
				return
			}
			fmt.Fprintf(d.agents.stderr, "! aped agent %s: launch failed (%v); retrying in %s\n", id, err, delay)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > agentMaxDelay {
			delay = agentMaxDelay
		}
	}
}

// errNoAgentCreds means the workspace was provisioned without per-VM credentials,
// so it runs no agent by design.
var errNoAgentCreds = errors.New("workspace has no per-VM credentials")

// isAgentTerminal reports whether an error means "stop supervising" rather than
// "retry": a workspace with no credentials, or one whose container/task is gone.
func isAgentTerminal(err error) bool {
	return err != nil && (strings.Contains(err.Error(), errNoAgentCreds.Error()) || errdefs.IsNotFound(err))
}

// runAgentOnce execs the agent in the workspace and blocks until it exits.
//
// stdio is discarded (NullIO): the agent's own output is diagnostics for a guest
// nobody is watching, and holding pipes open for the life of a workspace to
// collect them would be a real cost for no reader. What the agent has to say
// travels on its heartbeats.
func (d *containerdDriver) runAgentOnce(ctx context.Context, id string) (int, error) {
	container, err := d.cli.LoadContainer(ctx, ContainerName(id))
	if err != nil {
		return 0, mapContainerdErr(err)
	}
	spec, err := container.Spec(ctx)
	if err != nil {
		return 0, err
	}
	if spec.Process == nil || !hasAgentCreds(spec.Process.Env) {
		return 0, errNoAgentCreds
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return 0, mapContainerdErr(err)
	}

	pspec := *spec.Process
	pspec.Args = agentArgv()
	pspec.Terminal = false

	execID := fmt.Sprintf("ape-agent-%d", time.Now().UnixNano())
	process, err := task.Exec(ctx, execID, &pspec, cio.NullIO)
	if err != nil {
		return 0, err
	}
	defer func() { _, _ = process.Delete(context.WithoutCancel(ctx)) }()

	statusC, err := process.Wait(ctx)
	if err != nil {
		return 0, err
	}
	if err := process.Start(ctx); err != nil {
		return 0, err
	}
	select {
	case status := <-statusC:
		code, _, rerr := status.Result()
		return int(code), rerr
	case <-ctx.Done():
		// Supervision was cancelled (stop/destroy/shutdown). Kill the exec so the
		// guest does not keep a stranded agent, then report the cancellation as
		// terminal — the caller is going away too.
		_ = process.Kill(context.WithoutCancel(ctx), agentKillSignal)
		return 0, ctx.Err()
	}
}

// hasAgentCreds reports whether a container's env carries BOTH per-VM
// credential variables. Both, because either alone is a misconfiguration the
// agent cannot recover from — and launching it to fail immediately would turn a
// deliberate no-agent workspace into a restart loop.
func hasAgentCreds(env []string) bool {
	var url, creds bool
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "APE_NATS_URL="):
			url = len(kv) > len("APE_NATS_URL=")
		case strings.HasPrefix(kv, "APE_NATS_CREDS="):
			creds = len(kv) > len("APE_NATS_CREDS=")
		}
	}
	return url && creds
}
