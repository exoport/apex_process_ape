package aped

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/nats-io/nats.go/micro"
)

// FrontConfig configures the `aped front` de-privileged NATS surface.
type FrontConfig struct {
	Node     string // <node> subject segment of ape.vmm.<node>.>
	Socket   string // priv socket to reach the executor
	MgmtHost string // management listener host (default 127.0.0.1; guest-unreachable)
	MgmtPort int    // management listener port
	StateDir string // keys, staging homes, per-VM creds
	HostHome string // home Compose sources ~/.claude from
	// GuestNatsURL is APE_NATS_URL injected into guests (the bridge-IP telemetry
	// endpoint). "" disables per-VM cred injection (guests boot without an agent).
	GuestNatsURL string
	// OperatorCredsPath is where the host-operator .creds for the `ape` CLI is
	// written (0600). Empty skips it (an operator cred is provisioned elsewhere).
	OperatorCredsPath string
	CredsExpiry       time.Duration
	ApeVersion        string
	// ApeGitCommit is this daemon's own build revision. It separates two local builds that
	// carry the same version string, which is what a partial `make build` produces.
	ApeGitCommit string
	// ApeBinary overrides which `ape` is delivered into workspaces (PLAN-23). Empty means
	// the one beside this aped, which is the matching build by construction: they ship in a
	// single release archive.
	ApeBinary string
	// PolicyPath is the same policy.yaml the executor loads. The front reads it to
	// pre-check egress requests and to build each workspace's proxy allowlist
	// (PLAN-21 D1/D2) — the executor still re-validates authoritatively. Empty
	// disables egress entirely (fail-closed: no policy, no egress).
	PolicyPath string
	// EgressBindIP is the bridge host address the per-workspace CONNECT proxies
	// listen on — where guests reach them. Empty → sandbox's default bridge address.
	EgressBindIP string
	// FrameworkRoot is the host directory holding materialized APEX framework refs,
	// one subdirectory per ref. Empty → this node serves no framework mount.
	FrameworkRoot string
	// FrameworkRef is the default framework ref mounted when a request names none.
	FrameworkRef string
	// CacheRoot is the host directory holding durable tool caches (PLAN-22 D4).
	// Empty → cache requests are ignored and toolchain state stays in the rootfs.
	CacheRoot string
	// Credentials is the credential mode composed into workspaces by default:
	// "oauth" copies the credential published under HostHome into each workspace and
	// keeps them converged (see `ape sandbox credentials publish`), "none" injects
	// nothing. Empty → none.
	Credentials string
	// CredSyncInterval is how often the shared-credential sync loop runs; 0 → the
	// package default. Only used with the oauth mode.
	CredSyncInterval time.Duration
	// EgressPortLow/High bound the proxy listen ports. They MUST match the host
	// nftables chain's accepted range (both come from deploy/dev-host.sh). 0 → the
	// sandbox defaults.
	EgressPortLow, EgressPortHigh int
	// IdleStop is the node-wide idle-stop threshold (PLAN-24 D7). Zero disables
	// the reaper, which is the default: automatic lifecycle action is opt-in, and a
	// node that has not opted in must not start stopping workspaces after an
	// upgrade.
	IdleStop time.Duration
	Stderr   io.Writer
}

