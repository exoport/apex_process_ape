package member

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// NATSGO-1: nats.go v1.55.0 (#2129) reports a request that ForceReconnect
// cuts as ErrDisconnected, where v1.53.1 said ErrConnectionClosed. The
// token ask's connection is NoReconnect, so it meets ErrConnectionClosed on
// either version (NATSGO-R2); the ErrDisconnected row keeps askUnanswered
// right should a reconnecting connection ever carry an ask. A reply that
// refuses still is not unanswered.
func TestAskUnanswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"no responders", nats.ErrNoResponders, true},
		{"timeout", nats.ErrTimeout, true},
		{"deadline", context.DeadlineExceeded, true},
		{"connection closed", nats.ErrConnectionClosed, true},
		{"cut by ForceReconnect (v1.55.0)", nats.ErrDisconnected, true},
		{"refused", errors.New("ghapp: the App service refused: repo not installed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("ghapp: ask for a token: %w", tc.err)
			require.Equal(t, tc.want, askUnanswered(wrapped))
		})
	}
}
