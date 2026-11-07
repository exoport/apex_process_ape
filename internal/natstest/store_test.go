package natstest

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// V8-WIN-4: a store file still in use when the test ends is removed once
// it is let go, not reported at the first attempt; one that stays in use
// is still reported.
func TestRemoveAll_TriesAgainWhileAFileIsInUse(t *testing.T) {
	inUse := errors.New("the process cannot access the file because it is being used by another process")
	calls := 0
	err := removeAll("d", func(string) error {
		calls++
		if calls < 3 {
			return inUse
		}
		return nil
	}, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 3, calls)

	err = removeAll("d", func(string) error { return inUse }, 300*time.Millisecond)
	require.ErrorIs(t, err, inUse, "the control: still in use past the bound is an error")
}
