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
