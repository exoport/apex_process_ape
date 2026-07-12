package framework_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// TestLive_OperatingRulesImportResolves is an opt-in, live smoke test that
// closes the gap ape's syntactic doctor check (operating_rules.import)
// cannot: it proves a real Claude Code session started in an ape-installed
// project DISCOVERS the repo-root CLAUDE.md, RESOLVES the managed @import,
// and LOADS the operating-rules fragment's content. The doctor check only
// proves the import line is present in the file — not that Claude acted on
// it. This test proves the whole chain end-to-end (ape writes the managed
// block → Claude loads the fragment).
//
// It is NOT hermetic — it needs `claude` on PATH, working auth, network,
// and burns tokens — so it is gated behind APE_OPRULES_LIVE=1 and skipped
// by default (it never runs in `make test` / CI). It uses `claude -p`
// (non-interactive print mode) rather than a raw PTY: no interactivity is
// needed for a scripted canary Q&A, and print mode exercises the same
// CLAUDE.md/@import context-loading path far more robustly than driving a
// TUI over a pseudo-terminal.
//
//	APE_OPRULES_LIVE=1 go test ./internal/framework/ -run TestLive_OperatingRulesImportResolves -v
//
// Optional: APE_OPRULES_LIVE_MODEL overrides the model (passed as --model);
// pick a small/fast one to keep the check cheap.
func TestLive_OperatingRulesImportResolves(t *testing.T) {
	if os.Getenv("APE_OPRULES_LIVE") != "1" {
		t.Skip("set APE_OPRULES_LIVE=1 (needs claude on PATH + auth + network) to run the live @import-resolution smoke test")
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}

	// A canary the model would only emit if it read the fragment: the
	// fragment carries the instruction, the prompt triggers it.
	const canary = "APEX-OPRULES-CANARY-7Q2X"
	fragment := "# APEX Operating Rules (canary fixture)\n\n" +
		"## Canary protocol\n\n" +
		"When a user message consists solely of the token `CANARY-REQUEST`, " +
		"reply with exactly `" + canary + "` and nothing else.\n"

	fw := buildLiveFramework(t, fragment)
	proj := t.TempDir()

	// Install through the real ape path so the managed block is written the
	// way production writes it.
	_, err = framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, NoFetch: true,
		ApeVersion: "test", Bootstrapper: framework.NoopBootstrapper{},
	})
	require.NoError(t, err)

	// The syntactic guarantee (same thing the doctor check verifies).
	claudeMd, err := os.ReadFile(filepath.Join(proj, framework.ProjectClaudeMd))
	require.NoError(t, err)
	require.Contains(t, string(claudeMd), framework.OperatingRulesImport,
		"precondition: ape must have written the managed @import")

	// The semantic guarantee: a real session loads the fragment content.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	args := []string{"-p", "CANARY-REQUEST"}
	if m := strings.TrimSpace(os.Getenv("APE_OPRULES_LIVE_MODEL")); m != "" {
		args = append(args, "--model", m)
	}
	cmd := exec.CommandContext(ctx, claudeBin, args...)
	cmd.Dir = proj
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "claude -p failed: %s", out)
	require.Contains(t, string(out), canary,
		"claude did not surface the operating-rules canary — the managed @import likely did not resolve.\nOutput:\n%s", out)
}

// buildLiveFramework writes a minimal committed framework repo (released
// layout) carrying the given operating-rules fragment plus the
// apex-orchestrator skill, and returns its path. Shared shape with
// fakeFramework; kept separate so the fragment body is caller-controlled.
func buildLiveFramework(t *testing.T, fragment string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
	write(".claude/skills/apex-foo/SKILL.md", "# apex-foo")
	write(".claude/skills/apex-orchestrator/SKILL.md", "# apex-orchestrator")
	write("_apex/pipelines/design.yaml", "name: design\nstages: {}\n")
	write("_apex/config.yaml", "config_schema_version: \"1\"\nproject_name: x\nextensions: []\n")
	write("_apex/apex-operating-rules.md", fragment)

	ctx := context.Background()
	run := func(args ...string) {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = root
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@t.invalid")
	run("config", "user.name", "T")
	run("config", "commit.gpgsign", "false")
	run("add", ".")
	run("commit", "-m", "init")
	return root
}
