package sprintboard

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/sprint"
)

// countColumns is how many counter cells sit per row. Four keeps the seven
// story statuses to two tidy rows and still reads on a narrow sidebar; the ui
// grid collapses to one column under 700px regardless.
const countColumns = 4

// tones maps a status to a board colour TOKEN — never a hex. There are two
// themes and a project may patch either, so a colour chosen here would be
// right in one and illegible in the others, and nothing would report it.
//
// blocked is danger and cancelled is dim on purpose: they are the two the
// counters exist to keep visible and to keep from being mistaken for progress.
var tones = map[string]string{
	sprint.StatusBacklog:     "muted",
	sprint.StatusReadyForDev: "accent",
	sprint.StatusInProgress:  "accent",
	sprint.StatusReview:      "mark",
	sprint.StatusDone:        "agent",
	sprint.StatusBlocked:     "danger",
	sprint.StatusCancelled:   "dim",
}

// buildState renders a Summary as a `ui` component tree.
//
// FRESHNESS IS IN THE TREE, NOT IN state.heartbeat, and that is a deliberate
// departure from the request that asked for it. `heartbeat` and `readOnly` are
// declared by `kanban` (and `readOnly` by `table`); the `ui` renderer imports
// neither, so both would be stored and drawn by nothing — `aboard apply` says
// exactly that, twice, on every write. A freshness strip that renders nothing
// fails the requirement completely, where a line in the panel satisfies what
// the requirement is FOR: a view that cannot say when it was last true is one
// that lies during exactly the long run it exists for.
//
// `readOnly` is not written for the same reason, and is not needed: this tree
// contains no `field` and no `button`, so there is nothing on it to edit.
func buildState(s sprint.Summary, now time.Time) map[string]any {
	return map[string]any{
		"root": map[string]any{
			"type": "col",
			"gap":  "16px",
			"children": []any{
				storiesCard(s),
				epicsCard(s),
				inFlightCard(s),
				attentionCard(s),
				freshnessCard(now, s),
			},
		},
		// Declared so the tab's state matches what the renderer reads. An
		// absent `data` is legal; an empty one says "nothing is bound here".
		"data": map[string]any{},
	}
}

func storiesCard(s sprint.Summary) map[string]any {
	cells := make([]any, 0, len(sprint.SummaryStatuses))
	for _, name := range sprint.SummaryStatuses {
		cells = append(cells, map[string]any{
			"type":  "stat",
			"value": strconv.Itoa(s.StoryCounts[name]),
			"label": name,
			"tone":  tones[name],
		})
	}
	children := []any{
		map[string]any{"type": "grid", "columns": countColumns, "gap": "14px", "children": cells},
	}
	// An unrecognised status is a tracker problem. Named rather than folded
	// into a total, because folding it in is how it survives unnoticed.
	if len(s.StoryUnknown) > 0 {
		names := make([]string, 0, len(s.StoryUnknown))
		for name, n := range s.StoryUnknown {
			names = append(names, fmt.Sprintf("%s (%d)", name, n))
		}
		sort.Strings(names)
		children = append(children, map[string]any{
			"type":  "notice",
			"label": "Statuses outside the vocabulary:",
			"value": strings.Join(names, ", ") + " — these count in the total but in no cell above.",
			"tone":  "danger",
		})
	}
	return map[string]any{
		"type":     "card",
		"title":    fmt.Sprintf("Stories — %d", s.Stories),
		"accent":   "accent",
		"children": children,
	}
}

func epicsCard(s sprint.Summary) map[string]any {
	names := make([]string, 0, len(s.EpicCounts))
	for name := range s.EpicCounts {
		names = append(names, name)
	}
	sort.Strings(names)
	cells := make([]any, 0, len(names))
	for _, name := range names {
		cells = append(cells, map[string]any{
			"type":  "stat",
			"value": strconv.Itoa(s.EpicCounts[name]),
			"label": name,
			"tone":  tones[name],
		})
	}
	if len(cells) == 0 {
		cells = append(cells, map[string]any{"type": "caption", "value": "no epics in the tracker"})
	}
	return map[string]any{
		"type":  "card",
		"title": fmt.Sprintf("Epics — %d", s.Epics),
		"children": []any{
			map[string]any{"type": "grid", "columns": countColumns, "gap": "14px", "children": cells},
			map[string]any{
				"type":  "caption",
				"value": "Derived from each epic's story rows, not from the epic row's recorded status.",
			},
		},
	}
}

func inFlightCard(s sprint.Summary) map[string]any {
	body := []any{}
	if len(s.InFlight) == 0 {
		body = append(body, map[string]any{
			"type":  "caption",
			"value": "Nothing in flight — every story is done or cancelled.",
		})
	} else {
		body = append(body, storyTable(s.InFlight))
	}
	return map[string]any{
		"type":     "card",
		"title":    fmt.Sprintf("In flight — %d", len(s.InFlight)),
		"children": body,
	}
}

func attentionCard(s sprint.Summary) map[string]any {
	body := []any{}
	if len(s.Attention) == 0 {
		// Rendered, never hidden. An empty panel is a valid and good state;
		// an ABSENT one is ambiguous — the reader cannot tell "nothing is
		// waiting" from "this stopped being computed".
		body = append(body, map[string]any{
			"type":  "caption",
			"value": "Nothing waiting on a person.",
		})
	} else {
		body = append(body, storyTable(s.Attention))
	}
	body = append(body, map[string]any{
		"type": "caption",
		"value": "Blocked stories only. `reopened_by` lives in story frontmatter and parked " +
			"patches in the deferred store — neither is in the tracker, so neither is counted here.",
		"tone": "dim",
	})
	return map[string]any{
		"type":     "card",
		"title":    fmt.Sprintf("Attention — %d", len(s.Attention)),
		"children": body,
	}
}

func storyTable(refs []sprint.StoryRef) map[string]any {
	rows := make([]any, 0, len(refs))
	for _, r := range refs {
		rows = append(rows, map[string]any{
			"id":     r.Key,
			"story":  r.Key,
			"status": r.Status,
			"epic":   strconv.Itoa(r.Epic),
		})
	}
	return map[string]any{
		"type": "table",
		"columns": []any{
			map[string]any{"id": "story", "label": "Story"},
			map[string]any{"id": "status", "label": "Status"},
			map[string]any{"id": "epic", "label": "Epic"},
		},
		"rows": rows,
	}
}

func freshnessCard(now time.Time, s sprint.Summary) map[string]any {
	stamp := now.UTC().Format(time.RFC3339)
	return map[string]any{
		"type":  "card",
		"title": "Freshness",
		"children": []any{
			map[string]any{
				"type": "kv",
				"pairs": []any{
					map[string]any{"key": "refreshed", "value": stamp},
					map[string]any{"key": "by", "value": Actor + " (`ape sprint reconcile`)"},
					map[string]any{"key": "source", "value": "sprint-status.yaml"},
					map[string]any{"key": "stories counted", "value": strconv.Itoa(s.Stories)},
				},
			},
			map[string]any{
				"type": "caption",
				"value": "This tab is refreshed by `ape sprint reconcile`, which runs at every boundary " +
					"that moves a story. If that timestamp stops advancing during a run, the run has " +
					"stopped moving stories — the tab is not being maintained by anything else.",
			},
		},
	}
}
