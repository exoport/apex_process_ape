// Package workspace defines the transport-agnostic contract for provisioning
// and operating isolated workspaces (PLAN-18 D3). One Backend interface is
// implemented by a local driver today (the shellDriver in internal/sandbox,
// which shells out to nerdctl/ctr) and, in a later phase, by a NATS client
// speaking the `vmm` micro contract to `aped` — so `ape` and a future
// controller code identically against either.
//
// The request/response types are JSON-serializable and double as the NATS
// wire contract documented in docs/reference/events.md (the `ape.vmm` service).
// The sentinel errors map one-to-one onto that contract's req.Error code set.
// This package is pure and cross-platform: it carries no Kata/KVM/containerd
// code and builds on every platform.
package workspace

import (
	"context"
	"errors"
	"io"
)

// WireVersion is the vmm request/reply payload version. Payloads are
// additive-only; bump only for a breaking change (and document it in
// docs/reference/events.md). It mirrors the PLAN-13/14 versioning discipline
// (the micro service Version field + a payload "v":1 envelope).
const WireVersion = 1

// Sentinel errors returned by Backend implementations. They map one-to-one
// onto the `ape.vmm` req.Error code set frozen in docs/reference/events.md;
// use Code to render an error as its wire code. Wrap them with %w so callers
// (and the future NATS handler) can classify with errors.Is.
var (
	// ErrUnsupported: the operation is not available on this backend (e.g.
	// Suspend/Resume/Snapshot on the Kata-via-containerd path — D7). → UNSUPPORTED.
	ErrUnsupported = errors.New("workspace: unsupported on this backend")
	// ErrNotFound: no workspace with the requested id. → NOT_FOUND.
	ErrNotFound = errors.New("workspace: no such workspace")
	// ErrBusy: the workspace (or a resource it needs) is held by another
	// operation. → BUSY.
	ErrBusy = errors.New("workspace: busy")
	// ErrValidation: the request shape or contents are invalid. → VALIDATION.
	ErrValidation = errors.New("workspace: invalid request")
	// ErrDeviceUnavailable: a requested passthrough device is missing, bound
	// elsewhere, or not isolable. → DEVICE_UNAVAILABLE.
	ErrDeviceUnavailable = errors.New("workspace: device unavailable")
	// ErrPolicyDenied: the caller is not permitted this operation by policy.
	// → DENIED.
	ErrPolicyDenied = errors.New("workspace: denied by policy")
)

// The vmm req.Error codes (docs/reference/events.md). These strings are an
// external contract — never renamed or repurposed. Phase 2's `vmm` micro
// handlers return them via req.Error; Code maps a sentinel error to one.
const (
	CodeUnsupported       = "UNSUPPORTED"
	CodeNotFound          = "NOT_FOUND"
	CodeBusy              = "BUSY"
	CodeValidation        = "VALIDATION"
	CodeDeviceUnavailable = "DEVICE_UNAVAILABLE"
	CodeDenied            = "DENIED"
)

// Code returns the vmm req.Error code for err, matching against the sentinel
// errors with errors.Is (so wrapped errors classify correctly). It returns
// "" when err is nil or is not one of the recognized sentinels — the caller
// (or the NATS handler) decides how to render an unclassified error.
func Code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrUnsupported):
		return CodeUnsupported
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrBusy):
		return CodeBusy
	case errors.Is(err, ErrValidation):
		return CodeValidation
	case errors.Is(err, ErrDeviceUnavailable):
		return CodeDeviceUnavailable
	case errors.Is(err, ErrPolicyDenied):
		return CodeDenied
	default:
		return ""
	}
}

// Backend provisions and operates workspaces behind a transport-agnostic
// interface (PLAN-18 D3). A local driver and a remote NATS client both
// implement it identically. All methods take a context for cancellation;
// id is the logical workspace id returned by Create.
//
//nolint:interfacebloat // the vmm Backend is the full workspace-lifecycle contract (PLAN-18 D3); one verb per NATS endpoint
type Backend interface {
	// Capabilities reports what this backend/node can provision — scheduler
	// input for a future controller (PLAN-18 §10/D8).
	Capabilities(ctx context.Context) (Capabilities, error)

	// Create provisions a detached workspace from a fully-resolved request
	// and returns its durable record.
	Create(ctx context.Context, req CreateRequest) (Workspace, error)
	// Start (re)starts a created-or-stopped workspace.
	Start(ctx context.Context, id string) error
	// Stop gracefully stops a running workspace.
	Stop(ctx context.Context, id string) error
	// Destroy tears a workspace down.
	Destroy(ctx context.Context, id string, req DestroyRequest) error

	// Exec runs a one-shot command inside a running workspace.
	Exec(ctx context.Context, id string, req ExecRequest) (ExitStatus, error)
	// Attach opens an interactive session, wiring stdio through the Stream.
	Attach(ctx context.Context, id string, req AttachRequest, s Stream) (ExitStatus, error)

	// Freeze/Unfreeze are a containerd cgroup-freeze: the guest is stopped
	// but its RAM stays fully resident. This is real on the Kata backend
	// today — it is NOT a VM suspend.
	Freeze(ctx context.Context, id string) error
	Unfreeze(ctx context.Context, id string) error

	// Suspend/Resume/Snapshot are VMM save/restore (guest RAM to disk). They
	// return ErrUnsupported on Kata-via-containerd (the shim's Checkpoint is
	// unimplemented and the VMM control socket is Kata-owned — D7); only a
	// future VMM-owning driver or the Firecracker tier implements them.
	Suspend(ctx context.Context, id string) error
	Resume(ctx context.Context, id string) error
	Snapshot(ctx context.Context, id string, req SnapshotRequest) (SnapshotRef, error)

	// Logs returns a reader over the workspace's output.
	Logs(ctx context.Context, id string, req LogsRequest) (io.ReadCloser, error)
	// Events streams lifecycle events (TaskExit/TaskFrozen/TaskOOM …).
	Events(ctx context.Context) (<-chan Event, error)

	// List enumerates known workspaces; Inspect reports one's live status.
	List(ctx context.Context) ([]Workspace, error)
	Inspect(ctx context.Context, id string) (Status, error)
}

// Stream is the transport-agnostic interactive-attach channel. Locally it is
// wired directly to a PTY/process stdio; over NATS it rides the per-session
// exec subjects (PLAN-18 D2). Stderr is an explicit sink (the design-doc
// sketch omitted it).
type Stream interface {
	Stdin() io.Reader
	Stdout() io.Writer
	Stderr() io.Writer
	Resize(cols, rows uint16) error
	CloseWrite() error // half-close stdin
}

// Process is a running interactive process inside a workspace — an exec command
// or the attach shell — whose stdio a session relays (PLAN-18 D2). It is the
// server-side dual of Stream: the relayer WRITES client keystrokes to Stdin,
// READS process output from Stdout/Stderr, forwards Resize, and blocks on Wait
// for the exit code. A local driver (the containerd task exec) implements it; the
// aped front relays it over the priv socket, then bridges to the NATS session
// subjects (internal/vmmstream). Kept here — the pure contract — so the driver
// (internal/sandbox) and the transport (internal/vmmstream) share one definition
// without either importing the other.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Resize(cols, rows uint16) error
	Wait(ctx context.Context) (int, error)
	// Kill force-terminates the process so a relay can reap it when its
	// transport drops before a clean exit — an abandoned interactive client
	// (network drop / kill -9) would otherwise leak a live guest exec, since
	// NATS gives the server no disconnect signal (PLAN-18 D2). It is a no-op
	// (or a benign not-found) once the process has already exited.
	Kill(ctx context.Context) error
}
