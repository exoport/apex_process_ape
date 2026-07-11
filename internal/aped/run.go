package aped

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/sandbox"
)

// ErrConfig marks a configuration/usage failure (bad policy, missing paths) the
// command layer maps to a usage exit; everything else RunExecutor/RunFront
// return is a runtime failure.
var ErrConfig = errors.New("aped: configuration error")

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
	Stderr          io.Writer
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

	dataRoot := cfg.NerdctlDataRoot
	if dataRoot == "" {
		dataRoot = filepath.Join(cfg.StateDir, "nerdctl")
	}
	runner := &sandbox.Runner{Nerdctl: cfg.Nerdctl, DataRoot: dataRoot, Stdout: stderr, Stderr: stderr}
	reg := sandbox.OpenRegistry(cfg.StateDir)
	shell := sandbox.NewShellDriver(runner, reg, nil) // id-verbs + list/inspect/capabilities

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
		Backend:     shell,
		Provision:   NewShellProvisioner(runner, reg),
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

	fmt.Fprintf(stderr, "▶ aped run (executor) on %s — %d allowed peer uid(s), policy %s\n", l.Addr(), len(cfg.AllowedUIDs), cfg.PolicyPath)
	return ex.Serve(ctx, l)
}
