package apexv1

import (
	"strings"
	"time"
)

// CIEvent is a merge queue event as `ape ci event` and `ape ci watch`
// publish it (D876, research's DIM point 3): one builder, so the two carry
// the same payload by construction. name is merged, ejected or
// close-merged; ref and sha what it is about (Reason "<ref> <sha>"); the
// milestone is given, else read from a milestone/<M> ref. Seq and At are
// the moment now.
func CIEvent(name, ref, sha, milestone string, now time.Time) Event {
	if milestone == "" {
		if m, ok := strings.CutPrefix(ref, MilestoneBranchPrefix); ok {
			milestone = m
		}
	}
	return Event{Seq: now.UnixNano(), At: now.UTC().Format(time.RFC3339), Event: name, Milestone: milestone, Reason: strings.TrimSpace(ref + " " + sha)}
}
