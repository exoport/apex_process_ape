package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// E2 through the commands: pre-tool reads a person act's command line as
// the command will read itself, grants it on the person's words, and the
// command consumes the grant and records by: user.name with the prompt.
// Payloads are shaped like the ones measured on claude 2.1.303 (Q102).

// e2Session is one person's Claude Code session in a project.
type e2Session struct {
	root, session, transcript string
}

func newE2Session(t *testing.T, root, session string) *e2Session {
	t.Helper()
	// The commands run as that session's Bash tool calls (D219).
	t.Setenv(apexv1.EnvClaudeSessionID, session)
	return &e2Session{root: root, session: session, transcript: filepath.Join(t.TempDir(), session+".jsonl")}
}

// say is the person typing prompt: the transcript line Claude Code writes,
// and the UserPromptSubmit hook.
func (s *e2Session) say(t *testing.T, promptID, prompt string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "user", "isSidechain": false, "userType": "external", "promptId": promptID, "sessionId": s.session,
		"message": map[string]any{"role": "user", "content": prompt},
	})
	require.NoError(t, err)
	f, err := os.OpenFile(s.transcript, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.Write(append(line, '\n'))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	payload, err := json.Marshal(map[string]any{
		"session_id": s.session, "transcript_path": s.transcript, "cwd": s.root, "prompt_id": promptID,
		"permission_mode": "bypassPermissions", "hook_event_name": "UserPromptSubmit", "prompt": prompt,
	})
	require.NoError(t, err)
	stdout, _, err := v1ExecIn(t, newHookCmd, string(payload), "user-prompt")
	require.NoError(t, err)
	require.Empty(t, stdout, "the prompt hook never blocks")
}

// preTool is the model running command in the turn promptID opened: the
// deny reason, or "" when allowed.
func (s *e2Session) preTool(t *testing.T, promptID, command string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"session_id": s.session, "transcript_path": s.transcript, "cwd": s.root, "prompt_id": promptID,
		"permission_mode": "bypassPermissions", "hook_event_name": "PreToolUse", "tool_name": "Bash",
		"tool_input": map[string]any{"command": command, "description": "run it"}, "tool_use_id": "toolu_1",
	})
	require.NoError(t, err)
	stdout, _, err := v1ExecIn(t, newHookCmd, string(payload), "pre-tool")
	require.NoError(t, err)
	if strings.TrimSpace(stdout) == "" {
		return ""
	}
	var out struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	require.Equal(t, "deny", out.HookSpecificOutput.PermissionDecision)
	return out.HookSpecificOutput.PermissionDecisionReason
}

// e2Repo is v1Repo with a proposed M2 (its gate red), the person's
// user.name, and a person's Claude Code session (CLAUDECODE).
func e2Repo(t *testing.T, gate string) string {
	t.Helper()
	root := v1Repo(t)
	v1Git(t, root, "config", "user.name", "Ann Person")
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate2.sh"), []byte("#!/bin/sh\n"+gate+"\n"), 0o755))
	v1Git(t, root, "add", "gate2.sh")
	v1Git(t, root, "commit", "-q", "-m", "gate2")
	_, _, err := v1Exec(t, newMilestoneCmd, "new", "Second", "--gate", "./gate2.sh", "--goal", "more", "--cwd", root)
	require.NoError(t, err)
	t.Setenv("CLAUDECODE", "1")
	return root
}

