package lease

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// D732: the client-side expiry margin is ExpirySkew, at most a fifth of the
// lease's TTL — 2 s for the 30 s milestone and close leases, less for a
// short one, which would otherwise read as expired when written.
func TestSkewFor(t *testing.T) {
	require.Equal(t, 2*time.Second, skewFor(30*time.Second))
	require.Equal(t, 200*time.Millisecond, skewFor(time.Second))
}
