package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.4 (D498 (b), B4): requires: NAME: {probe, when}.
func TestConfig_RequiresProbe(t *testing.T) {
	load := func(t *testing.T, yaml string) (Config, error) {
		t.Helper()
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
		require.NoError(t, os.WriteFile(ConfigPath(root), []byte(yaml), 0o644))
		return LoadConfig(root)
	}
	cfg, err := load(t, "requires:\n  docker: cmd\n  dockerd: {probe: \"docker info\", when: \"-tags=[^ ]*integration\"}\n  db: {probe: \"pg_isready\"}\n")
	require.NoError(t, err)
	require.Equal(t, RequireCmd, cfg.Requires["docker"].Kind)
	require.Equal(t, MachineRequirement{Kind: RequireProbe, Probe: "docker info", When: "-tags=[^ ]*integration", when: cfg.Requires["dockerd"].when}, cfg.Requires["dockerd"])
	require.NotNil(t, cfg.Requires["dockerd"].when)
	require.Nil(t, cfg.Requires["db"].when, "no when: bound by name")
	for yaml, want := range map[string]string{
		"requires: {d: {probe: \"\"}}\n":                "probe is empty",
		"requires: {d: {probe: x, when: \"(\"}}\n":      "is not a regular expression",
		"requires: {d: {probe: x, when: y, also: z}}\n": "requires.d.also: not a key",
		"requires: {d: probe}\n":                        "is not env or cmd",
	} {
		_, err := load(t, yaml)
		require.ErrorContains(t, err, want, yaml)
	}
}

// A failing probe makes every check it binds unrun ("not ready"), never
// green; a check it does not bind runs; the probe runs once per command.
func TestRunChecks_Probe(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nrequires:\n  dockerd: {probe: \"echo probed >> probe.count; test -f ready.flag\", when: \"-tags=integration\"}\n"
	files["it.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	probeResults.Range(func(k, _ any) bool { probeResults.Delete(k); return true })
	checks := []PlannedCheck{
		{Ref: "T1", Kind: KindTask, Cmd: "./it.sh -tags=integration ./store"},
		{Ref: "T2", Kind: KindTask, Cmd: "./it.sh -tags=integration ./queue"},
		{Ref: "T3", Kind: KindTask, Cmd: "./it.sh ./unit"},
	}
	rs := p.RunChecks(ctx(), filepath.Join(p.Root, "_apex", "state", "logs", "t"), "", checks, RunOptions{})
	require.Equal(t, ReasonUnrun, rs[0].Reason)
	require.Equal(t, ExitUnrun, rs[0].Exit)
	require.Contains(t, rs[0].Detail, "dockerd is not ready: its probe `echo probed >> probe.count; test -f ready.flag` exited 1")
	require.Equal(t, ReasonUnrun, rs[1].Reason)
	require.Equal(t, 0, rs[2].Exit, "a check the probe does not bind runs")
	require.Equal(t, 1, strings.Count(read(t, p, "probe.count"), "probed"), "once per command")

	// Ready: the bound checks run.
	probeResults.Range(func(k, _ any) bool { probeResults.Delete(k); return true })
	write(t, p, "ready.flag", "")
	rs = p.RunChecks(ctx(), filepath.Join(p.Root, "_apex", "state", "logs", "t"), "", checks, RunOptions{})
	require.Equal(t, 0, rs[0].Exit)
	require.Equal(t, 0, rs[1].Exit)
}
