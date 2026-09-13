package claudeprobe

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/stretchr/testify/require"
)

// harness wires Ensure to a scripted claude: a fixed --version line and a
// probe that returns a chosen result and counts its calls.
type harness struct {
	opts   Options
	out    *bytes.Buffer
	probes int
}

func newHarness(t *testing.T, versionLine string, result repl.ProbeResult) *harness {
	t.Helper()
	h := &harness{out: &bytes.Buffer{}}
	h.opts = Options{
		ClaudeBin: "claude", ApeVersion: "0.0.69", CacheDir: t.TempDir(), Out: h.out,
		Version: func(context.Context, string) string { return versionLine },
		Probe: func(context.Context, string) repl.ProbeResult {
			h.probes++
			return result
		},
		Now: func() time.Time { return time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) },
	}
	return h
}

func TestEnsure_ProbesOncePerClaudeAndApeVersion(t *testing.T) {
	h := newHarness(t, "2.1.270 (Claude Code)", repl.ProbeResult{Verdict: repl.ProbeVerified})

	require.NoError(t, Ensure(t.Context(), h.opts))
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Equal(t, 1, h.probes, "a verified pair is remembered")
	require.Contains(t, h.out.String(), "claude 2.1.270 verified")

	h.opts.ApeVersion = "0.0.70"
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Equal(t, 2, h.probes, "a new ape re-probes: the trust walk it verifies is ape's")

	h.opts.Version = func(context.Context, string) string { return "2.1.271 (Claude Code)" }
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Equal(t, 3, h.probes, "a new claude re-probes — the case this exists for")
}

// A broken claude stops the run with the reason, the screen and the bytes,
// and is never cached — the next run checks again, so installing a fixed
// claude or ape clears it without anyone deleting a file.
func TestEnsure_ABrokenClaudeStopsTheRunAndIsNotCached(t *testing.T) {
	h := newHarness(t, "2.1.269 (Claude Code)", repl.ProbeResult{
		Verdict: repl.ProbeBroken,
		Detail:  "could not reach a trust-granting option in 6 moves",
		Pane:    " ❯ No, exit\n   Yes, I trust this folder",
		Output:  []byte("\x1b[?u raw"),
	})

	err := Ensure(t.Context(), h.opts)
	require.True(t, IsBroken(err), "%v", err)
	msg := err.Error()
	require.Contains(t, msg, "claude 2.1.269 failed ape's startup check: could not reach a trust-granting option")
	require.Contains(t, msg, " ❯ No, exit")
	require.Contains(t, msg, EnvProbe+"=off", "the escape hatch is named where it is needed")

	saved := filepath.Join(h.opts.CacheDir, "claude-probe-2.1.269.bin")
	require.Contains(t, msg, saved)
	raw, readErr := os.ReadFile(saved)
	require.NoError(t, readErr)
	require.Equal(t, []byte("\x1b[?u raw"), raw)

	_ = Ensure(t.Context(), h.opts)
	require.Equal(t, 2, h.probes, "a broken result is not remembered")
}

// Could not tell is not a pass and not a failure: the run continues, the
// warning is on the page, and nothing is cached.
func TestEnsure_UndeterminedWarnsAndContinuesUncached(t *testing.T) {
	h := newHarness(t, "2.1.270 (Claude Code)", repl.ProbeResult{
		Verdict: repl.ProbeUndetermined, Detail: "claude drew nothing before the deadline",
	})
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Contains(t, h.out.String(), "WARNING — could not verify claude 2.1.270 (claude drew nothing before the deadline)")
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Equal(t, 2, h.probes)
}

// A binary that is not Claude Code — a test stand-in, a wrapper — is not
// probed: its screens are not the contract.
func TestEnsure_NotClaudeCodeIsNotProbed(t *testing.T) {
	for _, line := range []string{"", "GNU bash, version 5.2", "2.1.270"} {
		h := newHarness(t, line, repl.ProbeResult{Verdict: repl.ProbeBroken})
		require.NoError(t, Ensure(t.Context(), h.opts), "%q", line)
		require.Zero(t, h.probes, "%q", line)
	}
}

// The escape hatch skips the probe and says so, every run.
func TestEnsure_OffSkipsAndSaysSo(t *testing.T) {
	t.Setenv(EnvProbe, "off")
	h := newHarness(t, "2.1.270 (Claude Code)", repl.ProbeResult{Verdict: repl.ProbeBroken})
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Zero(t, h.probes)
	require.Contains(t, h.out.String(), "skipped ("+EnvProbe+"=off) — this claude is unverified")
}

// A cache nobody can parse costs one probe, never a skipped one.
func TestEnsure_ACorruptCacheReprobes(t *testing.T) {
	h := newHarness(t, "2.1.270 (Claude Code)", repl.ProbeResult{Verdict: repl.ProbeVerified})
	require.NoError(t, os.WriteFile(filepath.Join(h.opts.CacheDir, "claude-contract.json"), []byte("{not json"), 0o644))
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Equal(t, 1, h.probes)
	require.NoError(t, Ensure(t.Context(), h.opts))
	require.Equal(t, 1, h.probes, "and the rewritten cache is read next time")
}

func TestCache_KeepsTheNewestPairs(t *testing.T) {
	t.Parallel()
	c := &cacheFile{}
	for i := range maxCached + 7 {
		c.add("2.1."+string(rune('a'+i%26)), "0.0.69", time.Unix(int64(i), 0))
	}
	require.Len(t, c.Verified, maxCached)
	require.Equal(t, time.Unix(int64(maxCached+6), 0).UTC().Format(time.RFC3339), c.Verified[maxCached-1].VerifiedAt)
}
