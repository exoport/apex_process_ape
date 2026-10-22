package usage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"time"
)

// A run's readings. The status-line command of a session ape launches for
// a run carries a launch key (`ape usage record --run <key>`), chosen
// before the settings are built — the run id does not exist yet then. The
// ape process names the run under that key once it has one (BeginRun);
// the recorder keeps the first and the last reading under it (RecordRun);
// the ape process takes them when the run ends (EndRun), into the run
// manifest's usage: {before, after}, and into the history share: auto
// learns from (AppendHistory).

// RunsDir is <user cache>/ape/usage-runs, one file per launch key.
func RunsDir() string { return filepath.Join(Dir(), "usage-runs") }

// HistoryPath is <user cache>/ape/usage-history.json.
func HistoryPath() string { return filepath.Join(Dir(), "usage-history.json") }

// keyRe is what a launch key may hold: it is a file name.
var keyRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// ValidKey reports whether key can name a run file.
func ValidKey(key string) bool { return keyRe.MatchString(key) }

// NewKey is a fresh launch key.
func NewKey() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(b)
}

// runFile is one launch key's record.
type runFile struct {
	Run   string   `json:"run"`
	Skill string   `json:"skill"`
	First *Reading `json:"first"`
	Last  *Reading `json:"last"`
}

func runPath(key string) string { return filepath.Join(RunsDir(), key+".json") }

func loadRun(key string) *runFile {
	rf := &runFile{}
	data, err := os.ReadFile(runPath(key))
	if err != nil {
		return rf
	}
	if json.Unmarshal(data, rf) != nil {
		return &runFile{}
	}
	return rf
}

// staleRunFiles is how old a run file is when a run that never ended (a
// killed ape) left it: it is removed.
const staleRunFiles = 7 * 24 * time.Hour

// BeginRun names the run (its id and skill) a launch key records for.
// Readings the recorder already kept under the key stay.
func BeginRun(key, run, skill string) error {
	if !ValidKey(key) {
		return nil
	}
	pruneRunFiles(time.Now())
	rf := loadRun(key)
	rf.Run, rf.Skill = run, skill
	return writeJSON(runPath(key), rf)
}

// RunOf is the run id a launch key records for ("" when none is named).
func RunOf(key string) string {
	if !ValidKey(key) {
		return ""
	}
	return loadRun(key).Run
}

// RecordRun keeps r as the launch key's last reading, and as its first
// when it has none. Concurrent status-line commands each write a whole
// file; the worst a race costs is a first or last reading a few hundred
// milliseconds off.
func RecordRun(key string, r *Reading) error {
	if !ValidKey(key) {
		return nil
	}
	rf := loadRun(key)
	if rf.First == nil {
		rf.First = r
	}
	if rf.Last == nil || !r.At.Before(rf.Last.At) {
		rf.Last = r
	}
	return writeJSON(runPath(key), rf)
}

// EndRun takes the launch key's first and last reading and removes its
// file.
func EndRun(key string) (first, last *Reading) {
	if !ValidKey(key) {
		return nil, nil
	}
	rf := loadRun(key)
	_ = os.Remove(runPath(key))
	return rf.First, rf.Last
}

func pruneRunFiles(now time.Time) {
	entries, err := os.ReadDir(RunsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleRunFiles {
			_ = os.Remove(filepath.Join(RunsDir(), e.Name()))
		}
	}
}

// HistoryEntry is one ended run's readings.
type HistoryEntry struct {
	Skill  string    `json:"skill"`
	Run    string    `json:"run"`
	At     time.Time `json:"at"`
	Before *Reading  `json:"before"`
	After  *Reading  `json:"after"`
}

// historyCap bounds the history file.
const historyCap = 500

func loadHistory() []HistoryEntry {
	data, err := os.ReadFile(HistoryPath())
	if err != nil {
		return nil
	}
	var h []HistoryEntry
	if json.Unmarshal(data, &h) != nil {
		return nil
	}
	return h
}

// AppendHistory records an ended run's readings, for share: auto. A run
// with no reading, or only one, is not recorded: it measures nothing.
func AppendHistory(e HistoryEntry) error {
	if e.Before == nil || e.After == nil || e.Skill == "" {
		return nil
	}
	h := append(loadHistory(), e)
	if len(h) > historyCap {
		h = h[len(h)-historyCap:]
	}
	return writeJSON(HistoryPath(), h)
}

// The share a launch is estimated to use of each window when the history
// cannot say: below ShareMinRuns measured runs of the skill, DefaultShare.
const (
	DefaultShare = 0.05
	ShareMinRuns = 3
	shareRuns    = 10
)

// Share is a launch's estimated use of each window.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type Share struct {
	FiveHour float64 `json:"five_hour" yaml:"five_hour"`
	SevenDay float64 `json:"seven_day" yaml:"seven_day"`
	// Source is "config" (a number in limits.usage.share), "auto" (the
	// skill's median), or "default" (share: auto with fewer than
	// ShareMinRuns measured runs of the skill: DefaultShare).
	Source string `json:"source" yaml:"source"`
	// Runs is how many runs the auto median was taken over, per window.
	FiveHourRuns int `json:"five_hour_runs,omitempty" yaml:"five_hour_runs,omitempty"`
	SevenDayRuns int `json:"seven_day_runs,omitempty" yaml:"seven_day_runs,omitempty"`
}

// AutoShare is share: auto for skill: per window, the median of (last −
// first reading) over the skill's last 10 recorded runs on this account
// whose window did not roll over between the two readings; DefaultShare
// for a window with fewer than ShareMinRuns such runs.
func AutoShare(skill string) Share {
	var five, seven []float64
	h := loadHistory()
	for i := len(h) - 1; i >= 0 && (len(five) < shareRuns || len(seven) < shareRuns); i-- {
		e := h[i]
		if e.Skill != skill || e.Before == nil || e.After == nil {
			continue
		}
		if d, ok := delta(e.Before.FiveHour, e.After.FiveHour, e.Before.FiveHourResetsAt, e.After.FiveHourResetsAt); ok && len(five) < shareRuns {
			five = append(five, d)
		}
		if d, ok := delta(e.Before.SevenDay, e.After.SevenDay, e.Before.SevenDayResetsAt, e.After.SevenDayResetsAt); ok && len(seven) < shareRuns {
			seven = append(seven, d)
		}
	}
	s := Share{Source: "auto", FiveHourRuns: len(five), SevenDayRuns: len(seven)}
	s.FiveHour, s.SevenDay = medianOr(five), medianOr(seven)
	if len(five) < ShareMinRuns && len(seven) < ShareMinRuns {
		s.Source = "default"
	}
	return s
}

// delta is a window's use between two readings, when both carry it and
// the window did not reset in between.
func delta(before, after *float64, beforeReset, afterReset *time.Time) (float64, bool) {
	if before == nil || after == nil {
		return 0, false
	}
	if (beforeReset == nil) != (afterReset == nil) || (beforeReset != nil && !beforeReset.Equal(*afterReset)) {
		return 0, false
	}
	d := *after - *before
	if d < 0 {
		return 0, false
	}
	return d, true
}

func medianOr(v []float64) float64 {
	if len(v) < ShareMinRuns {
		return DefaultShare
	}
	v = slices.Clone(v)
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
