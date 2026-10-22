// Package usage is ape's record of the claude.ai plan's rate limits (W4,
// v1.1: usage-aware launching).
//
// Claude Code hands a status-line command a JSON object on stdin, and from
// the first API response of a session on it carries
//
//	"rate_limits": {"five_hour": {"used_percentage": 13, "resets_at": 1792626000},
//	                "seven_day": {"used_percentage": 75, "resets_at": 1792785600}}
//
// (measured on Claude Code 2.1.306: used_percentage a whole number 0–100,
// resets_at epoch seconds; before the first response the key is absent,
// not null). Every session ape launches gets `ape usage record` as its
// status line (--settings, ape's own sessions only); the recorder keeps the
// newest reading in <user cache>/ape/usage.json, per OS user, and the
// first and last reading of the run that launched the session, which
// becomes the run manifest's usage: {before, after} and the history
// `limits.usage.share: auto` learns from.
//
// Every path here is under os.UserCacheDir(): the reading belongs to the
// account the person is signed in with, not to a project.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
)

// Reading is one statusline reading of the plan's two windows. A window
// is a fraction 0..1 (Claude Code's used_percentage over 100); nil when
// the reading did not carry it.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type Reading struct {
	At               time.Time  `json:"at" yaml:"at"`
	FiveHour         *float64   `json:"five_hour" yaml:"five_hour"`
	FiveHourResetsAt *time.Time `json:"five_hour_resets_at" yaml:"five_hour_resets_at"`
	SevenDay         *float64   `json:"seven_day" yaml:"seven_day"`
	SevenDayResetsAt *time.Time `json:"seven_day_resets_at" yaml:"seven_day_resets_at"`
	// Run is the ape run whose session recorded it ("" for a session ape
	// launched without a run record: a chat, a prompt).
	Run string `json:"run" yaml:"run,omitempty"`
	// ClaudeVersion is the statusline JSON's version.
	ClaudeVersion string `json:"claude_version" yaml:"claude_version,omitempty"`
}

// Dir is <user cache>/ape, where every usage file lives (the startup
// probe's directory): os.UserCacheDir, else the temp dir.
func Dir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "ape")
	}
	return filepath.Join(os.TempDir(), "ape")
}

// Path is <user cache>/ape/usage.json, the newest reading.
func Path() string { return filepath.Join(Dir(), "usage.json") }

// statusline is the part of Claude Code's status-line JSON ape reads.
//
//nolint:tagliatelle // Claude Code's own field names
type statusline struct {
	Version    string `json:"version"`
	RateLimits *struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
	} `json:"rate_limits"`
}

// window is one rate-limit window. used_percentage is a whole number on
// 2.1.306; a float is accepted too.
//
//nolint:tagliatelle // Claude Code's own field names
type window struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *float64 `json:"resets_at"`
}

// ErrNoRateLimits: the statusline JSON carries no rate_limits (before the
// session's first API response, or a plan without them).
var ErrNoRateLimits = errors.New("no rate_limits in the status-line JSON")

// Parse reads a status-line JSON object into a reading taken at now.
func Parse(data []byte, now time.Time) (*Reading, error) {
	var s statusline
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("status-line JSON: %w", err)
	}
	if s.RateLimits == nil {
		return nil, ErrNoRateLimits
	}
	r := &Reading{At: now.UTC(), ClaudeVersion: s.Version}
	r.FiveHour, r.FiveHourResetsAt = s.RateLimits.FiveHour.parse()
	r.SevenDay, r.SevenDayResetsAt = s.RateLimits.SevenDay.parse()
	if r.FiveHour == nil && r.SevenDay == nil {
		return nil, ErrNoRateLimits
	}
	return r, nil
}

func (w *window) parse() (*float64, *time.Time) {
	if w == nil || w.UsedPercentage == nil {
		return nil, nil
	}
	pct := *w.UsedPercentage
	if math.IsNaN(pct) || pct < 0 {
		return nil, nil
	}
	frac := pct / 100
	var at *time.Time
	if w.ResetsAt != nil && *w.ResetsAt > 0 {
		t := time.Unix(int64(*w.ResetsAt), 0).UTC()
		at = &t
	}
	return &frac, at
}

// Load is the newest reading, nil when there is none (or it cannot be
// read: the guard then decides as for no reading).
func Load() *Reading {
	data, err := os.ReadFile(Path())
	if err != nil {
		return nil
	}
	var r Reading
	if json.Unmarshal(data, &r) != nil || r.At.IsZero() {
		return nil
	}
	return &r
}

// refreshEvery is how stale an unchanged reading may get before it is
// written again: Claude Code runs the status line on every message update,
// and the reading's age only needs to be good to the minute.
const refreshEvery = 30 * time.Second

// Save writes r as the newest reading, atomically (concurrent sessions
// each rename a whole file into place). A reading older than the one on
// disk is not written, and neither is one equal to it taken within
// refreshEvery.
func Save(r *Reading) error {
	if cur := Load(); cur != nil {
		if r.At.Before(cur.At) {
			return nil
		}
		if r.At.Sub(cur.At) < refreshEvery && sameWindows(cur, r) && cur.Run == r.Run {
			return nil
		}
	}
	return writeJSON(Path(), r)
}

func sameWindows(a, b *Reading) bool {
	return eqF(a.FiveHour, b.FiveHour) && eqF(a.SevenDay, b.SevenDay) &&
		eqT(a.FiveHourResetsAt, b.FiveHourResetsAt) && eqT(a.SevenDayResetsAt, b.SevenDayResetsAt)
}

func eqF(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func eqT(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err //nolint:wrapcheck // cannot fail for these types
	}
	return atomicfile.Write(path, append(data, '\n'))
}

// Age is how long ago r was taken.
func (r *Reading) Age(now time.Time) time.Duration { return now.Sub(r.At) }

// StatusLine is what the recorder prints for Claude Code to show:
// "5h 14% · 7d 55%".
func (r *Reading) StatusLine() string {
	return r.windows(func(v float64) string { return strconv.Itoa(int(math.Round(v*100))) + "%" })
}

// Short is the reading for `ape status`: "5h 0.14 · 7d 0.55".
func (r *Reading) Short() string {
	return r.windows(func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) })
}

// windows joins the windows the reading carries, each said by say.
func (r *Reading) windows(say func(float64) string) string {
	var parts []string
	if r.FiveHour != nil {
		parts = append(parts, "5h "+say(*r.FiveHour))
	}
	if r.SevenDay != nil {
		parts = append(parts, "7d "+say(*r.SevenDay))
	}
	return strings.Join(parts, " · ")
}

// Ago says a duration as a person reads a reading's age: "just now",
// "2 min ago", "3 h ago".
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 2*24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days ago"
	}
}

// Minutes says a duration the way limits.usage.max_age reads: "10 min",
// else Go's own form.
func Minutes(d time.Duration) string {
	if d > 0 && d%time.Minute == 0 {
		return strconv.Itoa(int(d/time.Minute)) + " min"
	}
	return d.String()
}

// Record is `ape usage record`: data is the status-line JSON, key the
// launch key the command carries ("" for none). It keeps the reading as
// the newest (usage.json) and under the key, and returns it; a JSON with
// no rate_limits records nothing (ErrNoRateLimits).
func Record(data []byte, key string, now time.Time) (*Reading, error) {
	r, err := Parse(data, now)
	if err != nil {
		return nil, err
	}
	r.Run = RunOf(key)
	if err := Save(r); err != nil {
		return r, err
	}
	return r, RecordRun(key, r)
}