// The command line pre-tool judges and the one the command reads itself
// as are one: flag order, --flag=value, --json, --wait and --cwd aside.
func TestPersonCall_SameCommandLineBothSides(t *testing.T) {
	cmd := newMilestoneCmd()
	approve, rest, err := cmd.Find([]string{"approve", "M2", "--json", "--by", "Ann", "--wait", "1m"})
	require.NoError(t, err)
	require.NoError(t, approve.ParseFlags(rest))
	own := personCallOf(approve, approve.Flags().Args())
	require.NotNil(t, own)
	for _, line := range []string{
		"ape milestone approve M2 --by Ann",
		"cd /x && APE_TOOLCHAIN=local ape milestone approve --by=Ann M2 --wait 9m",
		"ape milestone approve M2 --cwd . --by 'Ann' --json",
	} {
		calls := personCallsIn(line)
		require.Len(t, calls, 1, line)
		require.Equal(t, own.ArgvSHA256, calls[0].ArgvSHA256, line)
		require.True(t, calls[0].Literal, line)
		require.Equal(t, "approve M2", calls[0].Phrase())
	}
	require.NotEqual(t, own.ArgvSHA256, personCallsIn("ape milestone approve M2 --by Bob")[0].ArgvSHA256)
	require.NotEqual(t, own.ArgvSHA256, personCallsIn("ape milestone approve M3 --by Ann")[0].ArgvSHA256)
	require.False(t, personCallsIn(`ape milestone approve M2 --by "$USER"`)[0].Literal)

	// Not person acts: a plain close and archive, a delegate's verdict, status.
	for _, line := range []string{
		"ape milestone close M1", "ape change archive add-wave", "ape status --json",
		`ape change accept add-wave T1 --verdict pass --reason ok`, "ape change accept add-wave T1 --delegate",
		"ape milestone approve", "echo ape milestone approve M2",
	} {
		require.Empty(t, personCallsIn(line), line)
	}
	for line, phrase := range map[string]string{
		`ape milestone close M3 --accept-red "known drift" --by Ann`:         "accept red on M3",
		"ape change archive add-cache --allow-pending-human":                 "allow pending human on add-cache",
		"ape change accept add-cache T3":                                     "accept T3 of add-cache",
		"ape change accept add-cache --task T3 --by Ann":                     "accept T3 of add-cache",
		`ape change answer add-cache --text "use Redis, not the file cache"`: "answer add-cache: use Redis, not the file cache",
		"ape project accept-config":                                          "accept the config",
		"ape decision accept ADR-0012":                                       "accept ADR-0012",
		"ape change approve fix-typo":                                        "approve fix-typo",
	} {
		calls := personCallsIn(line)
		require.Len(t, calls, 1, line)
		require.Equal(t, phrase, calls[0].Phrase(), line)
	}
}

