package sprint_test

import (
	"testing"

	"github.com/exoport/apex_process_ape/internal/sprint"
	"github.com/stretchr/testify/require"
)

func story(key, status string, epic int) sprint.Row {
	return sprint.Row{Key: key, Status: status, Kind: sprint.KindStory, Epic: epic}
}

func epicRow(key, status string, epic int) sprint.Row {
	return sprint.Row{Key: key, Status: status, Kind: sprint.KindEpic, Epic: epic}
}

func TestSummarizeCountsStoriesByStatus(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		story("1-1", "done", 1),
		story("1-2", "in-progress", 1),
		story("1-3", "blocked", 1),
		story("2-1", "backlog", 2),
		story("2-2", "review", 2),
		story("2-3", "cancelled", 2),
	})
	require.Equal(t, 6, s.Stories)
	require.Equal(t, 1, s.StoryCounts["done"])
	require.Equal(t, 1, s.StoryCounts["in-progress"])
	require.Equal(t, 1, s.StoryCounts["blocked"])
	require.Equal(t, 1, s.StoryCounts["backlog"])
	require.Equal(t, 1, s.StoryCounts["review"])
	require.Equal(t, 1, s.StoryCounts["cancelled"])
}

// Every status in the vocabulary carries a zero, because a board that renders
// a missing key and a zero the same way cannot be read at a glance — and the
// two cells this matters most for are blocked and cancelled.
func TestSummarizeKeepsZeroCells(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{story("1-1", "done", 1)})
	for _, name := range sprint.SummaryStatuses {
		require.Contains(t, s.StoryCounts, name, "status %q has no cell", name)
	}
	require.Equal(t, 0, s.StoryCounts["blocked"])
	require.Equal(t, 0, s.StoryCounts["cancelled"])
}

// The tracker writes `drafted` where a story file writes `ready-for-dev`.
// They mean the same thing, and counting them as two statuses would split
// one column in half.
func TestSummarizeNormalisesDraftedToReadyForDev(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		story("1-1", "drafted", 1),
		story("1-2", "ready-for-dev", 1),
	})
	require.Equal(t, 2, s.StoryCounts["ready-for-dev"])
	require.NotContains(t, s.StoryCounts, "drafted")
}

// An unrecognised status is a tracker problem. It is named, not folded into a
// total — folding it in is how it survives unnoticed.
func TestSummarizeSurfacesUnknownStatuses(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		story("1-1", "done", 1),
		story("1-2", "marinating", 1),
	})
	require.Equal(t, 1, s.StoryUnknown["marinating"])
	require.Equal(t, 2, s.Stories, "an unknown status still counts as a story")
	for _, name := range sprint.SummaryStatuses {
		if name != "done" {
			require.Zero(t, s.StoryCounts[name], "%q absorbed an unknown status", name)
		}
	}
}

func TestSummarizeInFlightExcludesTerminalStories(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		story("1-1", "done", 1),
		story("1-2", "cancelled", 1),
		story("1-3", "in-progress", 1),
		story("1-4", "blocked", 1),
	})
	keys := make([]string, 0, len(s.InFlight))
	for _, r := range s.InFlight {
		keys = append(keys, r.Key)
	}
	require.Equal(t, []string{"1-3", "1-4"}, keys,
		"done and cancelled are the terminal states; blocked is not")
}

// Blocked work is what the Attention panel is for. A sprint of nothing but
// blocked stories must not read as healthy.
func TestSummarizeAttentionIsBlockedWork(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		story("1-1", "blocked", 1),
		story("1-2", "in-progress", 1),
	})
	require.Len(t, s.Attention, 1)
	require.Equal(t, "1-1", s.Attention[0].Key)
}

// Epic counts come from the PROJECTION, not from the epic row's recorded
// status — the row can be stale, which is the whole reason reconcile exists.
func TestSummarizeCountsEpicsByDerivedStatus(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		epicRow("epic-1", "backlog", 1), // stale: its stories are all done
		story("1-1", "done", 1),
		story("1-2", "done", 1),
		epicRow("epic-2", "done", 2), // stale the other way
		story("2-1", "in-progress", 2),
	})
	require.Equal(t, 2, s.Epics)
	require.Equal(t, 1, s.EpicCounts["done"], "epic-1 projects to done despite its row")
	require.Equal(t, 1, s.EpicCounts["in-progress"], "epic-2 projects to in-progress despite its row")
}

// The projection declines on an epic with no stories, and on one whose
// stories are all cancelled — both mean "leave it alone" rather than a
// status. The count still has to add up, so the row's own value is used.
func TestSummarizeFallsBackWhenTheProjectionDeclines(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		epicRow("epic-1", "backlog", 1), // no stories at all
		epicRow("epic-2", "in-progress", 2),
		story("2-1", "cancelled", 2), // no active stories
	})
	require.Equal(t, 2, s.Epics)
	require.Equal(t, 1, s.EpicCounts["backlog"])
	require.Equal(t, 1, s.EpicCounts["in-progress"])
	total := 0
	for _, n := range s.EpicCounts {
		total += n
	}
	require.Equal(t, s.Epics, total, "every epic must land in exactly one cell")
}

// Stable order, so a refresh that changes nothing produces an identical tab
// and the board records no write.
func TestSummarizeOrdersDeterministically(t *testing.T) {
	t.Parallel()
	rows := []sprint.Row{
		story("2-1", "in-progress", 2),
		story("1-2", "review", 1),
		story("1-1", "blocked", 1),
	}
	first := sprint.Summarize(rows)
	second := sprint.Summarize([]sprint.Row{rows[2], rows[0], rows[1]})
	require.Equal(t, first.InFlight, second.InFlight)
	require.Equal(t, []string{"1-1", "1-2", "2-1"},
		[]string{first.InFlight[0].Key, first.InFlight[1].Key, first.InFlight[2].Key})
}

// Only story rows are counted as stories. An epic row and a retrospective row
// are not stories, and counting them would inflate every cell.
func TestSummarizeIgnoresNonStoryRows(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize([]sprint.Row{
		epicRow("epic-1", "in-progress", 1),
		story("1-1", "done", 1),
		{Key: "1-retrospective", Status: "done", Kind: sprint.KindOther},
	})
	require.Equal(t, 1, s.Stories)
	require.Equal(t, 1, s.StoryCounts["done"])
}

func TestSummarizeEmptyTrackerIsAllZeroes(t *testing.T) {
	t.Parallel()
	s := sprint.Summarize(nil)
	require.Zero(t, s.Stories)
	require.Zero(t, s.Epics)
	require.Empty(t, s.InFlight)
	require.Empty(t, s.Attention)
	for _, name := range sprint.SummaryStatuses {
		require.Zero(t, s.StoryCounts[name])
	}
}