// RunFront is the `aped front` entry point: it embeds the two-account NATS
// server, runs the vmm micro service (forwarding to the executor over the priv
// socket), mints the host-operator credential for the CLI, and serves until
// ctx cancellation or SIGINT/SIGTERM.
func RunFront(ctx context.Context, cfg FrontConfig) error {
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	node := natsconn.SubjectToken(cfg.Node)
	if node == "" {
		return fmt.Errorf("%w: node is required", ErrConfig)
	}

	srv, err := StartServer(ServerConfig{Host: cfg.MgmtHost, Port: cfg.MgmtPort, StoreDir: cfg.StateDir, Name: "aped-" + node})
	if err != nil {
		return err
	}
	defer srv.Shutdown()

	// The front's own HOST_OPS service credential (never leaves this host).
	svcCreds, _, err := srv.HostOps().MintUser("aped-front", serviceGrant(), 0)
	if err != nil {
		return err
	}
	svcCredsPath := filepath.Join(cfg.StateDir, "creds", "front.creds")
	if err := writeSecret(svcCredsPath, svcCreds); err != nil {
		return err
	}
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: srv.ClientURL(), CredsFile: svcCredsPath}, "aped-front/"+cfg.ApeVersion)
	if err != nil {
		return err
	}
	defer func() { _ = nc.Drain() }()

	// Provision a scoped host-operator credential for the `ape` CLI, reusing the
	// existing one across restarts (the account seed persists) so the operator's
	// copy is not churned every restart.
	if cfg.OperatorCredsPath != "" {
		reused, err := ensureOperatorCreds(srv.HostOps(), node, cfg.OperatorCredsPath)
		if err != nil {
			return err
		}
		action := "minted"
		if reused {
			action = "reused"
		}
		fmt.Fprintf(stderr, "  operator creds: %s (%s; point the ape CLI at APE_NATS_URL=%s APE_NATS_CREDS=%s)\n",
			cfg.OperatorCredsPath, action, srv.ClientURL(), cfg.OperatorCredsPath)
	}

	// Egress (PLAN-21 D2): the front — de-privileged, but the only aped process
	// allowed AF_INET — hosts one deny-by-default CONNECT proxy per workspace. The
	// policy it intersects requests against is the same file the executor
	// re-validates against; with no policy configured, egress stays off.
	// The proxies it starts deliberately outlive this ctx: they serve workspaces
	// that keep running, and are torn down by the deferred StopAll on shutdown
	// rather than by request cancellation.
	egress, err := startEgress(cfg, srv, node, nc.Publish, stderr) //nolint:contextcheck // proxies are lifetime-managed by StopAll, not ctx
	if err != nil {
		return err
	}
	if egress != nil {
		defer egress.StopAll()
	}

	// The `ape` every workspace runs (PLAN-23). Resolved and verified BEFORE the service
	// starts answering, and fatal when it fails: a workspace with no `ape` — or with one
	// that does not match this daemon — is broken, not degraded, and discovering that as
	// `command not found` inside a guest costs far more than refusing here.
	apeBin, err := resolveAndStageApe(cfg)
	if err != nil {
		return err
	}
	for _, w := range apeBin.Warnings {
		fmt.Fprintf(stderr, "! ape delivery: %s\n", w)
	}
	fmt.Fprintf(stderr, "  ape delivery: %s → %s (read-only, first on PATH)\n",
		apeBin, sandbox.ApeBinDest)

	// The vmm service dispatches to the executor over the priv socket; Create is
	// resolved here (de-privileged) before it crosses the boundary.
	resolver := NewResolver(ResolverConfig{
		StateDir:      cfg.StateDir,
		HostHome:      cfg.HostHome,
		NatsURL:       cfg.GuestNatsURL,
		CredsExpiry:   cfg.CredsExpiry,
		Telemetry:     srv.Telemetry(),
		Egress:        egressPlannerOrNil(egress),
		FrameworkRoot: cfg.FrameworkRoot,
		FrameworkRef:  cfg.FrameworkRef,
		CacheRoot:     cfg.CacheRoot,
		ApeBin:        apeBin,
		// Re-verified per create, not just at startup: the file can be replaced under a
		// running daemon, which is exactly what a redeploy does. A create landing in that
		// window would otherwise deliver a binary nothing has checked.
		ApeBinRecheck: func() (ApeBinary, error) { return resolveAndStageApe(cfg) },
		Credentials:   sandbox.CredentialMode(cfg.Credentials),
	})
	if mode := sandbox.CredentialMode(cfg.Credentials); mode != "" && mode != sandbox.CredentialNone {
		fmt.Fprintf(stderr, "  credentials: %s from %s (publish with 'ape sandbox credentials publish')\n",
			mode, cfg.HostHome)
	}
	// Keep the operator's credential and every workspace's copy converged, so ONE OAuth
	// session is shared: a refresh or login in a workspace reaches the host (through the
	// published hard link) and the other workspaces, and vice versa. Without this each
	// workspace would hold a copy that dies at the first token rotation.
	if sandbox.CredentialMode(cfg.Credentials) == sandbox.CredentialOAuth && cfg.HostHome != "" {
		syncer := &CredentialSync{
			Published: filepath.Join(cfg.HostHome, ".claude", ".credentials.json"),
			StateDir:  cfg.StateDir,
			Interval:  cfg.CredSyncInterval,
			Stderr:    stderr,
		}
		go syncer.Run(ctx)
	}
	if cfg.FrameworkRoot != "" {
		fmt.Fprintf(stderr, "  framework: %s (default ref %q) mounted read-only at %s\n",
			cfg.FrameworkRoot, cfg.FrameworkRef, sandbox.FrameworkDest)
	}
	// The front holds the NATS conn, so it forwards the executor's audit records
	// on ape.audit.<node>.> (the network-less executor returns them in-band —
	// PLAN-18 D9). Fire-and-forget: a publish failure must never fail the op.
	backend := NewPrivClient(PrivClientConfig{
		Socket:  cfg.Socket,
		Resolve: resolver.Resolve,
		Publish: func(subject string, data []byte) { _ = nc.Publish(subject, data) },
		Node:    node,
		// A destroyed workspace's proxy must go with it: the port is returned to the
		// allocator and the guest's only route out disappears with the netns the
		// executor tore down.
		OnDestroy: func(id string) {
			if egress != nil {
				egress.Stop(id)
			}
		},
	})

	hostname, _ := os.Hostname()
	svc, err := micro.AddService(nc, micro.Config{
		Name:        "ape-vmm",
		Version:     microVersion(cfg.ApeVersion),
		Description: "aped VM-management service (PLAN-18): Kata-QEMU workspaces over ape.vmm.<node>.>",
		Metadata:    map[string]string{"node": node, "hostname": hostname, "ape_version": cfg.ApeVersion},
	})
	if err != nil {
		return fmt.Errorf("aped: register vmm service: %w", err)
	}
	// NatsConn/Socket/Publish arm the interactive attach bridge: attach.open dials
	// a streaming priv conn to the executor and bridges the PTY to the session
	// subjects on this same front conn (the executor is network-less).
	vmmCfg := VMMConfig{
		Node:     node,
		Backend:  backend,
		Egress:   egressPlannerOrNil(egress),
		NatsConn: nc,
		Socket:   cfg.Socket,
		Publish:  func(subject string, data []byte) { _ = nc.Publish(subject, data) },
		// Cost reporting reads the composed homes this process owns (PLAN-24 D3) —
		// the operator's `ape` cannot, they are 0700 and aped-owned.
		Costs: &WorkspaceCosts{StateDir: cfg.StateDir, Backend: backend},
	}
	// The vmm handlers use context.Background() (a micro.Request carries no
	// context), which contextcheck flags at this call site where a ctx is in
	// scope — but there is no ctx to thread into a NATS request handler.
	if err := NewVMM(vmmCfg).Register(svc); err != nil { //nolint:contextcheck // handlers have no request context
		return err
	}
	_ = nc.Flush()

	// The idle reaper (PLAN-24 D7). Started AFTER the service is registered so a
	// node that fails to come up never stops anything on its way down.
	if rerr := startReaper(ctx, cfg, srv, backend, stderr); rerr != nil {
		// A reaper that cannot start is worth failing on: the alternative is a node
		// that silently never reclaims memory, which is discovered weeks later as
		// "the box filled up" rather than as a startup error.
		return rerr
	}

	fmt.Fprintf(stderr, "▶ aped front — ape.vmm.%s.> on %s (executor via %s)\n", node, srv.ClientURL(), cfg.Socket)
	// The vmm service is registered and the operator cred is written; tell the
	// service manager we are up and arm the watchdog (no-ops under Type=exec).
	signalReady(ctx)
	return serveUntilSignal(ctx, svc, stderr)
}

