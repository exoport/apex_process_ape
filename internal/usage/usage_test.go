package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TempCache points os.UserCacheDir at a fresh directory for the test, on
// every platform, so no test reads or writes the real <user cache>/ape.
func TempCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)           // os.UserCacheDir on macOS ($HOME/Library/Caches)
	t.Setenv("XDG_CACHE_HOME", dir) // on Linux
	t.Setenv("LocalAppData", dir)   // on Windows
	require.True(t, filepath.IsAbs(Dir()))
	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cache, "ape"), Dir())
	return Dir()
}

// fixtureLines are Claude Code 2.1.306's status-line JSON objects, as a
// session sent them: the first before its first API response (no
// rate_limits key), the second after.
func fixtureLines(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "statusline-2.1.306.jsonl"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	require.Len(t, out, 2)
	return out
}

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// The measured JSON: used_percentage a whole number over 100, resets_at
// epoch seconds; before the first API response the key is absent.
func TestParse_Measured2_1_306(t *testing.T) {
	lines := fixtureLines(t)
	_, err := Parse(lines[0], now)
	require.ErrorIs(t, err, ErrNoRateLimits)

	r, err := Parse(lines[1], now)
	require.NoError(t, err)
	require.InDelta(t, 0.13, *r.FiveHour, 1e-9)
	require.InDelta(t, 0.75, *r.SevenDay, 1e-9)
	require.True(t, time.Unix(1792626000, 0).Equal(*r.FiveHourResetsAt))
	require.True(t, time.Unix(1792785600, 0).Equal(*r.SevenDayResetsAt))
	require.Equal(t, "2.1.306", r.ClaudeVersion)
	require.Equal(t, now, r.At)
	require.Equal(t, "5h 13% · 7d 75%", r.StatusLine())
	require.Equal(t, "5h 0.13 · 7d 0.75", r.Short())
}

func TestParse_FloatsAndOddInput(t *testing.T) {
	r, err := Parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":13.6,"resets_at":1792626000.0}}}`), now)
	require.NoError(t, err)
	require.InDelta(t, 0.136, *r.FiveHour, 1e-9)
	require.Nil(t, r.SevenDay)
	require.Equal(t, "5h 14%", r.StatusLine())

	for _, bad := range []string{``, `not json`, `{"rate_limits":null}`, `{"rate_limits":{}}`, `{"rate_limits":{"five_hour":{"resets_at":1}}}`} {
		_, err := Parse([]byte(bad), now)
		require.Error(t, err, bad)
	}
}

// Record keeps the reading in usage.json (with the run its key names) and
// under the key; a JSON with no rate_limits writes nothing.
func TestRecord_WritesTheFileAndTheRun(t *testing.T) {
	TempCache(t)
	lines := fixtureLines(t)
	_, err := Record(lines[0], "k1", now)
	require.ErrorIs(t, err, ErrNoRateLimits)
	require.NoFileExists(t, Path())
	require.NoDirExists(t, RunsDir())

	require.NoError(t, BeginRun("k1", "20261021-120000-abc", "apex-apply"))
	r, err := Record(lines[1], "k1", now)
	require.NoError(t, err)
	require.Equal(t, "20261021-120000-abc", r.Run)

	var onDisk map[string]any
	data, err := os.ReadFile(Path())
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &onDisk))
	for _, k := range []string{"at", "five_hour", "five_hour_resets_at", "seven_day", "seven_day_resets_at", "run", "claude_version"} {
		require.Contains(t, onDisk, k)
	}
	require.InDelta(t, 0.13, onDisk["five_hour"], 1e-9)
	got := Load()
	require.NotNil(t, got)
	require.Equal(t, "20261021-120000-abc", got.Run)

	later := []byte(`{"version":"2.1.306","rate_limits":{"five_hour":{"used_percentage":20,"resets_at":1792626000},"seven_day":{"used_percentage":76,"resets_at":1792785600}}}`)
	_, err = Record(later, "k1", now.Add(time.Minute))
	require.NoError(t, err)
	first, last := EndRun("k1")
	require.InDelta(t, 0.13, *first.FiveHour, 1e-9)
	require.InDelta(t, 0.20, *last.FiveHour, 1e-9)
	require.NoFileExists(t, runPath("k1"))

	// An older reading never replaces a newer one.
	_, err = Record(lines[1], "", now)
	require.NoError(t, err)
	require.InDelta(t, 0.20, *Load().FiveHour, 1e-9)
}

// Concurrent recorders leave a whole, valid file.
func TestSave_ConcurrentWritersLeaveAValidFile(t *testing.T) {
	TempCache(t)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			line := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":%d,"resets_at":1792626000}}}`, i)
			_, _ = Record([]byte(line), "kc", now.Add(time.Duration(i)*time.Second))
		})
	}
	wg.Wait()
	r := Load()
	require.NotNil(t, r)
	require.NotNil(t, r.FiveHour)
	first, last := EndRun("kc")
	require.NotNil(t, first)
	require.NotNil(t, last)
}

// A key that is not a file name records nothing under it.
func TestKeys(t *testing.T) {
	TempCache(t)
	require.True(t, ValidKey(NewKey()))
	for _, bad := range []string{"", "../x", "a/b", "a b"} {
		require.False(t, ValidKey(bad), bad)
		require.NoError(t, BeginRun(bad, "r", "s"))
		require.Empty(t, RunOf(bad))
	}
	require.NoDirExists(t, RunsDir())
}

func TestAgo(t *testing.T) {
	require.Equal(t, "just now", Ago(30*time.Second))
	require.Equal(t, "2 min ago", Ago(2*time.Minute+10*time.Second))
	require.Equal(t, "3 h ago", Ago(3*time.Hour))
	require.Equal(t, "10 min", Minutes(10*time.Minute))
	require.Equal(t, "1m30s", Minutes(90*time.Second))
}
