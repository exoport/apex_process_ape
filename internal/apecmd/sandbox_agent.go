package apecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
)

// `ape sandbox-agent` — the in-guest heartbeat (PLAN-24 D6, PLAN-18 D6).
//
// It runs INSIDE a workspace, launched by aped after the workload starts, and
// does exactly one thing: sample what the guest is doing and publish it. That
// narrowness is the design.
//
// # Why a subcommand and not a separate binary
//
// PLAN-23 already mounts the node's own `ape` into every workspace read-only at
// /opt/ape/bin, verified by READING its build info rather than executing it. So
// the agent is already delivered, already verified, and already version-matched
// to the daemon that launched it — version skew is impossible by construction. A
// separate binary would need its own goreleaser target, archive slot,
// verification path and lockstep release to deliver something already
// delivered. And Go static linking means an agent carrying the NATS client would
// land in `ape`'s size class anyway: "minimal" here is a property of the
// process, not of the artifact.
//
// # Two independent safety belts
//
// A workspace is hardware-isolated but not trusted. If a guest is fully
// compromised it holds this credential, so the question is what the credential
// can do:
//
//	(a) The SERVER denies ape.vmm.> to every per-VM credential (VMGrant), and
//	    account isolation puts management on an account this credential is not in.
//	    That is the enforcement.
//	(b) This command carries NO vmm-request-builder code path: it cannot construct
//	    a management request even if something tricked it into trying. That is
//	    belt-and-braces, asserted by a test rather than by this comment
//	    (sandbox_agent_test.go).
//
// The same binary is still a full vmm client on the HOST, because capability is
// credential-scoped at the server rather than compiled in. Belt (b) is about
// this command's code path, not about the binary.
//
// # Hidden, not secret
//
// It is a hidden command because it is machinery a human never types, not
// because running it by hand is dangerous. Run without the per-VM environment it
// says so and exits.

// agentTopProcesses is how many process names a heartbeat carries. Three is
// enough to characterise what a workspace is doing ("go, compile, link") and few
// enough to keep the payload a line rather than a listing.
const agentTopProcesses = 3

// agentReconnectNotice throttles the stderr note when the connection drops, so a
// long outage does not fill the guest's logs with one line per attempt.
const agentReconnectNotice = 5 * time.Minute

