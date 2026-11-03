//go:build !windows

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/rebuild"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// TestFixture_ScratchCoordinated is not a test: it builds a scratch
// coordinated project for eval's 1c rehearsals (Q-1C-4: H2's attach, H7's
// stub run), on the cluster NATSLOCAL_URLS names, and leaves it at
// FIXTURE_OUT: a bare origin, two member checkouts (a: user ann, b: user
// bob), each with its own credential in hot/ (minted for an unenforced
// cluster), main holding M1 and M2 approved by a person, the coordination:
// block (ensemble scratch1c, no github: — local trunk), and the skills of
// FIXTURE_PAYLOAD (a payload dir holding .claude/skills) committed. The
// ape is FIXTURE_APE, else one built from this tree. README.md there says
// how to start the supervisor and the coordinator. Opt-in: skipped unless
// FIXTURE_OUT and NATSLOCAL_URLS are set.
func TestFixture_ScratchCoordinated(t *testing.T) {
	out, url := os.Getenv("FIXTURE_OUT"), os.Getenv("NATSLOCAL_URLS")
	if out == "" || url == "" {
		t.Skip("FIXTURE_OUT / NATSLOCAL_URLS unset: a fixture builder, not a test")
	}
	ape := os.Getenv("FIXTURE_APE")
	if ape == "" {
		ape = buildApe(t)
	}
	_, err := os.Stat(out)
	require.ErrorIs(t, err, os.ErrNotExist, "FIXTURE_OUT must not exist")
	require.NoError(t, os.MkdirAll(out, 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	nc, err := nats.Connect(url, nats.MaxReconnects(-1))
	require.NoError(t, err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rebuild.Declare(ctx, js, 3) == nil }, time.Minute, time.Second)
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	if ep, err := ls.Epoch(ctx); err != nil || ep == 0 {
		require.NoError(t, ls.SetEpoch(ctx, 1))
	}

	env := append(os.Environ(), "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null",
		"APE_SESSION=", "CLAUDECODE=", "APE_TOOLCHAIN=local", "APE_CLAUDE_PROBE=off")
	run := func(dir, name string, args ...string) {
		t.Helper()
		c := exec.Command(name, args...)
		c.Dir, c.Env = dir, env
		o, err := c.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, o)
	}
	origin := filepath.Join(out, "origin.git")
	run(out, "git", "init", "-q", "--bare", "-b", "main", origin)

	a := filepath.Join(out, "a")
	run(out, "git", "clone", "-q", origin, a)
	run(a, "git", "commit", "-q", "--allow-empty", "-m", "base")
	run(a, ape, "project", "init")
	cfgPath := filepath.Join(a, "_apex", "config.yaml")
	cfgData, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, []byte(strings.Replace(string(cfgData), `project_name: "a"`, `project_name: "scratch1c-project"`, 1)), 0o644))
	for rel, body := range map[string]string{
		"development/project.md": "# Scratch 1c\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n",
		"gate1.sh":               "#!/bin/sh\n./wave.sh 2>/dev/null | grep -q wave\n",
		"gate2.sh":               "#!/bin/sh\n./tide.sh 2>/dev/null | grep -q tide\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(a, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(a, rel), []byte(body), 0o755))
	}
	if payload := os.Getenv("FIXTURE_PAYLOAD"); payload != "" {
		run(out, "cp", "-r", filepath.Join(payload, ".claude", "skills"), filepath.Join(a, ".claude"))
	}
	run(a, "git", "add", "-A")
	run(a, "git", "commit", "-q", "-m", "scratch 1c fixture")
	run(a, ape, "milestone", "new", "Waves", "--gate", "./gate1.sh", "--goal", "waves")
	run(a, ape, "milestone", "new", "Tides", "--gate", "./gate2.sh", "--goal", "tides")
	run(a, ape, "milestone", "approve", "M1", "--by", "fixture")
	run(a, ape, "milestone", "approve", "M2", "--by", "fixture")
	cfg := filepath.Join(a, "_apex", "config.yaml")
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	block := "coordination:\n  ensemble: scratch1c\n  home: scratch-home\n  url: " + url + "\n"
	require.NoError(t, os.WriteFile(cfg, append(data, []byte(block)...), 0o644))
	run(a, "git", "add", "-A")
	run(a, "git", "commit", "-q", "-m", "chore: the coordination block (scratch1c)")
	run(a, "git", "push", "-q", "origin", "HEAD:main")

	b := filepath.Join(out, "b")
	run(out, "git", "clone", "-q", origin, b)
	for dir, user := range map[string]string{a: "ann", b: "bob"} {
		creds, _ := natstest.MintStandaloneCreds(t, user)
		data, err := os.ReadFile(creds)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "hot"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hot", "ape-nats.creds"), data, 0o600))
		run(dir, ape, "status", "--json")
	}
	head := strings.TrimSpace(gitOut(t, a, "rev-parse", "HEAD"))
	readme := `# Scratch coordinated fixture for 1c rehearsals (Q-1C-4)

Built by ape's TestFixture_ScratchCoordinated, on the cluster ` + url + `.
Not the mirror; nothing here reaches GitHub.

- origin: ` + origin + ` (main at ` + head + `)
- member checkouts: ` + a + ` (user ann), ` + b + ` (user bob); each hot/ape-nats.creds is minted for an unenforced cluster
- project: scratch1c-project; main: M1 (gate ./gate1.sh) and M2 (gate ./gate2.sh), approved by a person; ensemble scratch1c; local trunk (no coordination.github)
- ape: ` + ape + `

Start the coordination, from a (APE_TOOLCHAIN=local):

    cd ` + a + ` && APE_TOOLCHAIN=local setsid nohup ` + ape + ` supervisor --id sup-scratch1c --assess >> ` + filepath.Join(out, "supervisor.log") + ` 2>&1 < /dev/null &
    cd ` + a + ` && APE_TOOLCHAIN=local ` + ape + ` supervisor ensure

Then claim per checkout (ape milestone claim M1 in a, M2 in b; a person's act), attach a conductor
(ape conductor attach), and run the loops or the stub sessions. A claim lives while a run renews it.

What this fixture does not have: GitHub. Milestone branches, record PRs, close PRs and take-ins are the
coordination.github flow's and are not exercised here; a close is the local trunk's.
`
	require.NoError(t, os.WriteFile(filepath.Join(out, "README.md"), []byte(readme), 0o644))
	t.Logf("fixture at %s (main %s)", out, head)
}
