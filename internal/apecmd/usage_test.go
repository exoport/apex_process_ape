package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/usage"
)

// usageTempCache points os.UserCacheDir at a fresh directory for the test,
// on every platform: no test reads or writes the real <user cache>/ape.
func usageTempCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("LocalAppData", dir)
	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(cache, "ape"), usage.Dir())
	return usage.Dir()
}

// statuslineJSON is Claude Code 2.1.306's status-line rate_limits, as
// measured (internal/usage/testdata).
func statuslineJSON(fivePct, sevenPct int) string {
	return `{"version":"2.1.306","rate_limits":{"five_hour":{"used_percentage":` + itoa(fivePct) +
		`,"resets_at":1792626000},"seven_day":{"used_percentage":` + itoa(sevenPct) + `,"resets_at":1792785600}}}`
}

func itoa(n int) string { return strconv.Itoa(n) }

// W4-A1: the recorder prints the status line and keeps the reading; a JSON
// with no rate_limits (before the first API response), or one it cannot
// read, prints nothing, writes nothing, and exits 0.
func TestUsageRecord_StatusLineCommand(t *testing.T) {
	usageTempCache(t)
	for _, bad := range []string{`{"version":"2.1.306","cost":{"total_cost_usd":0}}`, `garbage`, ``} {
		stdout, stderr, err := v1ExecIn(t, newUsageCmd, bad, "record", "--run", "k1")
		require.NoError(t, err, bad)
		require.Empty(t, stdout)
		require.Empty(t, stderr)
		require.NoFileExists(t, usage.Path())
	}
	stdout, _, err := v1ExecIn(t, newUsageCmd, statuslineJSON(14, 55), "record")
	require.NoError(t, err)
	require.Equal(t, "5h 14% · 7d 55%\n", stdout)
	r := usage.Load()
	require.NotNil(t, r)
	require.InDelta(t, 0.55, *r.SevenDay, 1e-9)

	// ape usage shows it, with its age.
	at := r.At
	usageNow = func() time.Time { return at.Add(2 * time.Minute) }
	t.Cleanup(func() { usageNow = time.Now })
	stdout, _, err = v1Exec(t, newUsageCmd)
	require.NoError(t, err)
	require.Contains(t, stdout, "usage: 5h 0.14 · 7d 0.55 (read 2 min ago)")
	require.Contains(t, stdout, usage.Path())
	stdout, _, err = v1Exec(t, newUsageCmd, "--output-format", "json")
	require.NoError(t, err)
	var rep struct {
		Reading    *usage.Reading `json:"reading"`
		AgeSeconds float64        `json:"age_seconds"` //nolint:tagliatelle // the report's own names
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &rep), stdout)
	require.InDelta(t, 120, rep.AgeSeconds, 0.001)
	require.InDelta(t, 0.14, *rep.Reading.FiveHour, 1e-9)

	_, _, err = v1Exec(t, newUsageCmd, "stray")
	require.Error(t, err)
}

func TestUsage_UnknownWithoutAReading(t *testing.T) {
	usageTempCache(t)
	stdout, _, err := v1Exec(t, newUsageCmd)
	require.NoError(t, err)
	require.Contains(t, stdout, "usage: unknown")
	stdout, _, err = v1Exec(t, newUsageCmd, "--output-format", "json")
	require.NoError(t, err)
	require.Contains(t, stdout, `"reading": null`)
}

// statusLineOf is the statusLine of a prepend's --settings.
func statusLineOf(t *testing.T, prepend []string) map[string]any {
	t.Helper()
	for i, a := range prepend {
		if a == "--settings" {
			var s map[string]any
			require.NoError(t, json.Unmarshal([]byte(prepend[i+1]), &s))
			sl, _ := s["statusLine"].(map[string]any)
			return sl
		}
	}
	t.Fatal("no --settings")
	return nil
}

