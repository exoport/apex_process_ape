package apexv1

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	coordHead = "coordination:\n  ensemble: shop\n  home: ../home\n  url: nats://127.0.0.1:4222\n"
	coordGH   = "  github:\n    repo: shop/notes\n"
)

func modeOf(t *testing.T, cfg string) (string, error) {
	t.Helper()
	c, err := LoadConfig(writeConfig(t, cfg))
	require.NoError(t, err)
	return c.Coordination.Mode()
}

// D876: the five modes, and every block that makes none, refused with
// coord.integration and the fix.
func TestMode(t *testing.T) {
	for _, tc := range []struct{ name, cfg, mode string }{
		{"no block", "project_name: x\n", ModeSolo},
		{"trunk", "coordination:\n  integration: trunk\n  ensemble: shop\n", ModeTrunk},
		{"queue, events absent", "coordination:\n  integration: queue\n" + coordGH, ModeQueuePoll},
		{"queue/poll", "coordination:\n  integration: queue\n  events: poll\n" + coordGH, ModeQueuePoll},
		{"queue/watch", "coordination:\n  integration: queue\n  events: watch\n" + coordGH, ModeQueueWatch},
		{"queue/runner", "coordination:\n  integration: queue\n  events: runner\n  runner_label: apex-v12-7-mmr7\n" + coordGH, ModeQueueRunner},
	} {
		mode, err := modeOf(t, tc.cfg)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.mode, mode, tc.name)
	}
	for _, tc := range []struct{ name, cfg, msg string }{
		{"integration missing", coordHead, IntegrationModeCommand},
		{"integration missing, github set", coordHead + coordGH, "writes queue where coordination.github is set"},
		{"unknown integration", "coordination:\n  integration: merge\n", `"merge" is neither trunk nor queue`},
		{"queue without github", "coordination:\n  integration: queue\n", "queue without coordination.github"},
		// INT-1: a github: block naming no repository is no repository.
		{"queue, github without repo", "coordination:\n  integration: queue\n  github: {}\n", "queue without coordination.github.repo"},
		{"queue, empty repo", "coordination:\n  integration: queue\n  github:\n    repo: \"\"\n", "queue without coordination.github.repo"},
		{"queue, blank repo", "coordination:\n  integration: queue\n  github:\n    repo: \"  \"\n", "queue without coordination.github.repo"},
		{"watch, empty repo", "coordination:\n  integration: queue\n  events: watch\n  github:\n    repo: \"\"\n", "queue without coordination.github.repo"},
		{"trunk, empty github block", "coordination:\n  integration: trunk\n  github:\n    repo: \"\"\n", "trunk with coordination.github"},
		{"trunk with github", "coordination:\n  integration: trunk\n" + coordGH, "trunk with coordination.github"},
		{"trunk with events", "coordination:\n  integration: trunk\n  events: watch\n", "trunk with events: watch"},
		{"trunk with a label", "coordination:\n  integration: trunk\n  runner_label: r1\n", "trunk with runner_label"},
		{"unknown events", "coordination:\n  integration: queue\n  events: push\n" + coordGH, `events: "push" is not poll, watch or runner`},
		{"label under poll", "coordination:\n  integration: queue\n  runner_label: r1\n" + coordGH, "runner_label without events: runner"},
		{"label under watch", "coordination:\n  integration: queue\n  events: watch\n  runner_label: r1\n" + coordGH, "runner_label without events: runner"},
		{"runner without a label", "coordination:\n  integration: queue\n  events: runner\n" + coordGH, "no runner_label"},
		{"runner, self-hosted", "coordination:\n  integration: queue\n  events: runner\n  runner_label: Self-Hosted\n" + coordGH, "default runner labels"},
	} {
		_, err := modeOf(t, tc.cfg)
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%s: %v", tc.name, err)
		require.Equal(t, CodeCoordIntegration, fe.Code, tc.name)
		require.Contains(t, fe.Msg, tc.msg, tc.name)
	}
}

