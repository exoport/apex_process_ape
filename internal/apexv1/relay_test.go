package apexv1

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/sessions"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// OI-11: limits.context_relay (0 < x ≤ 0.95, default 0.6) and limits.relays
// (a whole number, default 5).
func TestLimits_Relay(t *testing.T) {
	var l Limits
	require.InDelta(t, 0.6, l.ContextRelayAt(), 1e-9)
	require.Equal(t, 5, l.RelaysCap())
	require.NoError(t, yaml.Unmarshal([]byte("context_relay: 0.05\nrelays: 0\n"), &l))
	require.InDelta(t, 0.05, l.ContextRelayAt(), 1e-9)
	require.Equal(t, 0, l.RelaysCap())
	require.NoError(t, yaml.Unmarshal([]byte("context_relay: 0.95\n"), &Limits{}))
	for doc, want := range map[string]string{
		"context_relay: 0\n":    "limits.context_relay: 0 is not a fraction above 0 and at most 0.95",
		"context_relay: 0.96\n": "limits.context_relay: 0.96 is not a fraction",
		"context_relay: 60\n":   "limits.context_relay: 60 is not a fraction",
		"relays: -1\n":          "limits.relays: -1 is not a whole number",
	} {
		require.ErrorContains(t, yaml.Unmarshal([]byte(doc), &Limits{}), want)
	}
	cfg, err := LoadConfig(writeConfig(t, "limits:\n  context_relay: 0.15\n  relays: 2\n"))
	require.NoError(t, err)
	require.InDelta(t, 0.15, cfg.Limits.ContextRelayAt(), 1e-9)
	require.Equal(t, 2, cfg.Limits.RelaysCap())
	_, err = LoadConfig(writeConfig(t, "limits:\n  context_relay: 1.5\n"))
	require.ErrorContains(t, err, "limits.context_relay: 1.5", "a value ape cannot use refuses the config")
}

// The relay keys sit in the guarded limits: block, so a loop cannot edit
// its way out of a relay (design §7.1).
func TestConfigChanged_GuardsRelayKeys(t *testing.T) {
	p := fixture(t, baseFiles())
	epoch := HeadSHA(ctx(), p.Root)
	write(t, p, "_apex/config.yaml", "limits:\n  context_relay: 0.9\n")
	commitAll(t, p, "chore: relay later")
	fs := p.configFindings(ctx(), epoch)
	require.Len(t, fs, 1)
	require.Contains(t, fs[0].Message, "guarded config keys (limits) changed")
	write(t, p, "_apex/config.yaml", "limits:\n  context_relay: 0.9\n  relays: 50\n")
	commitAll(t, p, "chore: more relays")
	require.Len(t, p.configFindings(ctx(), epoch), 2)
}

// RelayCheck: below the threshold and with an unknown window, nothing; at
// it, session.relay (exit 2, stop); at it with the relays spent,
// limits.relays (exit 1, person).
func TestRelayCheck(t *testing.T) {
	limits := Limits{}
	at := func(tokens, window int) ContextUse {
		return ContextUse{Tokens: tokens, Window: window, Model: "claude-sonnet-4-5"}
	}
	require.NoError(t, RelayCheck("ape task", at(119_999, 200_000), limits, 0), "59.99 % is under 60 %")
	require.NoError(t, RelayCheck("ape task", at(900_000, 0), limits, 0), "an unknown window never relays")

	err := RelayCheck("ape task", at(126_000, 200_000), limits, 0)
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeSessionRelay, ue.Code)
	require.Equal(t, ActionStop, ue.Action)
	require.Contains(t, ue.Msg, "ape task refused: this "+OrchestratorSkill+" session's context is at 63 % (126000 of 200000 tokens, claude-sonnet-4-5), at or over limits.context_relay 60 %")
	require.Contains(t, ue.Msg, "(relay 1 of 5)")
	require.Contains(t, ue.Msg, " — end this session now")
	require.NoError(t, RelayCheck("ape task", at(126_000, 200_000), Limits{ContextRelay: new(0.7)}, 0), "the configured threshold")

	err = RelayCheck("ape change tool", at(120_000, 200_000), limits, 5)
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeRelaysExceeded, fe.Code)
	require.Equal(t, ActionPerson, fe.Action)
	require.Contains(t, fe.Msg, "ape change tool refused: this "+OrchestratorSkill+" session's context is at 60 %")
	require.Contains(t, fe.Msg, "has relayed 5 time(s) (limits.relays 5)")
	require.Error(t, RelayCheck("ape task", at(120_000, 200_000), Limits{Relays: new(0)}, 0), "relays: 0 never relays")

	msg := RelaysExhausted(&RelayRequest{Tokens: 150_000, Window: 200_000, Threshold: 0.6}, 5, 5).Error()
	require.Contains(t, msg, "reached 75 % (150000 of 200000 tokens) at or over limits.context_relay 60 % after 5 relay(s), limits.relays 5 — a person")
}

// The record round-trips, a missing one is nil, and the run id cannot
// escape the relays directory.
func TestRelayRecord(t *testing.T) {
	root := t.TempDir()
	rec, err := ReadRelayRecord(root, "r1")
	require.NoError(t, err)
	require.Nil(t, rec)
	require.NoError(t, WriteRelayRecord(root, &RelayRecord{Run: "r1", Relays: 2, RelayOf: "r0", Transcript: "/t.jsonl"}))
	rec, err = ReadRelayRecord(root, "r1")
	require.NoError(t, err)
	require.Equal(t, "r0", rec.RelayOf)
	require.Equal(t, 2, rec.Relays)
	require.Equal(t, filepath.Join(filepath.Dir(RelayRecordPath(root, "r1")), "x.json"), RelayRecordPath(root, "../../x"))
	RemoveRelayRecord(root, "r1")
	require.NoFileExists(t, RelayRecordPath(root, "r1"))
}

