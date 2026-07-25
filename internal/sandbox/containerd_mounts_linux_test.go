//go:build linux

package sandbox

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
)

// TestContainerdMountsSkipsLegacyProjectBind covers the driver path aped actually
// runs (--driver containerd), where the duplicate mount was observed.
func TestContainerdMountsSkipsLegacyProjectBind(t *testing.T) {
	comp := &Composition{StagingDir: "/staging", GuestHome: "/sandbox/home"}
	spec := WorkspaceSpec{
		Name: "dev", Mount: MountHostFS, ProjectRoot: "/srv/workspaces/demo", Comp: comp,
		Mounts: []workspace.MountSpec{{Source: "/srv/workspaces/demo", Dest: "/workspace/demo"}},
	}
	dests := map[string]bool{}
	for _, m := range containerdMounts(spec) {
		dests[m.Destination] = true
	}
	assert.True(t, dests["/workspace/demo"], "the repo mount is applied")
	assert.False(t, dests[DefaultProjectDest],
		"the legacy /workspace bind would duplicate the repo and shadow the repo root")

	// A pre-PLAN-20 spec (no resolved mount list) keeps the legacy project bind.
	legacy := spec
	legacy.Mounts = nil
	dests = map[string]bool{}
	for _, m := range containerdMounts(legacy) {
		dests[m.Destination] = true
	}
	assert.True(t, dests[DefaultProjectDest])
}
