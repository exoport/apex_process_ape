package apecmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// D732: the detached run log dates every line, inside a step too; a line
// longer than the buffer still goes through, so the run never blocks.
func TestStampLines(t *testing.T) {
	at := time.Date(2026, 11, 2, 4, 19, 51, 0, time.UTC)
	var out bytes.Buffer
	stampLines(&out, strings.NewReader("[326ms]   step 1: apex-loop\nape: the lease on M8 was released by this checkout\nno newline"), func() time.Time { return at })
	require.Equal(t, "2026-11-02T04:19:51Z [326ms]   step 1: apex-loop\n2026-11-02T04:19:51Z ape: the lease on M8 was released by this checkout\n2026-11-02T04:19:51Z no newline\n", out.String())

	out.Reset()
	long := strings.Repeat("x", 5<<20)
	stampLines(&out, strings.NewReader("first\n"+long+"\nafter\n"), func() time.Time { return at })
	require.True(t, strings.HasPrefix(out.String(), "2026-11-02T04:19:51Z first\n"))
	require.Contains(t, out.String(), "after", "the rest is copied, nothing lost")
}