func relayTranscript(t *testing.T, model, tokens string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loop.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"assistant","message":{"model":"`+model+
		`","usage":{"input_tokens":4,"cache_read_input_tokens":`+tokens+`,"cache_creation_input_tokens":0}}}`+"\n"), 0o600))
	return path
}

// ape status --json: a live loop's session carries its context fill and
// relays; an unknown window is a null fill; any other session carries none.
func TestLiveSessions_LoopRelay(t *testing.T) {
	p := fixture(t, baseFiles())
	reg := filepath.Join(t.TempDir(), "registry.json")
	old := sessions.Path
	sessions.Path = func() string { return reg }
	t.Cleanup(func() { sessions.Path = old })
	now := time.Now()
	require.NoError(t, sessions.Register(reg, sessions.Session{PID: os.Getpid(), CWD: p.Root, Command: "task", RunID: "loop-2", Skill: OrchestratorSkill, Depth: 1, StartedAt: now}))
	require.NoError(t, sessions.Register(reg, sessions.Session{PID: os.Getpid(), CWD: p.Root, Command: "task", RunID: "child-1", Skill: "apex-change", Depth: 2, ParentRun: "loop-2", StartedAt: now.Add(time.Second)}))
	require.NoError(t, WriteRelayRecord(p.Root, &RelayRecord{Run: "loop-2", Relays: 1, RelayOf: "loop-1", Transcript: relayTranscript(t, "claude-sonnet-4-5", "49996")}))
	require.NoError(t, WriteRelayRecord(p.Root, &RelayRecord{Run: "child-1", Transcript: relayTranscript(t, "claude-sonnet-4-5", "1")}))

	live := p.LiveSessions()
	require.Len(t, live, 2)
	r := live[0].Relay
	require.NotNil(t, r)
	require.Equal(t, 50000, r.ContextTokens)
	require.Equal(t, 200000, r.ContextWindow)
	require.NotNil(t, r.ContextFill)
	require.InDelta(t, 0.25, *r.ContextFill, 1e-9)
	require.InDelta(t, 0.6, r.ContextRelay, 1e-9)
	require.Equal(t, 1, r.Relays)
	require.Equal(t, 5, r.RelaysCap)
	require.Equal(t, "loop-1", r.RelayOf)
	require.Equal(t, "context 25 % (relay at 60 %), relays 1/5, relay of loop-1", r.Describe())
	require.Nil(t, live[1].Relay, "only the loop's run is relayed")

	require.NoError(t, WriteRelayRecord(p.Root, &RelayRecord{Run: "loop-2", Transcript: relayTranscript(t, "claude-nonesuch-9", "10")}))
	r = p.LiveSessions()[0].Relay
	require.Nil(t, r.ContextFill, "an unknown window is an unknown fill")
	require.Zero(t, r.ContextWindow)
	require.Equal(t, "context unknown (relay at 60 %), relays 0/5", r.Describe())
}

// D226: limits.context_relay times the loop's window under 100k tokens is
// the notice limits.context-relay-low, on the model models.apex-loop names
// (a 200k one relays lower in tokens) or, without one, the smallest window
// of today's family models; the default, and an unknown model's window,
// give none.
func TestContextRelayLowNotice(t *testing.T) {
	notices := func(cfg string) []Diagnostic {
		p := fixture(t, baseFiles())
		write(t, p, "_apex/config.yaml", cfg)
		return p.ContextRelayNotices()
	}
	require.Empty(t, notices(""), "the default 0.6")
	require.Empty(t, notices("limits:\n  context_relay: 0.1\n"), "100k of a 1M window is enough")

	got := notices("limits:\n  context_relay: 0.05\n")
	require.Len(t, got, 1, "eval's ledger cell (D226): 50k of a 1M window")
	n := got[0]
	require.Equal(t, CodeContextRelayLow, n.Code)
	require.Equal(t, SeverityNotice, n.Severity)
	require.Equal(t, ActionPerson, n.Action)
	require.Equal(t, "_apex/config.yaml", n.File)
	require.Regexp(t, `^limits\.context_relay 0\.05 relays the loop at 50000 tokens of a 1000000-token window \(no models\.apex-loop model: the smallest window of the models Claude Code's family words name\), under 100000: a fresh apex-loop session starts at about 46000 tokens \(D226\)`, n.Message)
	require.Equal(t, "a person raises limits.context_relay to at least 0.1 (default 0.6) in _apex/config.yaml and accepts it (`ape project accept-config --by <who>`: limits: is guarded)", n.Fix)

	got = notices("limits:\n  context_relay: 0.4\nmodels:\n  apex-loop: claude-sonnet-4-5\n")
	require.Len(t, got, 1, "0.4 of a 200k window is 80k")
	require.Contains(t, got[0].Message, "at 80000 tokens of claude-sonnet-4-5's 200000-token window")
	require.Contains(t, got[0].Fix, "at least 0.5 ")
	require.Empty(t, notices("limits:\n  context_relay: 0.05\nmodels:\n  apex-loop: no-such-model\n"), "an unknown window never relays")
	require.Empty(t, notices("limits:\n  context_relay: 0.6\nmodels:\n  apex-loop: {model: claude-sonnet-4-5}\n"))
}
