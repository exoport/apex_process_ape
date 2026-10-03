package sessiondriver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// neverReady blocks until its context ends, like a claude that never draws
// its REPL.
func neverReady(ctx context.Context) error {
	<-ctx.Done()
	return errNotReady
}

var errNotReady = errors.New("REPL not ready")

// A ceiling shorter than the base window ends the wait at the ceiling, and
// says so: the termination is max-duration, with the readiness failure
// still reachable as the cause.
func TestWaitReady_CeilingShorterThanBaseCutsTheWaitShort(t *testing.T) {
	start := time.Now()
	err := WaitReady(context.Background(), time.Minute, 50*time.Millisecond, "interactive step", neverReady)
	require.Less(t, time.Since(start), 5*time.Second, "waited past the ceiling")

	var mde *MaxDurationError
	require.ErrorAs(t, err, &mde)
	require.Equal(t, 50*time.Millisecond, mde.Max)
	require.Equal(t, "interactive step", mde.Label)
	require.ErrorIs(t, err, errNotReady)
}

// With no ceiling, or one longer than the base window, the base window
// ends the wait and the error is the readiness failure itself.
func TestWaitReady_BaseWindowWhenTheCeilingIsNotShorter(t *testing.T) {
	for name, maxDuration := range map[string]time.Duration{
		"no ceiling":     0,
		"longer ceiling": time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			err := WaitReady(context.Background(), 50*time.Millisecond, maxDuration, "session", neverReady)
			require.ErrorIs(t, err, errNotReady)
			var mde *MaxDurationError
			require.NotErrorAs(t, err, &mde)
		})
	}
}

// A ready REPL is a nil error whatever the ceiling.
func TestWaitReady_Ready(t *testing.T) {
	ready := func(context.Context) error { return nil }
	require.NoError(t, WaitReady(context.Background(), time.Minute, time.Millisecond, "session", ready))
}

// A cancelled run is not a ceiling hit, even when the ceiling was armed.
func TestWaitReady_CancelledRunIsNotACeilingHit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := WaitReady(ctx, time.Minute, time.Second, "session", neverReady)
	var mde *MaxDurationError
	require.NotErrorAs(t, err, &mde)
	require.ErrorIs(t, err, errNotReady)
}
