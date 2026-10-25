package apexv1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.3: Claude Code 2.1.310's Monitor tool runs its command in a shell, so
// every Bash guard reads a Monitor command as it reads a Bash one; a
// Monitor call with no command (a ws watch) runs nothing and passes.
func TestPreTool_MonitorRunsTheBashGuards(t *testing.T) {
	t.Setenv(EnvAllowTag, "")
	t.Setenv(EnvAllowPush, "")
	t.Setenv(EnvAllowKill, "")
	p := e2Project(t)
	run(t, p.Root, "switch", "-q", "-c", "change/add-wave")
	decide := func(tool string, input map[string]any) PreToolDecision {
		in, err := json.Marshal(input)
		require.NoError(t, err)
		return PreTool(HookPayload{ToolName: tool, ToolInput: in, Cwd: p.Root})
	}
	for cmd, reason := range map[string]string{
		"git push origin main":                  "only a person pushes",
		"git tag v1.0.0":                        "only a person tags or releases",
		"cat _apex/state/prompts/s1.jsonl":      "what a person act rests on",
		`git commit --allow-empty -m "apex: x"`: "Only ape writes one",
		"pkill -f server":                       "pkill",
		"git rebase main":                       "`ape change start add-wave`",
	} {
		bash := decide("Bash", map[string]any{"command": cmd})
		require.True(t, bash.Deny, "the control: Bash denies %q", cmd)
		d := decide("Monitor", map[string]any{"command": cmd, "description": "watch", "timeout_ms": 60000, "persistent": true})
		require.True(t, d.Deny, "Monitor must deny %q as Bash does", cmd)
		require.Equal(t, bash.Reason, d.Reason, cmd)
		require.Contains(t, d.Reason, reason, cmd)
	}
	require.False(t, decide("Monitor", map[string]any{"command": "until test -f done.txt; do sleep 2; done", "timeout_ms": 60000}).Deny)
	require.False(t, decide("Monitor", map[string]any{"ws": map[string]any{"url": "ws://127.0.0.1:9/events"}, "timeout_ms": 60000}).Deny,
		"a ws-only Monitor runs no command")
}

// v1.3: a person act never runs in a Monitor command, whatever the person
// typed — a grant is for a Bash call in the turn the words were typed —
// and denying it writes no grant.
func TestPersonActGate_MonitorIsRefused(t *testing.T) {
	p := e2Project(t)
	asClaudeCode(t)
	payload := e2Turn(t, p, "s1", "p1", "approve M1", "ape milestone approve M1")
	payload.ToolName = "Monitor"
	d := PersonActGate(ctx(), payload, []PersonCall{approveCall("M1")})
	require.True(t, d.Deny)
	require.Contains(t, d.Reason, "Monitor")
	require.Contains(t, d.Reason, "run it as a Bash command")
	require.Nil(t, p.ConsumeGrant(ctx(), approveCall("M1")), "a refused Monitor call writes no grant")

	payload.ToolName = "Bash"
	require.False(t, PersonActGate(ctx(), payload, []PersonCall{approveCall("M1")}).Deny, "the control: the same words grant the Bash call")
	require.NotNil(t, p.ConsumeGrant(ctx(), approveCall("M1")))
}

// v1.3: init registers pre-tool with Monitor. A registration with the
// matcher before v1.3 is intact — never a finding, so a green project
// check stays green — but a hooks.monitor-unguarded notice, and init adds
// Monitor in place; a second init changes nothing.
func TestCheckHooks_PreMonitorMatcherIsANotice(t *testing.T) {
	require.Contains(t, strings.Split(PreToolMatcher, "|"), "Monitor")
	require.Equal(t, preMonitorMatcher+"|Monitor", PreToolMatcher)
	p := settingsProject(t, "")
	_, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	data, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	require.Contains(t, string(data), `"matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash|Monitor"`)
	require.Empty(t, p.monitorUnguardedNotices())
	require.Empty(t, p.MonitorUnguarded())

	v12 := bytesReplace(data, []byte(`"matcher": "`+PreToolMatcher+`"`), []byte(`"matcher": "`+preMonitorMatcher+`"`))
	require.NoError(t, os.WriteFile(p.SettingsJSON(), v12, 0o644))
	require.Empty(t, p.CheckHooks(), "a registration from before v1.3 is intact")
	notes := p.monitorUnguardedNotices()
	require.Len(t, notes, 1)
	require.Equal(t, CodeHooksMonitorUnguarded, notes[0].Code)
	require.Equal(t, SeverityNotice, notes[0].Severity)
	require.Contains(t, notes[0].Message, "without Monitor")
	require.Contains(t, notes[0].Fix, "ape project init")
	require.Contains(t, noticeCodesOf(p.Notices()), CodeHooksMonitorUnguarded)
	require.Equal(t, notes[0].Message, p.MonitorUnguarded())

	added, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Equal(t, []string{"PreToolUse: ape hook pre-tool (matcher: +Monitor)"}, added)
	require.Empty(t, p.monitorUnguardedNotices())
	after, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	require.Equal(t, string(data), string(after), "upgraded in place: the file is what a fresh init writes")
	added, err = MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Empty(t, added, "idempotent")
}

// Both upgrades at once (a pre-rc.7q registration: no onFailure, the old
// matcher), and a group ape's hook shares with a person's: ape's moves to
// a group of its own, so the person's hook is not widened to Monitor.
func TestMergeHooks_PreMonitorMatcherInASharedGroup(t *testing.T) {
	p := settingsProject(t, `{"hooks": {"PreToolUse": [{"matcher": "`+preMonitorMatcher+`", "hooks": [
	  {"type": "command", "command": "./mine.sh"},
	  {"type": "command", "command": "ape hook pre-tool"}]}]}}`)
	require.Equal(t, []string{"hooks.missing", "hooks.missing", "hooks.missing"}, codesOf(p.CheckHooks()))
	require.Len(t, p.monitorUnguardedNotices(), 1)
	require.Len(t, p.failOpenNotices(), 1)

	added, err := MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Contains(t, added, "PreToolUse: ape hook pre-tool (onFailure: block)")
	require.Contains(t, added, "PreToolUse: ape hook pre-tool (matcher: +Monitor)")
	require.Empty(t, p.CheckHooks())
	require.Empty(t, p.monitorUnguardedNotices())
	require.Empty(t, p.failOpenNotices())

	data, err := os.ReadFile(p.SettingsJSON())
	require.NoError(t, err)
	var root struct {
		Hooks map[string][]struct {
			Matcher string           `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &root))
	groups := root.Hooks["PreToolUse"]
	require.Len(t, groups, 2, "%s", data)
	require.Equal(t, preMonitorMatcher, groups[0].Matcher, "the person's hook keeps its matcher")
	require.Equal(t, []map[string]any{{"type": "command", "command": "./mine.sh"}}, groups[0].Hooks)
	require.Equal(t, PreToolMatcher, groups[1].Matcher)
	require.Equal(t, []map[string]any{{"type": "command", "command": "ape hook pre-tool", "onFailure": "block"}}, groups[1].Hooks)

	added, err = MergeHooks(p.SettingsJSON())
	require.NoError(t, err)
	require.Empty(t, added, "idempotent")
}

func TestInlineHooks_PreToolCoversMonitor(t *testing.T) {
	for _, h := range InlineHooks() {
		if h.Event == "PreToolUse" {
			require.Equal(t, PreToolMatcher, h.Matcher)
			require.Contains(t, strings.Split(h.Matcher, "|"), "Monitor")
		}
	}
}