// startEgress builds the per-workspace egress supervisor and restores the
// proxies a previous front left running. It returns nil (and no error) on a node
// with no policy configured — egress is fail-closed, so no policy means no
// egress rather than open egress.
//
// The caller defers StopAll: the proxies deliberately outlive any request
// context, because the workspaces they serve do.
func startEgress(cfg FrontConfig, srv *Server, node string, publish func(string, []byte) error, stderr io.Writer) (*EgressSupervisor, error) {
	// The in-guest agent's route home (PLAN-24 D5). Built from THIS daemon's own
	// configuration — the URL it hands guests and the listener it already holds —
	// so it is a node grant rather than anything a workspace can ask for. An
	// unusable configuration is fatal here rather than at the first heartbeat: a
	// route that silently does not exist looks exactly like an agent that never
	// started, and D7 is built to read that as "unknown", so it would degrade
	// quietly and forever.
	agentRoute, err := sandbox.AgentNatsRoute(cfg.GuestNatsURL, cfg.MgmtHost, srv.Port())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	var systemRoutes []sandbox.SystemRoute
	if agentRoute.Host != "" {
		systemRoutes = append(systemRoutes, agentRoute)
		fmt.Fprintf(stderr, "  agent endpoint: %s → %s (system route, audited as %q)\n",
			cfg.GuestNatsURL, agentRoute.Target, agentRoute.Reason)
	}
	if cfg.PolicyPath == "" {
		return nil, nil //nolint:nilnil // "this node has no egress" is a configuration, not an error
	}

	policy, err := LoadPolicy(cfg.PolicyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	egress := NewEgressSupervisor(EgressConfig{
		BindIP:       cfg.EgressBindIP,
		PortLow:      cfg.EgressPortLow,
		PortHigh:     cfg.EgressPortHigh,
		StateDir:     cfg.StateDir,
		Policy:       &policy.Egress,
		SystemRoutes: systemRoutes,
		Publish:      func(subject string, data []byte) { _ = publish(subject, data) },
		Node:         node,
		Stderr:       stderr,
	})
	// Rebuild the proxies of workspaces that are still running from a previous
	// front: restarting this process must not silently strip their egress.
	egress.RestoreAll()
	if policy.Egress.Enabled {
		fmt.Fprintf(stderr, "  egress: enabled — %d allowed domain(s), proxies on %s\n",
			len(policy.Egress.AllowedDomains), egressBindNote(cfg.EgressBindIP))
	} else {
		fmt.Fprintln(stderr, "  egress: disabled by policy (egress.enabled: false) — workspaces stay networkless")
	}
	return egress, nil
}

// startReaper wires and launches the idle reaper, or explains why it is off.
//
// It needs its OWN connection, on the TELEMETRY account: heartbeats are
// published by per-VM credentials, which live in TELEMETRY, and the front's
// service connection is in HOST_OPS. Account isolation is the boundary that
// keeps a compromised guest away from management — so the reaper crosses it the
// only legitimate way, with a read-only ingest credential that can subscribe
// every VM's telemetry and publish nothing.
func startReaper(ctx context.Context, cfg FrontConfig, srv *Server, backend workspace.Backend, stderr io.Writer) error {
	if cfg.IdleStop <= 0 {
		fmt.Fprintln(stderr, "  idle reaper: off (--idle-stop 0) — workspaces are stopped by hand")
		return nil
	}
	creds, _, err := srv.Telemetry().MintUser("aped-reaper", telemetryIngestGrant(), 0)
	if err != nil {
		return err
	}
	credsPath := filepath.Join(cfg.StateDir, "creds", "reaper.creds")
	if err := writeSecret(credsPath, creds); err != nil {
		return err
	}
	conn, err := natsconn.Connect(ctx,
		natsconn.Config{URL: srv.ClientURL(), CredsFile: credsPath}, "aped-reaper/"+cfg.ApeVersion)
	if err != nil {
		return fmt.Errorf("aped: reaper telemetry connection: %w", err)
	}
	reaper := NewReaper(ReaperConfig{
		Backend:   backend,
		Conn:      conn,
		IdleAfter: cfg.IdleStop,
		Stderr:    stderr,
	})
	go func() {
		defer func() { _ = conn.Drain() }()
		if rerr := reaper.Run(ctx); rerr != nil {
			fmt.Fprintf(stderr, "! aped reaper: stopped: %v\n", rerr)
		}
	}()
	fmt.Fprintf(stderr, "  idle reaper: workspaces idle for %s are STOPPED (state kept); "+
		"a workspace with no heartbeat is never reaped\n", cfg.IdleStop)
	return nil
}

// egressPlannerOrNil returns the supervisor as an EgressPlanner, or a nil
// interface when there is none. Returning a typed nil pointer would make the
// resolver's `r.egress == nil` guard false and NPE on the first request.
func egressPlannerOrNil(s *EgressSupervisor) EgressPlanner {
	if s == nil {
		return nil
	}
	return s
}

// egressBindNote renders the proxy bind address for the startup line.
func egressBindNote(bindIP string) string {
	if strings.TrimSpace(bindIP) != "" {
		return bindIP
	}
	return sandbox.DefaultEgressHostCIDR + " (default bridge address)"
}

// ensureOperatorCreds writes the scoped host-operator credential for the `ape`
// CLI at path, REUSING an existing file when it still validates (issued by the
// current HOST_OPS account, unexpired, and scoped to this node) instead of
// re-minting. Re-minting on every restart rewrites the file with a fresh user
// key, churning the operator's 0600 copy — which the human must then re-copy.
// Reuse is sound only because the account seed is persisted across restart
// (StartServer StoreDir); with no persisted store the old cred fails the issuer
// check and is re-minted, closing the loop. Returns whether it reused.
func ensureOperatorCreds(hostOps Account, node, path string) (reused bool, err error) {
	requirePub := subjectVMM + "." + node + ".>"
	requireSub := subjectVMM + "." + node + ".exec.>" // the interactive session subtree
	if existing, rerr := os.ReadFile(path); rerr == nil {
		if hostOps.reusableOperatorCreds(existing, now(), requirePub, requireSub) {
			return true, nil
		}
	}
	creds, _, err := hostOps.MintUser("ape-operator", OperatorGrant(node), 0)
	if err != nil {
		return false, err
	}
	if err := writeSecret(path, creds); err != nil {
		return false, err
	}
	return false, nil
}

// serveUntilSignal blocks until ctx is cancelled or SIGINT/SIGTERM, then stops
// the micro service (the deferred conn.Drain flushes pending publishes).
func serveUntilSignal(ctx context.Context, svc micro.Service, stderr io.Writer) error {
	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)

	select {
	case <-ctx.Done():
	case <-sigc:
	}
	signalStopping() // no-op outside a Type=notify unit
	fmt.Fprintln(stderr, "⇣ aped front: draining")
	_ = svc.Stop()
	return nil
}

// semVerRe is the SemVer shape micro.AddService requires for Version.
var semVerRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-.]+)?(?:\+[0-9A-Za-z-.]+)?$`)

// microVersion returns a SemVer acceptable to micro.AddService: the ape version
// when already SemVer, else 0.0.0 (dev builds), mirroring internal/service.
func microVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if semVerRe.MatchString(v) {
		return v
	}
	return "0.0.0"
}

// resolveAndStageApe verifies the node's `ape` and stages it into a directory of its own.
//
// The staging step is not incidental: the mount is a directory, and the directory an `ape`
// is installed in is /usr/local/bin, which on a real node also holds containerd, the Kata
// shims and aped itself. Mounting that into every workspace first on PATH would expose all
// of it and shadow the image's own tooling with the host's.
func resolveAndStageApe(cfg FrontConfig) (ApeBinary, error) {
	bin, err := ResolveApeBinary(cfg.ApeBinary, cfg.ApeVersion, cfg.ApeGitCommit)
	if err != nil {
		return ApeBinary{}, err
	}
	return StageApeBinary(cfg.StateDir, bin)
}
