package repl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/stretchr/testify/require"
)

// TestLive_ClaudeCodeContract is the opt-in gate for the question no
// hermetic test can answer: did the Claude Code installed on this machine
// break the contract ape drives it through?
//
// ape does not call a Claude Code API. It types into a TUI over a
// pseudo-terminal and reads the rendered grid back, and every one of those
// couplings is an undocumented implementation detail of a binary that
// auto-updates on a schedule ape does not control:
//
//   - the `--dangerously-skip-permissions` / `--model` flags it spawns with;
//   - the `bypass permissions on` footer WaitForReady keys "the REPL is
//     accepting input" off, and the ❯ glyph behind it;
//   - CLAUDE_CODE_EFFORT_LEVEL, and the effort vocabulary it accepts;
//   - the model ids in ape's family-alias table still naming real models;
//   - a spawned claude persisting a session transcript at all, which every
//     transcript-derived telemetry value depends on;
//   - `--version` printing something the manifest stamp can parse.
//
// When one of these moves, nothing errors. ape keeps running and silently
// stops doing the thing the coupling bought — the same "believed-present
// protection" trap `hookdrift` and `ape costs coverage` exist to close, in
// the one surface neither of them can see. Those two read artifacts a past
// run left behind; this spawns the harness and looks.
//
// It is NOT hermetic — it needs `claude` on PATH, working auth, and network
// — so it is gated behind APE_CLAUDE_LIVE=1 and never runs in `make test`
// or in GitHub CI. Run it via `make check-claude` before a release.
//
//	APE_CLAUDE_LIVE=1 go test ./internal/repl/ -run TestLive_ClaudeCodeContract -v
//
// All subtests but transcript_persists cost zero tokens: they only launch
// the REPL and read the pane. transcript_persists submits one Haiku turn
// (fractions of a cent) and is skippable with APE_CLAUDE_LIVE_TOKENS=0.
func TestLive_ClaudeCodeContract(t *testing.T) {
	if os.Getenv("APE_CLAUDE_LIVE") != "1" {
		t.Skip("set APE_CLAUDE_LIVE=1 (needs claude on PATH + auth + network) to run the live Claude Code contract gate")
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	t.Logf("probing Claude Code at %s (%s)", claudeBin, versionOf(t, claudeBin))

	t.Run("version_shape", func(t *testing.T) { liveVersionShape(t, claudeBin) })
	t.Run("ready_signals", func(t *testing.T) { liveReadySignals(t, claudeBin) })
	t.Run("effort_env", func(t *testing.T) { liveEffortEnv(t, claudeBin) })
	t.Run("model_aliases", func(t *testing.T) { liveModelAliases(t, claudeBin) })
	t.Run("transcript_persists", func(t *testing.T) { liveTranscriptPersists(t, claudeBin) })
}

// claudeVersionRe is the shape `claude --version` prints, e.g.
// "2.1.240 (Claude Code)". runner.claudeVersion stamps the whole line into
// each manifest and hookdrift.claudeVersionFor strips the parenthesised
// suffix back off to name the harness in a drift report. Both degrade
// quietly on anything else, so the shape is only ever asserted here.
var claudeVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+\S*\s+\(Claude Code\)$`)

func liveVersionShape(t *testing.T, claudeBin string) {
	t.Helper()
	require.Regexp(t, claudeVersionRe, versionOf(t, claudeBin),
		"`claude --version` no longer prints `<semver> (Claude Code)` — the manifest's claude_version stamp "+
			"and hookdrift's version attribution both parse this shape (runner.claudeVersion, "+
			"hookdrift.claudeVersionFor)")
}

// liveReadySignals is the core PTY assertion: a claude spawned exactly the
// way the interactive runner spawns it reaches an input-accepting REPL, and
// BOTH signals replReady tests for are really there.
//
// WaitForReady succeeding is not enough on its own. It returns true on
// either the bypass-permissions footer or a bare ❯ prompt line, so if the
// footer text changed, the fallback would keep the check green while the
// primary signal rotted — the failure mode this whole file exists to
// prevent. Each signal is therefore asserted on its own.
//
// The launch directory is a fresh temp dir claude has never seen, which is
// also the scenario the blockingModals table exists for. A NEW pre-REPL
// modal (a theme picker, a "what's new" gate) that ape does not know how to
// dismiss shows up here as a WaitForReady timeout whose NotReadyError
// carries the pane that blocked it — which is the diagnosis, not just the
// symptom.
func liveReadySignals(t *testing.T, claudeBin string) {
	t.Helper()
	pane := launch(t, claudeBin, "ready", nil, "")

	require.Contains(t, pane, "bypass permissions on",
		"the bypass-permissions footer is replReady's PRIMARY ready signal (internal/repl/repl.go). "+
			"It is gone, so WaitForReady is now running on its ❯ fallback alone")
	require.Contains(t, pane, ReadyGlyph,
		"the ❯ prompt glyph (ReadyGlyph) is replReady's fallback signal and the anchor for emptyPromptRe")
	require.True(t, replReady(pane),
		"replReady rejected a pane WaitForReady accepted — the two have diverged")
}

// liveEffortEnv proves CLAUDE_CODE_EFFORT_LEVEL still selects the reasoning
// effort of a spawned claude. ape sets it via the environment rather than a
// CLI flag specifically so it reaches sub-agents (see EnvClaudeEffortLevel);
// if the variable were renamed, every ape run would silently fall back to
// whatever the harness defaults to, at a different cost and quality than
// the pipeline asked for.
//
// The REPL renders the live level in its footer ("◐ medium · /effort"), so
// this is observable without spending a token. Two levels are probed and
// each asserts the ABSENCE of the other: a pane that merely contains the
// word could be a coincidence, but a pane that tracks the variable across
// two values is honouring it.
func liveEffortEnv(t *testing.T, claudeBin string) {
	t.Helper()
	// medium/max, deliberately: neither is a substring of the other (unlike
	// high/xhigh), so the mutual-exclusion assertion below is exact.
	for _, tc := range []struct{ want, absent string }{
		{want: "medium", absent: "max"},
		{want: "max", absent: "medium"},
	} {
		// No --model: the effort indicator only renders for models that take
		// one, and the default model does.
		pane := launch(t, claudeBin, "effort-"+tc.want, nil, tc.want)

		// Assert on the indicator LINE, not the whole pane. The launch banner
		// carries release notes that have mentioned `--max-budget-usd`, and a
		// whole-pane match on "max" reads that as the effort level.
		line := lineContaining(pane, "/effort")
		require.NotEmpty(t, line,
			"the REPL no longer renders an effort indicator — CLAUDE_CODE_EFFORT_LEVEL can no longer be "+
				"verified from the pane, so this check needs rethinking.\nPane:\n%s", pane)
		require.Contains(t, line, tc.want,
			"spawned with %s=%s but the effort indicator reads %q — the variable is no longer honoured, so "+
				"every ape run is silently using the harness default effort", EnvClaudeEffortLevel, tc.want, line)
		require.NotContains(t, line, tc.absent,
			"effort indicator %q shows %q while spawned at effort %q — the footer is not tracking %s",
			line, tc.absent, tc.want, EnvClaudeEffortLevel)
	}
}

// lineContaining returns the first line of pane containing sub, trimmed, or
// "" when no line does.
func lineContaining(pane, sub string) string {
	for l := range strings.SplitSeq(pane, "\n") {
		if strings.Contains(l, sub) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// liveModelAliases proves every model id ape can select still names a model
// Claude Code knows.
//
// The set is read from ape's own family-alias table rather than hardcoded,
// so a family added to internal/cost/prices.yaml is covered here the moment
// it lands. These are the ids a spec's bare `model: sonnet` resolves to —
// ape pins the generation itself instead of passing the family word through
// (see cost.CanonicalModelArg), which is what makes a stale table able to
// select a model that no longer exists.
//
// The assertion is that the pane does NOT echo the raw id. An unrecognised
// model is not an error: Claude Code starts the REPL anyway and prints the
// id verbatim in the footer, where a recognised one renders as its display
// name ("claude-opus-5" → "Opus 5"). Keying on the absence of the raw id
// rather than on an expected display name means this survives Anthropic
// renaming the human-facing labels, and only fails on the thing that
// actually matters — the id going dead.
func liveModelAliases(t *testing.T, claudeBin string) {
	t.Helper()
	aliases := cost.FamilyAliases()
	require.NotEmpty(t, aliases, "cost.FamilyAliases() is empty — nothing to verify")

	families := make([]string, 0, len(aliases))
	for family := range aliases {
		families = append(families, family)
	}
	sort.Strings(families)

	for _, family := range families {
		model := aliases[family]
		t.Run(family, func(t *testing.T) {
			pane := launch(t, claudeBin, "model-"+family, []string{"--model", model}, "medium")
			require.NotContains(t, pane, model,
				"Claude Code echoed %q back verbatim instead of rendering a display name, which is what it does "+
					"for a model it does not recognise. ape's `%s` alias points at a dead id: a pipeline saying "+
					"`model: %s` now spawns a fallback model. Fix the aliases: block in internal/cost/prices.yaml",
				model, family, family)
		})
	}
}

// liveTranscriptPersists is the regression gate for the v0.0.28–32 saga.
//
// When ape runs inside a Claude Code session (ubiquitous in development),
// the inherited CLAUDECODE / CLAUDE_CODE_* markers make a spawned claude
// treat itself as a nested session and suppress transcript persistence.
// ScrubClaudeCodeEnv strips them so the child registers as top-level and
// writes ~/.claude/projects/<slug>/<sid>.jsonl. Nothing fails when that
// stops working — the run completes, and every transcript-derived value
// (cost, tokens, model attribution) is silently zero.
//
// So this asserts the whole chain end to end: spawn through the real
// NewSessionWithEnv, submit one turn, and require that a transcript
// appeared AND that ape can parse it into a model id and non-zero tokens.
// That second half is the part `ape costs coverage` cannot give: coverage
// reads transcripts that already exist, so it validates the price table
// against history, while this validates ape's parser against a transcript
// the current Claude Code wrote seconds ago. A renamed usage field or a
// restructured record shows up here first.
//
// This is the only subtest that spends tokens — one short Haiku turn.
func liveTranscriptPersists(t *testing.T, claudeBin string) {
	t.Helper()
	if os.Getenv("APE_CLAUDE_LIVE_TOKENS") == "0" {
		t.Skip("APE_CLAUDE_LIVE_TOKENS=0 — skipping the one subtest that submits a turn")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	dir, token := uniqueWorkdir(t)
	name := sessionName(t, "transcript")
	t.Cleanup(func() { removeScratchTranscripts(t, home, token) })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Recorded before the spawn so anyTranscriptSince can tell "nothing was
	// persisted anywhere" from "persisted somewhere this test did not look".
	start := time.Now()

	// Haiku keeps the turn cheap. The transcript's shape is the subject
	// here, and it does not vary by model.
	argv := []string{claudeBin, "--dangerously-skip-permissions", "--model", cost.ResolveFamilyAlias("haiku")}
	require.NoError(t, NewSessionWithEnv(ctx, name, dir, argv, EffortEnv("medium")))
	t.Cleanup(func() { _ = KillSession(context.Background(), name) })
	require.NoError(t, WaitForReady(ctx, name))

	require.NoError(t, SendCommand(ctx, name, "Reply with the single word OK and nothing else."))

	// Claude Code writes the session file as the turn completes, not at
	// launch, so poll rather than sleeping a fixed interval.
	path := waitForTranscript(ctx, t, home, token)
	if path == "" {
		diagnosis := "transcript persistence is BROKEN: nothing was written anywhere under the Claude home " +
			"during this test. ScrubClaudeCodeEnv is supposed to strip the parent session's CLAUDECODE / " +
			"CLAUDE_CODE_* nesting markers so the spawned claude registers as top-level and persists its own " +
			"transcript. With that gone, every transcript-derived value in ape (cost, tokens, model " +
			"attribution) is silently zero — the v0.0.28–32 root cause."
		if anyTranscriptSince(home, start) {
			diagnosis = "transcript persistence still WORKS — a transcript was written during this test, just " +
				"not under a directory naming this session's cwd. Claude Code's cwd → project-directory " +
				"encoding now mangles alphanumerics too, so the token match in waitForTranscript needs " +
				"rethinking. ape itself is unaffected: cost.FindSessionJSONL globs across all project dirs " +
				"and never depends on the encoding."
		}
		require.FailNow(t, "no session transcript for the spawned session",
			"%s\n\nlooked for: %s\nlast pane:\n%s", diagnosis,
			filepath.Join(home, ".claude", "projects", "*"+token+"*", "*.jsonl"), paneOf(ctx, name))
	}

	totals, model, err := cost.ScanSessionJSONL(path)
	require.NoError(t, err, "ape could not parse the transcript Claude Code just wrote: %s", path)
	require.NotEmpty(t, model,
		"ape read no model id out of a fresh transcript (%s) — the record's model field has moved", path)
	require.NotZero(t, totals.InputTokens+totals.OutputTokens,
		"ape read zero tokens out of a fresh transcript (%s) — the usage fields have moved, so every cost "+
			"and token figure ape reports is now zero", path)
	t.Logf("transcript %s → model=%s in=%d out=%d", filepath.Base(path), model, totals.InputTokens, totals.OutputTokens)
}

// waitForTranscript polls for the session transcript written for dir, and
// returns its path (or "" if ctx expires first).
//
// It globs dir's own project slug rather than calling cost.FindSessionJSONL,
// which returns the newest transcript across the WHOLE home. That is the
// right behaviour in production but unusable here: this gate is normally run
// from inside a Claude Code session that is writing its own transcript
// continuously, and would win the mtime race.
//
// The match is on the caller's unique token (see uniqueWorkdir), not on a
// reconstruction of Claude Code's cwd → directory encoding. Reproducing that
// encoding is a coupling with no upside: it is undocumented (it folds `_` to
// `-` as well as the separators), ape itself never depends on it —
// cost.FindSessionJSONL globs across all project dirs precisely so it does
// not have to — and getting it wrong fails this subtest for a reason that
// has nothing to do with the contract under test. An alphanumeric token
// survives any sanitisation scheme.
//
// Matching loosely on t.TempDir()'s own leaf would NOT do: those leaves are
// `001`, `002`, and repeat across runs, so the glob finds a directory an
// earlier run created and the check passes on a stale transcript without
// ever waiting for this turn. The token is unique per run for that reason.
func waitForTranscript(ctx context.Context, t *testing.T, home, token string) string {
	t.Helper()
	glob := filepath.Join(home, ".claude", "projects", "*"+token+"*", "*.jsonl")
	for {
		if matches, _ := filepath.Glob(glob); len(matches) > 0 {
			sort.Strings(matches)
			return matches[0]
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(2 * time.Second):
		}
	}
}

// uniqueWorkdir creates a working directory whose name is a token that
// survives Claude Code's cwd → project-directory encoding intact, and
// returns both. Lowercase letters and digits only: whatever that encoding
// folds (separators, underscores), it leaves alphanumerics alone, so the
// token can be matched inside the resulting directory name without ape
// having to model the encoding at all.
//
// The nanosecond stamp makes it unique per run, so a glob on it can never
// match a directory a previous run left behind.
func uniqueWorkdir(t *testing.T) (dir, token string) {
	t.Helper()
	token = fmt.Sprintf("apelive%d", time.Now().UnixNano())
	dir = filepath.Join(t.TempDir(), token)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir, token
}

// removeScratchTranscripts deletes the Claude project directory this
// subtest's throwaway session created, so a gate run before every release
// does not slowly fill the developer's Claude home with one-turn transcripts
// for /tmp paths that no longer exist — and so those turns never skew what
// `ape costs coverage` reads.
//
// Only a directory whose name carries this run's unique token is touched,
// and only inside ~/.claude/projects. A token is generated per run and
// contains a nanosecond stamp, so the glob cannot name a real project even
// if the encoding around it changes.
func removeScratchTranscripts(t *testing.T, home, token string) {
	t.Helper()
	if token == "" { // never glob `*<empty>*` — that is every project
		return
	}
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*"+token+"*"))
	if err != nil {
		return
	}
	for _, dir := range matches {
		if !strings.Contains(filepath.Base(dir), token) {
			continue // belt and braces: never remove what the token did not name
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("could not remove scratch transcript dir %s: %v", dir, err)
		}
	}
}

// anyTranscriptSince reports whether ANY session transcript in home was
// written after ts. It disambiguates the two ways waitForTranscript can come
// back empty: if nothing anywhere was written, transcript persistence is
// broken (the v0.0.28–32 failure); if something was, persistence works and
// it is projectSlug's encoding that has moved.
func anyTranscriptSince(home string, ts time.Time) bool {
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
	for _, p := range matches {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(ts) {
			return true
		}
	}
	return false
}

// launch spawns claude the way the interactive runner does, waits for the
// REPL, and returns the rendered pane. Failures carry the pane so a broken
// contract is diagnosable from the test output alone.
func launch(t *testing.T, claudeBin, label string, extraArgs []string, effort string) string {
	t.Helper()
	name := sessionName(t, label)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	argv := append([]string{claudeBin, "--dangerously-skip-permissions"}, extraArgs...)
	require.NoError(t, NewSessionWithEnv(ctx, name, t.TempDir(), argv, EffortEnv(effort)),
		"could not spawn claude with argv %v", argv)
	t.Cleanup(func() { _ = KillSession(context.Background(), name) })

	require.NoError(t, WaitForReady(ctx, name),
		"claude never reached an input-accepting REPL with argv %v.\n"+
			"Either a flag ape passes was rejected, or a NEW pre-REPL modal is blocking that "+
			"blockingModals does not know how to dismiss — the pane above shows which",
		argv)

	// WaitForReady returns on the first ready frame; the footer that carries
	// the model and effort indicators paints a beat later.
	time.Sleep(2 * time.Second)
	return paneOf(ctx, name)
}

func paneOf(ctx context.Context, name string) string {
	pane, err := CapturePane(ctx, name)
	if err != nil {
		return fmt.Sprintf("(pane unavailable: %v)", err)
	}
	return pane
}

// sessionName derives a registry-unique session name. The repl registry is
// process-global and keyed by name, so subtests sharing one would collide.
func sessionName(t *testing.T, label string) string {
	t.Helper()
	return "live-" + label + "-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
}

func versionOf(t *testing.T, claudeBin string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, claudeBin, "--version").Output()
	require.NoError(t, err, "`claude --version` failed")
	return strings.TrimSpace(string(out))
}
