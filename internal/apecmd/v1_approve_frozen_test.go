package apecmd

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// v1.2 F4: approve's human line says what the approval froze, or that a
// no-op approve recorded nothing.
func TestFrozenLine(t *testing.T) {
	f := &apexv1.FrozenGate{GateFiles: []string{"scripts/gate.sh"}, GateOwnFiles: []string{"scripts/checks/one.checks", "scripts/lib/gate.sh"}, GateGlobs: []string{}}
	require.Equal(t, "froze 3 files: scripts/gate.sh, scripts/checks/one.checks, scripts/lib/gate.sh", frozenLine("recorded", "x", f))
	require.Equal(t, "unchanged since 2026-10-23T10:00:00Z: nothing to re-approve", frozenLine("unchanged", "2026-10-23T10:00:00Z", f))
	require.Empty(t, frozenLine("recorded", "", nil))

	many := &apexv1.FrozenGate{GateFiles: []string{"g.sh"}, GateOwnFiles: []string{"a", "b", "c", "d", "e", "f", "h"}, GateGlobs: []string{"scripts/checks/*.checks"}}
	require.Equal(t, "froze 8 files: g.sh, a, b, c, d, e (+2 more); and any new match of scripts/checks/*.checks", frozenLine("recorded", "", many))
	require.Equal(t, "froze 1 file: g.sh", frozenLine("recorded", "", &apexv1.FrozenGate{GateFiles: []string{"g.sh"}}))
}
