package apecmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// Q-2C-3 (D856): a supervisor's coordinators write to its log, beside its
// own stamped lines; V8n's "riding it out" lines must say when, and which
// coordinator (its id carries the pid), to be read against an outage.
func TestCoordinatorLog_StampsTimeAndID(t *testing.T) {
	var b bytes.Buffer
	coordLog(&b, "coord-mmr7-4242")("coordinator: coord.%s could not be renewed (%v)\n", "atomix-2c", "deadline")
	require.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ coord-mmr7-4242: coordinator: coord\.atomix-2c could not be renewed \(deadline\)\n$`, b.String())
}
