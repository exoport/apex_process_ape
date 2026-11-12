package apecmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/member"
)

// A merged close whose lease release failed says so, naming the act that
// releases it; it no longer drops the error (V15-REL-1, research's hole F).
func TestCloseReleaseNote(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"released", nil, ""},
		{"no-ensemble", fmt.Errorf("open: %w", member.ErrNotMember), ""},
		{"stale", fmt.Errorf("%w: renewed 10 times", lease.ErrStale), "ape: M1 closed, but its lease could not be released (lease: stale token: renewed 10 times): release it with `ape milestone release M1`\n"},
		{"nats-down", errors.New("nats: no servers available for connection"), "could not be released (nats: no servers available for connection)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			closeReleaseNote(&b, "M1", tc.err)
			if tc.want == "" {
				require.Empty(t, b.String())
				return
			}
			require.Contains(t, b.String(), tc.want)
		})
	}
}