// Every unattended session ape launches gets ape's status line in its
// --settings — an ape task run's with its launch key, an ape prompt's
// without — an ape chat (the person's own session) gets none, and the
// person's own settings file is never written.
func TestSpawnSettings_CarryTheStatusLine(t *testing.T) {
	home := usageTempCache(t)
	personal := filepath.Join(home, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(personal), 0o755))
	own := `{"statusLine":{"type":"command","command":"my-own-line"}}` + "\n"
	require.NoError(t, os.WriteFile(personal, []byte(own), 0o644))

	task, err := buildInteractivePrepend("/opt/ape/bin/ape", 4242, false, "", nil, spawnSettings{usageKey: "20261021-120000-1-abcd"})
	require.NoError(t, err)
	sl := statusLineOf(t, task)
	require.Equal(t, "command", sl["type"])
	require.Equal(t, "/opt/ape/bin/ape usage record --run 20261021-120000-1-abcd", sl["command"])

	prompt, err := buildInteractivePrepend("/opt/ape/bin/ape", 4242, false, "", nil, spawnSettings{})
	require.NoError(t, err)
	require.Equal(t, "/opt/ape/bin/ape usage record", statusLineOf(t, prompt)["command"])

	chat, err := buildInteractivePrepend("/opt/ape/bin/ape", 4242, false, "", nil, spawnSettings{personSession: true})
	require.NoError(t, err)
	for i, a := range chat {
		if a == "--settings" {
			require.NotContains(t, chat[i+1], "statusLine", "ape chat is the person's own session")
		}
	}

	got, err := os.ReadFile(personal)
	require.NoError(t, err)
	require.Equal(t, own, string(got), "the person's settings are never written")
}

// An ape task run's first and last reading become its manifest's usage:
// {before, after}, and an entry of the share history.
func TestEndRunUsage_ManifestBeforeAfter(t *testing.T) {
	usageTempCache(t)
	root := t.TempDir()
	runDir := filepath.Join(runlog.TasksRoot(root), "apex-apply", "20261021-120000-abc1234")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "manifest.yaml"), []byte("run_id: 20261021-120000-abc1234\nstatus: completed\n"), 0o644))

	key := usage.NewKey()
	beginRunUsage(key, "20261021-120000-abc1234", "apex-apply")
	runUsageRecord(strings.NewReader(statuslineJSON(10, 50)), &strings.Builder{}, key)
	time.Sleep(10 * time.Millisecond)
	runUsageRecord(strings.NewReader(statuslineJSON(16, 52)), &strings.Builder{}, key)
	require.Equal(t, "20261021-120000-abc1234", usage.Load().Run)
	endRunUsage(key, "apex-apply", runDir)

	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.NotNil(t, m.Usage)
	require.InDelta(t, 0.10, *m.Usage.Before.FiveHour, 1e-9)
	require.InDelta(t, 0.16, *m.Usage.After.FiveHour, 1e-9)
	require.InDelta(t, 0.52, *m.Usage.After.SevenDay, 1e-9)
	require.NotNil(t, m.Usage.Before.SevenDayResetsAt)
	require.Contains(t, string(data), "usage:\n    before:\n")
	require.FileExists(t, usage.HistoryPath())

	// A run whose session recorded nothing has no usage: block.
	quiet := filepath.Join(runlog.TasksRoot(root), "apex-apply", "20261021-130000-0000000")
	require.NoError(t, os.MkdirAll(quiet, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(quiet, "manifest.yaml"), []byte("run_id: x\nstatus: completed\n"), 0o644))
	k2 := usage.NewKey()
	beginRunUsage(k2, "x", "apex-apply")
	endRunUsage(k2, "apex-apply", quiet)
	data, err = os.ReadFile(filepath.Join(quiet, "manifest.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "usage:")
}

// ape status shows the reading in one line, and as usage in --json.
func TestStatus_ShowsUsage(t *testing.T) {
	root := v1Repo(t)
	usageTempCache(t)
	stdout, _, err := v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "\nusage: unknown\n")

	runUsageRecord(strings.NewReader(statuslineJSON(14, 55)), &strings.Builder{}, "")
	stdout, _, err = v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "\nusage: 5h 0.14 · 7d 0.55 (read just now)\n")
	stdout, _, err = v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Usage map[string]any `json:"usage"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.InDelta(t, 0.55, st.Usage["seven_day"], 1e-9)
	require.Contains(t, st.Usage, "age_seconds")
	require.Contains(t, st.Usage, "seven_day_resets_at")
}
