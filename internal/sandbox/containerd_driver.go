package sandbox

import (
	"context"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// Containerd driver defaults.
const (
	// DefaultContainerdAddress is the rootful containerd socket the driver dials.
	DefaultContainerdAddress = "/run/containerd/containerd.sock"
	// DefaultContainerdNamespace isolates aped's containers/snapshots/images in
	// containerd from any other tenant on the host.
	DefaultContainerdNamespace = "aped"
)

// ContainerdConfig configures NewContainerdDriver.
type ContainerdConfig struct {
	// Address is the containerd socket ("" → DefaultContainerdAddress).
	Address string
	// Namespace is the containerd namespace ("" → DefaultContainerdNamespace).
	Namespace string
	// Registry is the authoritative server-side workspace index (List/Inspect
	// existence). nil → List/Inspect report ErrUnsupported.
	Registry *Registry
	// Resolve turns a wire CreateRequest into a resolved spec for Backend.Create.
	// aped drives the driver via Provision (the front resolves), so it passes nil.
	Resolve SpecResolver
	// Netns re-creates a workspace's egress namespace when Start finds it missing
	// (after a host reboot). nil → no recovery: Start on such a workspace fails in
	// the shim, as it did before this existed.
	Netns NetnsEnsurer
}

// InteractiveBackend is a Backend that can open an interactive process — an exec
// command or the attach login shell — whose stdio the caller relays (PLAN-18 D2).
// Only the containerd driver implements it (a Kata task exec with a PTY); the
// nerdctl shellDriver does not, so the executor reports UNSUPPORTED there. The
// returned Process is owned by the caller: relay its stdio, then let Wait clean
// it up.
type InteractiveBackend interface {
	OpenExec(ctx context.Context, id string, req workspace.ExecRequest) (workspace.Process, error)
	OpenAttach(ctx context.Context, id string, req workspace.AttachRequest) (workspace.Process, error)
}

// ProvisioningBackend is a workspace.Backend that also provisions a resolved
// spec and owns a client connection to close. It is the containerd driver's
// shape: unlike the nerdctl shellDriver (a Backend + a separate Runner), the
// containerd client serves both the id-verbs and the privileged Create from one
// connection. aped uses it as its Backend AND its Provisioner (PLAN-18 D3).
type ProvisioningBackend interface {
	workspace.Backend
	// Provision creates + starts a workspace from a fully-resolved spec — the
	// barrier-3-free Create the aped executor invokes as its Provisioner.
	Provision(ctx context.Context, spec WorkspaceSpec) (workspace.Workspace, error)
	// Close releases the containerd client connection.
	Close() error
}

// NetnsEnsurer re-creates a workspace's egress network namespace on demand. It is
// declared HERE, not imported from the netd package, because netd imports this one —
// aped wires its netd client in at construction.
//
// Why the driver needs it at all: a named netns lives in /run, so it survives an aped
// or helper restart but NOT a host reboot. After a reboot the container and its
// snapshot are still there, and its spec still names /run/netns/ape-<name> — which no
// longer exists, so starting it would fail deep in the Kata shim ("failed to set into
// network namespace … invalid argument"). Re-ensuring on Start turns that into a
// recoverable workspace.
type NetnsEnsurer interface {
	EnsureNetns(ctx context.Context, workspace string, proxyPort int, reuse bool) (string, error)
}

// Reconciler is implemented by a backend that can re-align its workspace registry
// with the container runtime's actual state (PLAN-22 D5c). aped runs it once at
// startup: containerd survives an aped restart, but a workspace destroyed
// out-of-band would otherwise linger in `ape sandbox ls` forever.
//
// It is a SEPARATE interface, not a ProvisioningBackend method, because only the
// containerd driver can implement it — the nerdctl shellDriver has no cheap,
// reliable way to enumerate container existence, and forcing a stub on it would
// invite a no-op that silently looks like reconciliation.
type Reconciler interface {
	Reconcile(ctx context.Context) (ReconcileReport, error)
}

// ReconcileReport is what one reconciliation pass did.
type ReconcileReport struct {
	// Checked is how many registry rows were examined.
	Checked int
	// Dropped names the workspaces whose container was gone, so their registry row
	// was removed.
	Dropped []string
}
