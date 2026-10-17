package apexv1

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// flakyScript fails its first n runs, then passes; its counter lives
// outside the project, so the stray undo does not reset it.
func flakyScript(t *testing.T, failures int) string {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "runs")
	return "n=$(cat " + counter + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + counter +
		"; echo run $n; [ $n -gt " + strconv.Itoa(failures) + " ]"
}

func TestConfig_Retries(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	for yaml, want := range map[string]string{
		"retries: 2\n":   "",
		"retries: 0\n":   "",
		"retries: -1\n":  "not a whole number from 0 to 5",
		"retries: 9\n":   "not a whole number from 0 to 5",
		"retries: two\n": "not a whole number from 0 to 5",
	} {
		require.NoError(t, os.WriteFile(ConfigPath(root), []byte(yaml), 0o644))
		cfg, err := LoadConfig(root)
		if want == "" {
			require.NoError(t, err, yaml)
			continue
		}
		require.ErrorContains(t, err, want, yaml)
		require.Zero(t, cfg.Retries)
	}
}

// H12: a red check runs again up to retries: times; a pass on a retry is
// green and flaky, and the failed run's log is kept.
func TestRunChecks_Retries(t *testing.T) {
	p := fixture(t, baseFiles())
	runOne := func(cmd string) CheckResult {
		return p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: "T1", Kind: KindTask, Cmd: cmd}}, RunOptions{})[0]
	}

	r := runOne(flakyScript(t, 1))
	require.Equal(t, 1, r.Exit, "retries: 0 (the default) runs once")
	require.False(t, r.Flaky)
	require.Zero(t, r.Tries)

	write(t, p, "_apex/config.yaml", "retries: 2\n")
	r = runOne(flakyScript(t, 1))
	require.Equal(t, 0, r.Exit)
	require.True(t, r.Flaky)
	require.Equal(t, 2, r.Tries)
	require.False(t, r.Red())
	require.Equal(t, "_apex/state/logs/x/T1.log", r.Log)
	last, err := os.ReadFile(filepath.Join(p.Root, r.Log))
	require.NoError(t, err)
	require.Contains(t, string(last), "run 2")
	first, err := os.ReadFile(filepath.Join(p.LogDir("x"), "T1.try1.log"))
	require.NoError(t, err)
	require.Contains(t, string(first), "run 1", "the failed run's log is kept")

	r = runOne(flakyScript(t, 5))
	require.Equal(t, 1, r.Exit, "red on every try is red")
	require.False(t, r.Flaky)
	require.Equal(t, 3, r.Tries)

	r = runOne("echo 'ok  pkg 0.01s [no tests to run]'")
	require.Equal(t, ReasonVacuous, r.Reason)
	require.Zero(t, r.Tries, "running nothing again runs nothing")

	r = runOne("true")
	require.Zero(t, r.Tries)
	require.FileExists(t, filepath.Join(p.LogDir("x"), "T1.log"))
}

// H12: a flaky task check does not fail verify; `ape status` lists it per
// change (JSON flaky, and the brief).
func TestVerify_FlakyIsGreenAndListed(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nretries: 1\n"
	p := fixture(t, files)
	flaky := flakyScript(t, 3) // red at baseline (runs 1, 2), red then green at verify (runs 3, 4)
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n- T2 timing holds — check: "+flaky+"\n"))
	commitAll(t, p, "plan")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	var t2 CheckResult
	for _, r := range v.Results {
		if r.Task == "T2" {
			t2 = r
		}
	}
	require.True(t, t2.Flaky)
	require.Equal(t, 2, t2.Tries)

	cs := p.Status().Changes[0]
	require.Equal(t, []string{"T2"}, cs.Flaky)
	require.Contains(t, p.Status().Brief(), "flaky: T2")
	require.True(t, state(t, p, "add-wave").Verify.Results[1].Flaky, "recorded in state.json for review")
}