// D871, V12-15: a runner label is the runner's own — one word, never one
// of GitHub's default labels, whatever its case.
func TestCheckRunnerLabel(t *testing.T) {
	for _, ok := range []string{"apex-v12-7-mmr7", "r1", "a.b_c-d"} {
		require.NoError(t, CheckRunnerLabel(ok), ok)
	}
	for _, bad := range []string{"", "self-hosted", "SELF-HOSTED", "Linux", "linux", "X64", "x86", "ARM64", "arm", "macOS", "Windows",
		"a,b", "a b", "[a]", "a:b", strings.Repeat("a", 65)} {
		require.Error(t, CheckRunnerLabel(bad), bad)
	}
}

// The migration's edit: one line before the block's first key at its
// indentation; every other byte kept (comments, CRLF), and a flow mapping
// left to a person.
func TestInsertIntegration(t *testing.T) {
	in := "# my project\nproject_name: x\ncoordination:   # the ensemble\n    # the shop\n    ensemble: shop\n    url: nats://h:1\nlimits: {}\n"
	out, err := insertIntegration([]byte(in), IntegrationTrunk)
	require.NoError(t, err)
	require.Equal(t, "# my project\nproject_name: x\ncoordination:   # the ensemble\n    # the shop\n    integration: trunk\n    ensemble: shop\n    url: nats://h:1\nlimits: {}\n", string(out))

	crlf := strings.ReplaceAll(coordHead+coordGH, "\n", "\r\n")
	out, err = insertIntegration([]byte(crlf), IntegrationQueue)
	require.NoError(t, err)
	require.Equal(t, "coordination:\r\n  integration: queue\r\n  ensemble: shop\r\n", string(out)[:len("coordination:\r\n  integration: queue\r\n  ensemble: shop\r\n")])

	_, err = insertIntegration([]byte("coordination: {ensemble: shop}\n"), IntegrationTrunk)
	require.ErrorContains(t, err, "flow mapping")
	_, err = insertIntegration([]byte("coordination: {}\n"), IntegrationTrunk)
	require.Error(t, err)
}

func integrationFixture(t *testing.T, cfg string) *Project {
	t.Helper()
	files := baseFiles()
	files["_apex/config.yaml"] = cfg
	return fixture(t, files)
}

// D876 + framework's ANSWER-Q-D877-CONFIG: the verb writes only
// coordination.integration (queue where github: is set, else trunk), in
// ape's own commit, which config.changed does not flag; --check reports
// it (1) and writes nothing; a second run is a no-op.
func TestWriteIntegrationMode(t *testing.T) {
	for _, tc := range []struct{ cfg, want string }{
		{"project_name: x\n" + coordHead + coordGH, IntegrationQueue},
		{"project_name: x\n" + coordHead, IntegrationTrunk},
	} {
		p := integrationFixture(t, tc.cfg)
		epoch := HeadSHA(ctx(), p.Root)
		_, err := p.IntegrationMode()
		require.Error(t, err, "before: the block makes no mode")

		res, err := p.IntegrationModeCheck()
		fe, ok := errors.AsType[*FailError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, CodeIntegrationModePending, fe.Code)
		require.Equal(t, tc.want, res.Integration)
		data, _ := os.ReadFile(ConfigPath(p.Root))
		require.Equal(t, tc.cfg, string(data), "--check writes nothing")

		res, err = p.WriteIntegrationMode(ctx())
		require.NoError(t, err)
		require.Equal(t, tc.want, res.Integration)
		require.NotEmpty(t, res.Commit)
		require.Equal(t, IntegrationModeSubject, strings.TrimSpace(run(t, p.Root, "log", "-1", "--format=%s")))
		require.Equal(t, "_apex/config.yaml", strings.TrimSpace(run(t, p.Root, "show", "--name-only", "--format=", "HEAD")), "the commit carries the config alone")
		require.Equal(t, "1\t0\t_apex/config.yaml", strings.TrimSpace(run(t, p.Root, "diff", "--numstat", "HEAD^", "HEAD")), "one line added, nothing else")
		require.Contains(t, run(t, p.Root, "show", "HEAD"), "+  integration: "+tc.want)
		require.Empty(t, p.configFindings(ctx(), epoch), "ape's own commit: no config.changed")

		mode, err := p.IntegrationMode()
		require.NoError(t, err)
		require.Contains(t, []string{ModeTrunk, ModeQueuePoll}, mode)

		res, err = p.IntegrationModeCheck()
		require.NoError(t, err, "--check passes on the explicit config")
		require.Empty(t, res.Integration)
		head := HeadSHA(ctx(), p.Root)
		res, err = p.WriteIntegrationMode(ctx())
		require.NoError(t, err)
		require.Empty(t, res.Commit, "idempotent")
		require.Equal(t, head, HeadSHA(ctx(), p.Root))

		// A person's commit that changes integration is config.changed.
		write(t, p, "_apex/config.yaml", strings.Replace(string(mustRead(t, ConfigPath(p.Root))), "integration: "+tc.want, "integration: trunk2", 1))
		commitAll(t, p, "chore: retune the integration")
		require.Contains(t, findingMessages(p.configFindings(ctx(), epoch)), "guarded config keys (coordination) changed")
	}
}

