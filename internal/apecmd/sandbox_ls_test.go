package apecmd

import (
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
)

func TestSinceRendersAgeAndDistinguishesNever(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

	assert.Equal(t, "just now", since(stamp(10*time.Second), now))
	assert.Equal(t, "42m", since(stamp(42*time.Minute), now))
	assert.Equal(t, "5h", since(stamp(5*time.Hour), now))
	assert.Equal(t, "3d", since(stamp(72*time.Hour), now))
	// "never used" is materially different from "used a moment ago" and must not be
	// rendered as an age.
	assert.Equal(t, "never", since("", now))
	assert.Equal(t, "?", since("not-a-timestamp", now))
}

func TestFilterIdleUsesCreationWhenNeverUsed(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

	list := []workspace.Workspace{
		{Name: "busy", CreatedAt: at(72 * time.Hour), LastUsedAt: at(10 * time.Minute)},
		{Name: "stale", CreatedAt: at(72 * time.Hour), LastUsedAt: at(48 * time.Hour)},
		{Name: "untouched-old", CreatedAt: at(48 * time.Hour)},
		{Name: "untouched-new", CreatedAt: at(30 * time.Minute)},
	}
	names := func(ws []workspace.Workspace) []string {
		out := make([]string, 0, len(ws))
		for _, w := range ws {
			out = append(out, w.Name)
		}
		return out
	}

	// A days-old workspace someone used minutes ago is NOT idle — the whole reason ape
	// reports use instead of age.
	assert.Equal(t, []string{"stale", "untouched-old"}, names(filterIdle(list, 24*time.Hour, now)))
	assert.Equal(t, []string{"busy", "stale", "untouched-old", "untouched-new"}, names(filterIdle(list, time.Minute, now)))
}
