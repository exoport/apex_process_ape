package aped

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// ErrConfig marks a configuration/usage failure (bad policy, missing paths) the
// command layer maps to a usage exit; everything else RunExecutor/RunFront
// return is a runtime failure.
var ErrConfig = errors.New("aped: configuration error")

// Driver selectors for --driver.
const (
	// DriverShell is the default: the PLAN-16 nerdctl shellDriver. Its Create
	// hits the executor-sandbox wall through the hardened unit (client-side
	// mount(2) — see PLAN-18 Risks); read-only verbs work.
	DriverShell = "shell"
	// DriverContainerd is the opt-in containerd Go-client driver. It builds the
	// OCI spec without the client-side rootfs mount, so `ape sandbox up` works
	// through the hardened executor (PLAN-18 D3). Linux-only.
	DriverContainerd = "containerd"
)

// ExecutorRunConfig configures the `aped run` root executor.
type ExecutorRunConfig struct {
	// Socket is the priv socket path to bind when not socket-activated. Under
	// systemd (aped-priv.socket) the listener is inherited and Socket is unused.
	Socket string
	// PolicyPath is the required policy.yaml (fail-closed: no policy → no run).
	PolicyPath string
	// StateDir holds the workspace registry (server-side source of truth).
	StateDir string
	// AuditLog is the append-only audit JSONL path ("" → no file sink).
	AuditLog string
	// Node is the <node> token stamped into audit subjects.
	Node string
	// AllowedUIDs are the peer uids permitted over the priv socket — the
	// aped-front uid. Empty rejects every peer (fail-closed).
	AllowedUIDs []uint32
	// Nerdctl overrides the driver binary (default "nerdctl").
	Nerdctl string
	// NerdctlDataRoot relocates nerdctl's metadata store (global --data-root).
	// Empty → <StateDir>/nerdctl, which lives under the executor's writable
	// ReadWritePaths so nerdctl works under ProtectSystem=strict without
	// widening the unit (PLAN-18 D1 — the executor-sandbox gap).
	NerdctlDataRoot string
	// Driver selects the workspace backend: DriverShell (default) or
	// DriverContainerd (the opt-in barrier-3 fix). Empty → DriverShell.
	Driver string
	// ContainerdAddress/ContainerdNamespace configure the containerd driver
	// ("" → the sandbox package defaults).
	ContainerdAddress   string
	ContainerdNamespace string
	Stderr              io.Writer
}

// RunExecutor is the `aped run` entry point: the network-less root executor. It
// loads policy, opens the audit log, binds (or adopts) the priv socket, and
// serves commands until ctx is cancelled.
func RunExecutor(ctx context.Context, cfg ExecutorRunConfig) error {
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	policy, err := LoadPolicy(cfg.PolicyPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConfig, err)
	}

	reg := sandbox.OpenRegistry(cfg.StateDir)
	backend, provision, closeDriver, err := buildDriver(cfg, reg, stderr)
	if err != nil {
		return err
	}
	defer closeDriver()

	var auditW io.Writer
	if cfg.AuditLog != "" {
		f, err := OpenAuditLog(cfg.AuditLog)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		auditW = f
	}
	// The executor is network-less: audit is written to the append-only file
	// here; NATS forwarding on ape.audit.<node>.> is done front-side (follow-up).
	auditor := NewAuditor(auditW, nil, cfg.Node)

	ex := NewExecutor(ExecutorConfig{
		Backend:     backend,
		Provision:   provision,
		Policy:      policy,
		Auditor:     auditor,
		AllowedUIDs: cfg.AllowedUIDs,
		Node:        cfg.Node,
	})

	l, activated, err := socketActivatedListener()
	if err != nil {
		return err
	}
	if !activated {
		if cfg.Socket == "" {
			return fmt.Errorf("%w: no priv socket path and not socket-activated", ErrConfig)
		}
		l, err = listenPriv(cfg.Socket)
		if err != nil {
			return err
		}
	}
	defer func() { _ = l.Close() }()

	driver := cfg.Driver
	if driver == "" {
		driver = DriverShell
	}
	fmt.Fprintf(stderr, "▶ aped run (executor, driver=%s) on %s — %d allowed peer uid(s), policy %s\n", driver, l.Addr(), len(cfg.AllowedUIDs), cfg.PolicyPath)
	// The listener is bound (or adopted); tell the service manager we are up and
	// start the watchdog pinger (both no-ops outside a Type=notify unit).
	signalReady(ctx)
	defer signalStopping()
	return ex.Serve(ctx, l)
}

// buildDriver selects the workspace backend + provisioner from cfg.Driver. It
// returns a closer (always non-nil) the caller defers. The shellDriver's
// Provisioner shells out to nerdctl (the default, but blocked through the
// hardened unit — PLAN-18 Risks); the containerd driver provisions via the Go
// client without a client-side rootfs mount (the barrier-3 fix, opt-in).
func buildDriver(cfg ExecutorRunConfig, reg *sandbox.Registry, stderr io.Writer) (workspace.Backend, Provisioner, func(), error) {
	switch cfg.Driver {
	case "", DriverShell:
		dataRoot := cfg.NerdctlDataRoot
		if dataRoot == "" {
			dataRoot = filepath.Join(cfg.StateDir, "nerdctl")
		}
		runner := &sandbox.Runner{Nerdctl: cfg.Nerdctl, DataRoot: dataRoot, Stdout: stderr, Stderr: stderr}
		return sandbox.NewShellDriver(runner, reg, nil), NewShellProvisioner(runner, reg), func() {}, nil
	case DriverContainerd:
		cd, err := sandbox.NewContainerdDriver(sandbox.ContainerdConfig{
			Address:   cfg.ContainerdAddress,
			Namespace: cfg.ContainerdNamespace,
			Registry:  reg,
		})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %w", ErrConfig, err)
		}
		return cd, cd.Provision, func() { _ = cd.Close() }, nil
	default:
		return nil, nil, nil, fmt.Errorf("%w: unknown driver %q (want %s|%s)", ErrConfig, cfg.Driver, DriverShell, DriverContainerd)
	}
}
