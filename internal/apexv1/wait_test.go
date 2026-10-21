package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// --until is comma = any, each condition once, in the order given; an
// unknown one or none is refused.
func TestParseUntil(t *testing.T) {
	got, err := ParseUntil(" person,stopped ,person")
	require.NoError(t, err)
	require.Equal(t, []string{"person", "stopped"}, got)
	_, err = ParseUntil("person,people")
	require.ErrorContains(t, err, `"people" is not a condition`)
	_, err = ParseUntil(" , ")
	require.ErrorContains(t, err, "names no condition")
}

// The levels, and which events decide a condition alone.
func TestWaitLevel(t *testing.T) {
	r := &StatusReport{}
	l := WaitLevel{Report: r}
	require.True(t, l.Holds(UntilStopped), "no run is live")
	require.False(t, l.Holds(UntilDone), "a roadmap with no milestone is not done")
	require.False(t, l.Holds(UntilPerson))
	require.False(t, l.Holds(UntilEvent), "event has no level")
	r.Milestones = []MilestoneLine{{ID: "M1", Status: MilestoneDone}, {ID: "M2", Status: MilestoneProposed}}
	require.False(t, l.Holds(UntilDone))
	r.Milestones[1].Status = MilestoneDone
	require.True(t, l.Holds(UntilDone))
	r.Changes = []ChangeStatus{{ID: "a", Status: StatusBlocked}}
	require.True(t, l.Holds(UntilBlocked))
	l.Running = []string{"r1"}
	require.False(t, l.Holds(UntilStopped))

	require.True(t, EventMeets(UntilPerson, Event{Event: EventBlock}))
	require.False(t, EventMeets(UntilBlocked, Event{Event: EventPerson}))
	require.True(t, EventMeets(UntilStopped, Event{Event: EventRunLost}))
	require.True(t, EventNeedsLevel(UntilStopped))
	require.True(t, EventNeedsLevel(UntilDone))
	require.False(t, EventNeedsLevel(UntilPerson))
	require.True(t, EventMeets(UntilEvent, Event{Event: EventGateGreen}))
}