// The person types "approve M2"; the model's `ape milestone approve M2` is
// granted, runs, and records by: user.name with the prompt. Without the
// words — or after the grant was used — it is refused, naming the phrase.
func TestE2_MilestoneApproveOnThePersonsWords(t *testing.T) {
	root := e2Repo(t, "exit 1")
	s := newE2Session(t, root, "fcaf711f")
	cmdline := "ape milestone approve M2"

	// T1: no words, no grant — pre-tool denies, and the command refuses.
	s.say(t, "p1", "where are we?")
	reason := s.preTool(t, "p1", cmdline)
	require.Contains(t, reason, "does not ask for it")
	require.Contains(t, reason, "**approve M2**")
	stdout, _, err := v1Exec(t, newMilestoneCmd, "approve", "M2", "--json", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "session.person-only"`)
	require.Contains(t, stdout, "**approve M2**")
	require.Contains(t, stdout, "a `!` line fires no hook")

	// The person's words.
	s.say(t, "p2", "That gate looks right. Approve M2.")
	require.Empty(t, s.preTool(t, "p2", cmdline))
	// Q-OI-2: another --by is refused, before the grant is spent.
	require.Contains(t, s.preTool(t, "p2", cmdline+" --by Bob"), "git config user.name")
	stdout, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, `M2 — Second: approved (by Ann Person (prompt `)
	require.Contains(t, stdout, `: "Approve M2")`)
	roadmap, err := os.ReadFile(filepath.Join(root, "development", "roadmap.md"))
	require.NoError(t, err)
	require.Contains(t, string(roadmap), `Status: approved (by Ann Person (prompt `)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Contains(t, recs["M2"].Approval.By, `"Approve M2"`)

	// T3: used once — the same command again needs the words again.
	require.Contains(t, s.preTool(t, "p2", cmdline), "granted it once already")
	_, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
}

// Every guarded act refuses without a grant in a person's Claude Code
// session, naming its phrase (OI-4: accept --by and decision accept are
// two of them); the delegate's --verdict is no person act.
func TestE2_EveryPersonActNeedsAGrant(t *testing.T) {
	root := e2Repo(t, "exit 1")
	for _, tc := range []struct {
		build  func() *cobra.Command
		args   []string
		phrase string
	}{
		{newMilestoneCmd, []string{"approve", "M2"}, "approve M2"},
		{newChangeCmd, []string{"approve", "fix-typo"}, "approve fix-typo"},
		{newChangeCmd, []string{"answer", "add-wave", "--text", "use Redis"}, "answer add-wave: use Redis"},
		{newChangeCmd, []string{"accept", "add-wave", "T1", "--by", "Ann Person"}, "accept T1 of add-wave"},
		{newProjectCmd, []string{"accept-config"}, "accept the config"},
		{newMilestoneCmd, []string{"close", "M1", "--accept-red", "known", "--by", "Ann Person"}, "accept red on M1"},
		{newChangeCmd, []string{"archive", "add-wave", "--allow-pending-human"}, "allow pending human on add-wave"},
		{newDecisionCmd, []string{"accept", "ADR-0001"}, "accept ADR-0001"},
	} {
		stdout, _, err := v1Exec(t, tc.build, append(tc.args, "--json", "--cwd", root)...)
		require.Equal(t, ExitUsage, exitCodeOf(t, err), "%v: %s", tc.args, stdout)
		require.Contains(t, stdout, `"code": "session.person-only"`, tc.args)
		require.Contains(t, stdout, "**"+tc.phrase+"**", tc.args)
	}
	// The delegate's verdict path is unchanged: its own refusal.
	stdout, _, err := v1Exec(t, newChangeCmd, "accept", "add-wave", "T1", "--verdict", "pass", "--reason", "ok", "--json", "--cwd", root)
	require.NotEqual(t, ExitOK, exitCodeOf(t, err))
	require.NotContains(t, stdout, "session.person-only")
	require.Empty(t, strings.TrimSpace(v1Git(t, root, "status", "--porcelain")), "nothing was written")
}

// Q-OI-2: a --by in a model session must be git config user.name.
func TestE2_ByMismatch(t *testing.T) {
	root := e2Repo(t, "exit 1")
	s := newE2Session(t, root, "s1")
	s.say(t, "p1", "accept the config")
	// pre-tool knows user.name too, and denies first; the command is the
	// backstop when a grant exists (granted on the right name).
	require.Contains(t, s.preTool(t, "p1", "ape project accept-config --by Bob"), "drop --by")
	require.Empty(t, s.preTool(t, "p1", "ape project accept-config --by 'Ann Person'"))
	v1Git(t, root, "config", "user.name", "Someone Else")
	stdout, _, err := v1Exec(t, newProjectCmd, "accept-config", "--by", "Ann Person", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "session.by-mismatch"`)
	require.Contains(t, stdout, `"action": "person"`)
}

// accept-config, answer and decision accept on the person's words.
func TestE2_InProcessActsRecordThePrompt(t *testing.T) {
	root := e2Repo(t, "exit 1")
	s := newE2Session(t, root, "s1")
	s.say(t, "p1", "ok, accept the config")
	require.Empty(t, s.preTool(t, "p1", "ape project accept-config"))
	stdout, _, err := v1Exec(t, newProjectCmd, "accept-config", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, v1Git(t, root, "log", "-1", "--format=%s"), `apex: config accepted by Ann Person (prompt `)
	require.Contains(t, v1Git(t, root, "log", "-1", "--format=%s"), `: "ok, accept the config")`)

	// ADR-0001 is APEX's standard (D205): the project's first is ADR-0002.
	_, _, err = v1Exec(t, newDecisionCmd, "new", "use-sqlite", "--cwd", root)
	require.NoError(t, err)
	s.say(t, "p2", "accept ADR-0002")
	require.Empty(t, s.preTool(t, "p2", "ape decision accept ADR-0002"))
	stdout, _, err = v1Exec(t, newDecisionCmd, "accept", "ADR-0002", "--cwd", root)
	require.NoError(t, err, stdout)
	data, err := os.ReadFile(filepath.Join(root, "_apex", "state", "decisions.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `Ann Person (prompt `)
}

// T6/T7 through the hook: the records are protected, and in a session ape
// launched pre-tool grants nothing.
func TestE2_HookGuardsAndApeSession(t *testing.T) {
	root := e2Repo(t, "exit 1")
	s := newE2Session(t, root, "s1")
	s.say(t, "p1", "approve M2")
	require.Contains(t, s.preTool(t, "p1", "cat _apex/state/prompts/s1.jsonl"), "what a person act rests on")
	require.Contains(t, s.preTool(t, "p1", `echo '{}' | ape hook user-prompt`), "what a person act rests on")
	require.Contains(t, s.preTool(t, "p1", "ape milestone approve M2 && ape project accept-config"), "as a command of its own")
	t.Setenv("APE_SESSION", "task/test")
	require.Empty(t, s.preTool(t, "p1", "ape milestone approve M2"), "allowed: the command refuses it")
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	entries, _ := os.ReadDir(p.GrantsDir())
	for _, e := range entries {
		require.True(t, e.IsDir(), "no grant written under APE_SESSION: %s", e.Name())
	}
	s.say(t, "p2", "approve M2")
	data, _ := os.ReadFile(filepath.Join(p.PromptsDir(), "s1.jsonl"))
	require.NotContains(t, string(data), `"p2"`, "nothing recorded under APE_SESSION")
	_, _, err = v1Exec(t, newMilestoneCmd, "approve", "M2", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
}