// A dirty config is refused, nothing written; the same project clean is
// the control. A solo project has nothing to write.
func TestWriteIntegrationMode_Refusals(t *testing.T) {
	p := integrationFixture(t, "project_name: x\n"+coordHead)
	write(t, p, "_apex/config.yaml", "project_name: y\n"+coordHead)
	head := HeadSHA(ctx(), p.Root)
	_, err := p.WriteIntegrationMode(ctx())
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "tree.dirty", fe.Code)
	require.Equal(t, "project_name: y\n"+coordHead, string(mustRead(t, ConfigPath(p.Root))), "nothing written")
	require.Equal(t, head, HeadSHA(ctx(), p.Root))

	write(t, p, "_apex/config.yaml", "project_name: x\n"+coordHead)
	_, err = p.WriteIntegrationMode(ctx())
	require.NoError(t, err, "control: the clean tree writes")

	solo := integrationFixture(t, "project_name: x\n")
	res, err := solo.WriteIntegrationMode(ctx())
	require.NoError(t, err)
	require.Empty(t, res.Commit)
	_, err = solo.IntegrationModeCheck()
	require.NoError(t, err)

	flow := integrationFixture(t, "coordination: {ensemble: shop, home: h, url: u}\n")
	_, err = flow.WriteIntegrationMode(ctx())
	fe, ok = errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeIntegrationModePending, fe.Code)
	require.Contains(t, fe.Msg, "by hand")
}

// ape status's mode, solo included; a block that makes none says why.
func TestStatus_IntegrationMode(t *testing.T) {
	p := integrationFixture(t, "project_name: x\n")
	require.Equal(t, ModeSolo, p.Status().IntegrationMode)
	write(t, p, "_apex/config.yaml", "coordination:\n  integration: queue\n  events: watch\n"+coordGH)
	require.Equal(t, ModeQueueWatch, p.Status().IntegrationMode)
	write(t, p, "_apex/config.yaml", coordHead)
	r := p.Status()
	require.Empty(t, r.IntegrationMode)
	require.Contains(t, r.IntegrationError, IntegrationModeCommand)
}

// project check carries the refusal as coord.integration.
func TestCheck_IntegrationFinding(t *testing.T) {
	p := integrationFixture(t, "project_name: x\n"+coordHead)
	var found bool
	for _, f := range ClassifyFindings(p.Check()) {
		if f.Code == CodeCoordIntegration {
			found = true
		}
	}
	require.True(t, found, "project check: coord.integration")
	write(t, p, "_apex/config.yaml", "project_name: x\ncoordination:\n  integration: trunk\n  ensemble: shop\n  home: h\n  url: u\n")
	for _, f := range ClassifyFindings(p.Check()) {
		require.NotEqual(t, CodeCoordIntegration, f.Code, "control: a coherent block")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
