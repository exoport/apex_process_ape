package repl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/cost"
)

// argsProbeSkill is the scratch skill args_whitespace types: its body
// brackets $ARGUMENTS between markers, so the expanded body claude records
// in the transcript says exactly what the skill received, with no model
// asked to repeat it.
const argsProbeSkill = "ape-args-probe"

const argsProbeBody = `---
name: ` + argsProbeSkill + `
description: Probe for ape's args_whitespace contract check. Not for general use.
---
APEARGS[$ARGUMENTS]APEARGS

If the text between the two markers above tells you to read a file, read it with the Read tool, once. Then reply with the single word DONE and nothing else.
`

// argsMarkerRe finds the bracketed arguments in a recorded skill body.
var argsMarkerRe = regexp.MustCompile(`(?s)APEARGS\[(.*?)\]APEARGS`)

// liveArgsWhitespace measures the reason for repl.TypesVerbatim and proves
// the route it sends text down, in one Haiku session of two short turns.
//
// (a) It types the probe skill with a double space in its arguments,
// exactly as given (no ape line assembly in between), and reads what the
// skill received from the transcript. That half is an observation, logged
// either way: ape's rule does not depend on it, because ape's own line
// assembly splits --args on whitespace and a line break or a tab is a key
// whatever Claude Code does with spaces. It fails only when there is
// nothing to observe (the skill did not run, or $ARGUMENTS was not
// substituted).
//
// (b) It writes text holding a double space, a tab and a newline to a
// file, types the skill with ArgsPointer(file) as stepLine does, and
// requires that the pointer arrived intact and that the session's Read of
// that file returned the bytes ape wrote. That half is the contract: a
// pointer that does not survive, or a session that does not read the file
// it names, loses every spilled argument.
func liveArgsWhitespace(t *testing.T, claudeBin string) {
	t.Helper()
	if os.Getenv("APE_CLAUDE_LIVE_TOKENS") == "0" {
		t.Skip("APE_CLAUDE_LIVE_TOKENS=0 — the args-whitespace check submits two turns")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, token := uniqueWorkdir(t)
	t.Cleanup(func() { removeScratchTranscripts(t, home, token) })
	skillDir := filepath.Join(dir, ".claude", "skills", argsProbeSkill)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(argsProbeBody), 0o600))

	name := sessionName(t, "args-ws")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	argv := []string{claudeBin, "--dangerously-skip-permissions", "--model", cost.ResolveFamilyAlias("haiku")}
	require.NoError(t, NewSessionWithEnv(ctx, name, dir, argv, EffortEnv("low")))
	t.Cleanup(func() { _ = KillSession(context.Background(), name) })
	require.NoError(t, WaitForReady(ctx, name))
	time.Sleep(2 * time.Second) // the footer paints a beat after the first ready frame

	projects := filepath.Join(home, ".claude", "projects", "*"+token+"*")

	// (a) typed as is.
	const typed = "alpha  bravo"
	require.False(t, TypesVerbatim(typed), "the typed fixture must be one ape spills")
	require.NoError(t, SendCommand(ctx, name, "/"+argsProbeSkill+" "+typed))
	scan := waitArgsProbe(ctx, projects, func(s argsProbeScan) bool { return len(s.markers) >= 1 && s.dones >= 1 })
	require.NotEmpty(t, scan.markers,
		"probe invalid: no recorded skill body carries the APEARGS markers, so the %s skill never ran "+
			"(not discovered in the scratch workdir %s?) or its body was not recorded as a user message. "+
			"This says nothing about whitespace yet.\nlast pane:\n%s", argsProbeSkill, dir, paneOf(ctx, name))
	got := scan.markers[0]
	require.NotEqual(t, "$ARGUMENTS", got, "probe invalid: claude no longer substitutes $ARGUMENTS in a skill body")
	if got == typed {
		t.Logf("(a) a typed double space reached the skill intact (%q): Claude Code keeps runs of spaces; "+
			"the whitespace spill still covers ape's own --args split, line breaks and control keys", got)
	} else {
		t.Logf("(a) typed %q, the skill received %q: Claude Code alters typed whitespace", typed, got)
	}
	require.GreaterOrEqual(t, scan.dones, 1, "the first turn never finished.\nlast pane:\n%s", paneOf(ctx, name))

	// (b) through the args file.
	payload := "alpha  bravo\tcharlie\nline two  ends here"
	path := filepath.Join(dir, "args-probe.args.md")
	require.NoError(t, os.WriteFile(path, []byte(payload+"\n"), 0o600))
	pointer := ArgsPointer(path)
	require.True(t, TypesVerbatim(pointer) && FitsTypedLine(pointer),
		"the pointer for %s must itself type as written; a workdir path with a run of spaces breaks the probe", path)
	require.NoError(t, SendCommand(ctx, name, "/"+argsProbeSkill+" "+pointer))
	scan = waitArgsProbe(ctx, projects, func(s argsProbeScan) bool {
		_, read := s.reads[path]
		return len(s.markers) >= 2 && read && s.dones >= 2
	})
	require.GreaterOrEqual(t, len(scan.markers), 2,
		"the second skill run left no APEARGS markers.\nlast pane:\n%s", paneOf(ctx, name))
	require.Equal(t, pointer, scan.markers[1],
		"the typed ArgsPointer line did not reach the skill as written, so a spilled argument's file is "+
			"named wrongly or not at all")
	content, read := scan.reads[path]
	require.True(t, read,
		"the session never Read %s, the file the pointer names (it read: %v). Every argument ape spills "+
			"to a file is lost if the session does not follow the pointer.\nlast pane:\n%s",
		path, scan.readPaths(), paneOf(ctx, name))
	require.Equal(t, payload, strings.TrimSuffix(content, "\n"),
		"the Read of the args file did not return the bytes ape wrote: a spilled argument with a run of "+
			"spaces, a tab or a newline still arrives altered")
	t.Logf("(b) the args file route delivered %q byte-exact", payload)
}