func newSandboxAgentCmd() *cobra.Command {
	var (
		interval time.Duration
		busyAt   float64
		once     bool
	)
	cmd := &cobra.Command{
		Use:    "sandbox-agent",
		Short:  "Publish this workspace's liveness heartbeat (runs inside a workspace)",
		Hidden: true,
		Long: `Sample what this workspace is doing and publish it as a heartbeat on
ape.metrics.<vm>.heartbeat, using the per-VM credential aped injected.

aped launches this inside each workspace; you do not normally run it yourself.
It reaches the host through the workspace's own CONNECT proxy — the one route a
workspace has — so it needs no network configuration beyond the environment
aped already set.

It publishes telemetry and nothing else. It holds no ability to manage
workspaces: the credential it uses is denied every management subject, and this
command contains no code that could build such a request.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSandboxAgent(cmd, agentOptions{
				Interval: interval,
				BusyAt:   busyAt,
				Once:     once,
			})
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", sandbox.DefaultHeartbeatInterval, "How often to sample and publish")
	cmd.Flags().Float64Var(&busyAt, "busy-at", sandbox.DefaultBusyPercent, "Guest CPU percent at or above which the workspace counts as busy")
	cmd.Flags().BoolVar(&once, "once", false, "Publish a single heartbeat and exit (diagnostics)")
	return cmd
}

// agentOptions are the resolved agent settings.
type agentOptions struct {
	Interval time.Duration
	BusyAt   float64
	Once     bool
}

// runSandboxAgent connects with the per-VM credential and publishes heartbeats
// until ctx is cancelled.
//
// The connection is made ONCE and reused: nats.go reconnects on its own with
// capped backoff, and a publish while disconnected is buffered rather than lost.
// A heartbeat that misses its window is not worth a reconnect storm — the reaper
// reads the last one it saw and its threshold is hours.
func runSandboxAgent(cmd *cobra.Command, opts agentOptions) error {
	if opts.Interval <= 0 {
		opts.Interval = sandbox.DefaultHeartbeatInterval
	}
	cfg := natsconn.Resolve("", "")
	if !cfg.Enabled() || cfg.CredsFile == "" {
		// The gate aped applies before launching this, restated here so a human
		// running it by hand gets the reason rather than a connection error.
		return fmt.Errorf("ape sandbox-agent runs inside a workspace: it needs %s and %s, which aped injects "+
			"(this workspace has no per-VM credential, so it runs without an agent — that is a supported configuration)",
			natsconn.EnvURL, natsconn.EnvCreds)
	}

	id, err := natsconn.DecodeIdentity(cfg.CredsFile)
	if err != nil {
		return fmt.Errorf("ape sandbox-agent: read the per-VM credential: %w", err)
	}
	token := id.SubjectToken
	if token == "" {
		return fmt.Errorf("ape sandbox-agent: the per-VM credential at %s carries no usable identity", cfg.CredsFile)
	}
	subject := sandbox.HeartbeatSubject(token)

	nc, err := connectThroughWorkspaceProxy(cmd.Context(), cfg, token)
	if err != nil {
		return err
	}
	defer func() { _ = nc.Drain() }()

	pub := &heartbeatPublisher{nc: nc, token: token, subject: subject, stderr: cmd.ErrOrStderr()}
	fmt.Fprintf(cmd.ErrOrStderr(), "▶ ape sandbox-agent: %s every %s (busy at ≥%.0f%% cpu)\n",
		subject, opts.Interval, opts.BusyAt)
	return pub.loop(cmd.Context(), opts)
}

// connectThroughWorkspaceProxy dials NATS through the workspace's CONNECT proxy.
//
// A workspace has exactly one route off the box and it is an HTTP CONNECT proxy,
// which the NATS client knows nothing about — so the dialer does the CONNECT
// handshake and hands NATS an ordinary conn (PLAN-24 D5). With no proxy in the
// environment it dials directly, which is what happens off a sandbox.
//
// The inbox prefix is the per-VM scoped one, not the default _INBOX: the
// credential denies _INBOX.> so one workspace cannot sniff another's replies,
// and a client using the default would fail its first request/reply.
func connectThroughWorkspaceProxy(ctx context.Context, cfg natsconn.Config, token string) (*nats.Conn, error) {
	opts := []nats.Option{nats.CustomInboxPrefix(sandbox.VMInboxPrefix(token))}
	if proxy := natsconn.ProxyAddrFromEnv(); proxy != "" {
		opts = append(opts, nats.SetCustomDialer(&natsconn.ProxyDialer{ProxyAddr: proxy}))
	}
	nc, err := natsconn.Connect(ctx, cfg, "ape-sandbox-agent/"+Version, opts...)
	if err != nil {
		return nil, fmt.Errorf("ape sandbox-agent: connect %s: %w", cfg.URL, err)
	}
	if nc == nil {
		return nil, fmt.Errorf("ape sandbox-agent: no NATS endpoint configured (%s)", natsconn.EnvURL)
	}
	return nc, nil
}

// heartbeatPublisher owns the publish side.
type heartbeatPublisher struct {
	nc      *nats.Conn
	token   string
	subject string
	stderr  io.Writer
	started time.Time
	warned  time.Time
}

// publish sends one heartbeat.
//
// SAFETY BELT: the subject is checked against this agent's own token on every
// send. The NATS server enforces the same thing and is the authority — this
// exists so a bug that computed a subject from the wrong input fails here, in
// the guest, loudly, instead of being silently refused by the server and looking
// like a network problem.
func (p *heartbeatPublisher) publish(h sandbox.Heartbeat) error {
	want := sandbox.HeartbeatSubject(p.token)
	if p.subject != want {
		return fmt.Errorf("ape sandbox-agent: refusing to publish on %q (this agent may only publish %q)",
			p.subject, want)
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return p.nc.Publish(p.subject, data)
}

// loop samples and publishes on the interval until ctx is done.
//
// Each heartbeat needs TWO samples to compute a delta, so the first window is
// spent measuring rather than reporting. That is why a workspace that has just
// started emits nothing for one interval — and why the reaper treats "no
// heartbeat yet" as unknown rather than idle.
func (p *heartbeatPublisher) loop(ctx context.Context, opts agentOptions) error {
	p.started = time.Now()
	prev := sandbox.SampleGuest()

	tick := time.NewTicker(opts.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil // a cancelled agent is a normal shutdown, not a failure
		case <-tick.C:
		}
		cur := sandbox.SampleGuest()
		h, ok := p.build(prev, cur, opts.BusyAt)
		prev = cur
		if !ok {
			// No reading — publish nothing. A fabricated 0% would be read as idle.
			p.warn("no CPU reading from /proc; skipping this heartbeat")
			continue
		}
		if err := p.publish(h); err != nil {
			p.warn("publish: " + err.Error())
		}
		if opts.Once {
			return p.nc.Flush()
		}
	}
}

// build assembles a heartbeat from two samples, reporting false when the guest
// could not be read at all.
func (p *heartbeatPublisher) build(prev, cur sandbox.GuestSample, busyAt float64) (sandbox.Heartbeat, bool) {
	percent, ok := sandbox.CPUPercentBetween(prev, cur)
	if !ok {
		return sandbox.Heartbeat{}, false
	}
	return sandbox.Heartbeat{
		V:             1,
		Workspace:     p.token,
		TS:            time.Now().UTC().Format(time.RFC3339),
		UptimeSeconds: int64(time.Since(p.started).Seconds()),
		Busy:          percent >= busyAt,
		CPUPercent:    percent,
		Load1:         sandbox.GuestLoad1(),
		Top:           sandbox.TopProcesses(prev, cur, agentTopProcesses),
		Agent:         Version,
	}, true
}

// warn writes a throttled note to stderr. Throttled because the agent is
// supervised and long-lived: a wedged endpoint would otherwise write a line
// every interval, forever, into a guest nobody is watching.
func (p *heartbeatPublisher) warn(msg string) {
	if time.Since(p.warned) < agentReconnectNotice {
		return
	}
	p.warned = time.Now()
	fmt.Fprintf(p.stderr, "! ape sandbox-agent: %s\n", strings.TrimSpace(msg))
}
