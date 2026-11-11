package member

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// B3 (ANSWER-D751): a loop's hold that finds its claimed milestone's lease
// lapsed between runs re-creates it (claimFresh) and now says so, with when
// the lease was last renewed — in 1b nothing in m9's files showed M9's
// lease had been free for 2 h 40 min. The control: a hold right after the
// claim, the lease live, says nothing of the kind.
func TestHoldMilestone_NotesARetakeAfterALapse(t *testing.T) {
	shortLeasesTTL(t, lapseTTL, 250*time.Millisecond)
	ctx := context.Background()
	url := leaseCluster(t)
	for _, tc := range []struct {
		name  string
		lapse bool
	}{{"lapsed", true}, {"control-live", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ann := checkout(t, url, "ann-"+tc.name)
			cfg, err := os.ReadFile(apexv1.ConfigPath(ann.Root))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(apexv1.ConfigPath(ann.Root), []byte(strings.Replace(string(cfg), "Notes Svc", "Retake "+tc.name, 1)), 0o644))
			_, err = ClaimMilestone(ctx, ann, "M1")
			require.NoError(t, err)
			if tc.lapse {
				time.Sleep(5 * MilestoneTTL / 2) // past the TTL, nothing renewing
			}
			var log syncBuf
			stop, err := HoldMilestone(ctx, ann, "M1", &log)
			require.NoError(t, err, "its own claim: taken again, no person act")
			stop()
			if tc.lapse {
				require.Contains(t, log.String(), "M1's lease had lapsed")
				require.Contains(t, log.String(), "claimed again")
				return
			}
			require.NotContains(t, log.String(), "had lapsed")
		})
	}
}
