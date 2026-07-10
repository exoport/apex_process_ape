package aped

import (
	"context"
	"fmt"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// NewShellProvisioner returns a Provisioner backed by the PLAN-16 Runner +
// Registry (the non-device shell tier): it provisions a fully-resolved spec
// (nerdctl run -d, byte-identical to the PLAN-16 path) and records it in the
// authoritative server-side registry. This is the privileged act the root
// executor performs on create; every other verb goes through the shellDriver
// Backend the executor also holds.
func NewShellProvisioner(runner *sandbox.Runner, reg *sandbox.Registry) Provisioner {
	return func(ctx context.Context, spec sandbox.WorkspaceSpec) (workspace.Workspace, error) {
		if err := runner.Provision(ctx, spec); err != nil {
			return workspace.Workspace{}, err
		}
		rec := sandbox.Workspace{
			Name:        spec.Name,
			Container:   spec.Container(),
			VMM:         string(spec.VMM),
			Image:       spec.Image,
			Mount:       string(spec.Mount),
			ProjectRoot: spec.ProjectRoot,
			Volume:      spec.Volume,
			CreatedAt:   now().Format(time.RFC3339),
		}
		if spec.Comp != nil {
			rec.StagingDir = spec.Comp.StagingDir
		}
		if reg != nil {
			if err := reg.Put(rec); err != nil {
				return workspace.Workspace{}, fmt.Errorf("aped: workspace provisioned but registry write failed: %w", err)
			}
		}
		runtime := ""
		if spec.VMM != "" {
			runtime = "kata-" + string(spec.VMM)
		}
		return workspace.Workspace{
			ID:      spec.Name,
			Name:    spec.Name,
			Image:   spec.Image,
			Runtime: runtime,
			Mount:   string(spec.Mount),
		}, nil
	}
}
