package claudesurface

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/stretchr/testify/require"
)

// baselinePath is the committed, reviewed surface.
const baselinePath = "testdata/claude-surface.json"

// changelogURL is Claude Code's public CHANGELOG. APE_CLAUDE_CHANGELOG_URL
// overrides it, e.g. to read a local copy.
const changelogURL = "https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md"

// watchedEnv are the variables ape SETS on a spawn. One vanishing from the
// binary means ape's setting silently stopped applying.
var watchedEnv = []string{repl.EnvClaudeEffortLevel, repl.EnvDisableBGShellReap, repl.EnvForkSubagent}

// TestLive_ClaudeSurface diffs the installed Claude Code's tool list and
// environment variables against the reviewed baseline, and lists the
// relevant CHANGELOG entries nobody has reviewed yet. See the package doc.
//
// Opt-in (APE_CLAUDE_LIVE=1, `make check-claude-surface`): it needs claude,
// auth and network. APE_CLAUDE_SURFACE_UPDATE=1 (`make
// update-claude-surface`) records the review instead: it rewrites the
// baseline from the installed claude, after printing what it acknowledges.
func TestLive_ClaudeSurface(t *testing.T) {
	if os.Getenv("APE_CLAUDE_LIVE") != "1" {
		t.Skip("APE_CLAUDE_LIVE=1 not set — this gate needs claude, auth and network (make check-claude-surface)")
	}
	update := os.Getenv("APE_CLAUDE_SURFACE_UPDATE") == "1"

	claudeBin, err := exec.LookPath("claude")
	require.NoError(t, err, "claude not on PATH")
	out, err := exec.Command(claudeBin, "--version").Output()
	require.NoError(t, err)
	installed := Canonical(string(out))
	t.Logf("installed Claude Code %s (%s)", installed, claudeBin)

	base, err := Load(baselinePath)
	require.NoError(t, err)
	t.Logf("baseline reviewed through %s", base.ReviewedThrough)

	tools := liveTools(t, claudeBin)
	realBin, err := filepath.EvalSymlinks(claudeBin)
	require.NoError(t, err)
	f, err := os.Open(realBin)
	require.NoError(t, err)
	env, err := EnvVars(f)
	_ = f.Close()
	require.NoError(t, err)
	f, err = os.Open(realBin)
	require.NoError(t, err)
	models, err := ModelIDs(f)
	_ = f.Close()
	require.NoError(t, err)
	require.NotEmpty(t, models, "no claude-<family>-<n> ids in %s — the model scan needs rethinking", realBin)
	unreviewed := RelevantSince(ParseChangelog(fetchChangelog(t)), base.ReviewedThrough, installed)

	toolsAdded, toolsRemoved := Diff(base.Tools, tools)
	envAdded, envRemoved := Diff(base.EnvVars, env)
	modelsAdded, modelsRemoved := Diff(base.Models, models)
	for _, e := range unreviewed {
		t.Logf("changelog %s: %s", e.Version, e.Text)
	}
	t.Logf("tools: +%v -%v", toolsAdded, toolsRemoved)
	t.Logf("env vars: %d added, %d removed: +%v -%v", len(envAdded), len(envRemoved), head(envAdded), head(envRemoved))
	t.Logf("models: +%v -%v", head(modelsAdded), modelsRemoved)
	for _, m := range modelsAdded {
		if _, exact := cost.Lookup(m); !exact {
			t.Logf("model %s: no exact row in internal/cost/prices.yaml", m)
		}
	}

	if update {
		require.NoError(t, (&Baseline{ReviewedThrough: installed, Tools: tools, EnvVars: env, Models: models}).Save(baselinePath))
		t.Logf("baseline rewritten from %s: %d tools, %d env vars, %d models, %d changelog entries acknowledged",
			installed, len(tools), len(env), len(models), len(unreviewed))
		return
	}

	var gone []string
	for _, v := range watchedEnv {
		if slices.Contains(envRemoved, v) || !slices.Contains(env, v) {
			gone = append(gone, v)
		}
	}
	require.Empty(t, gone, "Claude Code %s no longer names %v, which ape sets on every spawn: the setting silently "+
		"stopped applying. Find what replaced it before releasing", installed, gone)
	require.True(t, len(toolsAdded) == 0 && len(toolsRemoved) == 0,
		"Claude Code %s's tool list moved since the baseline (%s): added %v, removed %v. A removed tool breaks every "+
			"skill naming it (TaskOutput, 2.1.277). Check the framework's skills, then `make update-claude-surface`",
		installed, base.ReviewedThrough, toolsAdded, toolsRemoved)
	require.Empty(t, modelsAdded, "Claude Code %s knows model ids the baseline (%s) does not: %v. A new model can be "+
		"what a bare family word now starts (Sonnet 5.5, 2.1.284). For each real one: an exact row and context window "+
		"in internal/cost/prices.yaml, its family alias repointed if it is the new generation (check-claude's "+
		"model_aliases says), then `make update-claude-surface`", installed, base.ReviewedThrough, modelsAdded)
	require.Empty(t, unreviewed, "%d CHANGELOG entries between %s and %s touch what ape and the skills drive "+
		"Claude Code through (listed above). Read them; follow up anything that changes a contract; then "+
		"`make update-claude-surface` to record the review", len(unreviewed), base.ReviewedThrough, installed)
}

// liveTools reads the tool list from the init event of a `-p` session
// spawned the way ape spawns (scrubbed env plus ape's defaults), killing it
// once the event arrives. `-p` rather than a PTY because only stream-json
// reports the tool list; the interactive list is the same release's.
func liveTools(t *testing.T, claudeBin string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin, "-p", "Reply OK", "--model", "haiku",
		"--output-format", "stream-json", "--verbose", "--strict-mcp-config")
	cmd.Dir = t.TempDir()
	cmd.Env = append(repl.ScrubClaudeCodeEnv(os.Environ()), repl.SpawnDefaultEnv()...)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	tools, version, err := ToolsFromStreamJSON(stdout)
	require.NoError(t, err, "no init event from `claude -p --output-format stream-json --verbose`")
	require.NotEmpty(t, tools, "the init event listed no tools — this check needs rethinking")
	t.Logf("init event: Claude Code %s, %d tools", version, len(tools))
	return tools
}

// head keeps a log line readable: the first seeding lists every name.
func head(xs []string) []string {
	const n = 40
	if len(xs) <= n {
		return xs
	}
	return append(slices.Clone(xs[:n]), "…")
}

func fetchChangelog(t *testing.T) []byte {
	t.Helper()
	url := changelogURL
	if u := os.Getenv("APE_CLAUDE_CHANGELOG_URL"); u != "" {
		url = u
	}
	if path, ok := strings.CutPrefix(url, "file://"); ok {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return data
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "changelog NOT verified: could not fetch %s", url)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "changelog NOT verified: %s returned %s", url, resp.Status)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotEmpty(t, ParseChangelog(data), "changelog NOT verified: no `## <version>` sections in %s", url)
	return data
}
