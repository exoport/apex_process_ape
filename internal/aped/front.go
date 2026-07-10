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

	// Provision a scoped host-operator credential for the `ape` CLI.
	if cfg.OperatorCredsPath != "" {
		opCreds, _, err := srv.HostOps().MintUser("ape-operator", OperatorGrant(node), 0)
		if err != nil {
			return err
		}
		if err := writeSecret(cfg.OperatorCredsPath, opCreds); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "  operator creds: %s (point the ape CLI at APE_NATS_URL=%s APE_NATS_CREDS=%s)\n",
			cfg.OperatorCredsPath, srv.ClientURL(), cfg.OperatorCredsPath)
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
	backend := NewPrivClient(cfg.Socket, resolver.Resolve)

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
	// The vmm handlers use context.Background() (a micro.Request carries no
	// context), which contextcheck flags at this call site where a ctx is in
	// scope — but there is no ctx to thread into a NATS request handler.
	if err := NewVMM(VMMConfig{Node: node, Backend: backend}).Register(svc); err != nil { //nolint:contextcheck // handlers have no request context
		return err
	}
	_ = nc.Flush()

	fmt.Fprintf(stderr, "▶ aped front — ape.vmm.%s.> on %s (executor via %s)\n", node, srv.ClientURL(), cfg.Socket)
	return serveUntilSignal(ctx, svc, stderr)
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