// argsProbeScan is what the probe session's transcripts record so far.
type argsProbeScan struct {
	// markers is every distinct APEARGS[…] value outside the model's own
	// records, in order: the arguments each probe run received.
	markers []string
	// reads maps a file path to the content a Read tool result returned.
	reads map[string]string
	// dones counts assistant texts that say DONE: finished probe turns.
	dones int
}

func (s argsProbeScan) readPaths() []string {
	out := make([]string, 0, len(s.reads))
	for p := range s.reads {
		out = append(out, p)
	}
	return out
}

// waitArgsProbe polls the transcripts under the projects glob until done
// holds or ctx expires, and returns the last scan.
func waitArgsProbe(ctx context.Context, projects string, done func(argsProbeScan) bool) argsProbeScan {
	for {
		s := scanArgsProbe(projects)
		if done(s) {
			return s
		}
		select {
		case <-ctx.Done():
			return s
		case <-time.After(2 * time.Second):
		}
	}
}

// scanArgsProbe reads every main transcript under the projects glob.
// Markers come from any string in a record that is not the model's own,
// so the shape the expanded skill body is recorded in (a meta user
// message, an attachment) does not matter, and a model echoing the
// markers back cannot count. Reads come from a record's
// toolUseResult.file, which holds the file's content without the line
// numbers the model sees.
func scanArgsProbe(projects string) argsProbeScan {
	s := argsProbeScan{reads: map[string]string{}}
	seen := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(projects, "*.jsonl"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			//nolint:tagliatelle // Claude Code's transcript field names, not ours
			var rec struct {
				Type    string          `json:"type"`
				Message json.RawMessage `json:"message"`
				Result  json.RawMessage `json:"toolUseResult"`
			}
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			if rec.Type == "assistant" {
				s.dones += doneTexts(rec.Message)
				continue
			}
			var whole any
			if json.Unmarshal([]byte(line), &whole) == nil {
				for _, str := range stringsIn(whole) {
					for _, m := range argsMarkerRe.FindAllStringSubmatch(str, -1) {
						if !seen[m[1]] {
							seen[m[1]] = true
							s.markers = append(s.markers, m[1])
						}
					}
				}
			}
			//nolint:tagliatelle // Claude Code's transcript field names, not ours
			var res struct {
				File struct {
					FilePath string  `json:"filePath"`
					Content  *string `json:"content"`
				} `json:"file"`
			}
			// A tool result that is a plain string (an error) is no Read.
			if json.Unmarshal(rec.Result, &res) == nil && res.File.Content != nil {
				s.reads[res.File.FilePath] = *res.File.Content
			}
		}
	}
	return s
}

// doneTexts counts the text blocks of an assistant message that say DONE.
func doneTexts(message json.RawMessage) int {
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(message, &msg) != nil {
		return 0
	}
	n := 0
	for _, b := range msg.Content {
		if b.Type == "text" && strings.Contains(b.Text, "DONE") {
			n++
		}
	}
	return n
}

// stringsIn returns every string inside a decoded JSON value.
func stringsIn(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, stringsIn(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range x {
			out = append(out, stringsIn(e)...)
		}
		return out
	}
	return nil
}
