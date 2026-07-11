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
	Stderr            io.Writer
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

	// The vmm service dispatches to the executor over the priv socket; Create is
	// resolved here (de-privileged) before it crosses the boundary.
	resolver := NewResolver(ResolverConfig{
		StateDir:    cfg.StateDir,
		HostHome:    cfg.HostHome,
		NatsURL:     cfg.GuestNatsURL,
		CredsExpiry: cfg.CredsExpiry,
		Telemetry:   srv.Telemetry(),
	})
	// The front holds the NATS conn, so it forwards the executor's audit records
	// on ape.audit.<node>.> (the network-less executor returns them in-band —
	// PLAN-18 D9). Fire-and-forget: a publish failure must never fail the op.
	backend := NewPrivClient(PrivClientConfig{
		Socket:  cfg.Socket,
		Resolve: resolver.Resolve,
		Publish: func(subject string, data []byte) { _ = nc.Publish(subject, data) },
		Node:    node,
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
		NatsConn: nc,
		Socket:   cfg.Socket,
		Publish:  func(subject string, data []byte) { _ = nc.Publish(subject, data) },
	}
	// The vmm handlers use context.Background() (a micro.Request carries no
	// context), which contextcheck flags at this call site where a ctx is in
	// scope — but there is no ctx to thread into a NATS request handler.
	if err := NewVMM(vmmCfg).Register(svc); err != nil { //nolint:contextcheck // handlers have no request context
		return err
	}
	_ = nc.Flush()

	fmt.Fprintf(stderr, "▶ aped front — ape.vmm.%s.> on %s (executor via %s)\n", node, srv.ClientURL(), cfg.Socket)
	// The vmm service is registered and the operator cred is written; tell the
	// service manager we are up and arm the watchdog (no-ops under Type=exec).
	signalReady(ctx)
	return serveUntilSignal(ctx, svc, stderr)
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
	if existing, rerr := os.ReadFile(path); rerr == nil {
		if hostOps.reusableOperatorCreds(existing, now(), requirePub) {
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
