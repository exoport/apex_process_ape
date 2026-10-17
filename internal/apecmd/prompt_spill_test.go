package apecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// TestSpillPromptLine — `ape prompt --agent` with a long prompt writes it
// to the record directory and types a pointer; anything short, and any
// plain prompt, is typed as before.
func TestSpillPromptLine(t *testing.T) {
	long := "Line one of the brief.\n" + strings.Repeat("More of the brief. ", 50)

	t.Run("agent, long", func(t *testing.T) {
		dir := t.TempDir()
		line, argsFile, err := spillPromptLine("apex-agent-sm", long, assemblePromptLine("apex-agent-sm", long), dir)
		require.NoError(t, err)
		require.Equal(t, runlog.PromptArgsFile, argsFile)
		path := runlog.PromptArgsPath(dir)
		require.Equal(t, "/apex-agent-sm --autonomous -- "+repl.ArgsPointer(path), line)
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, long+"\n", string(got))
	})
	t.Run("agent, short", func(t *testing.T) {
		dir := t.TempDir()
		in := assemblePromptLine("apex-agent-sm", "short brief")
		line, argsFile, err := spillPromptLine("apex-agent-sm", "short brief", in, dir)
		require.NoError(t, err)
		require.Equal(t, in, line)
		require.Empty(t, argsFile)
		require.NoFileExists(t, filepath.Join(dir, runlog.PromptArgsFile))
	})
	t.Run("plain prompt, long", func(t *testing.T) {
		dir := t.TempDir()
		oneLine := strings.TrimSpace(strings.Repeat("More of the brief. ", 50))
		line, argsFile, err := spillPromptLine("", oneLine, oneLine, dir)
		require.NoError(t, err)
		require.Equal(t, oneLine, line, "a plain prompt is a plain message whatever its length")
		require.Empty(t, argsFile)
		require.NoFileExists(t, filepath.Join(dir, runlog.PromptArgsFile))
	})
}

// TestSpillPromptLine_Whitespace — a prompt that would not arrive as
// written when typed goes to prompt.md byte for byte, with or without
// --agent and whatever its length; a single-spaced one is typed as before.
func TestSpillPromptLine_Whitespace(t *testing.T) {
	for _, text := range []string{
		"keep  this  gap",
		"col one\tcol two",
		"Line one of the brief.\nLine two.",
		"windows\r\nline",
		"bell\x07",
	} {
		for _, agent := range []string{"", "apex-agent-sm"} {
			t.Run(fmt.Sprintf("%q/agent=%q", text, agent), func(t *testing.T) {
				dir := t.TempDir()
				line, argsFile, err := spillPromptLine(agent, text, assemblePromptLine(agent, text), dir)
				require.NoError(t, err)
				require.Equal(t, runlog.PromptArgsFile, argsFile)
				path := runlog.PromptArgsPath(dir)
				require.Equal(t, assemblePromptLine(agent, repl.ArgsPointer(path)), line)
				got, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, []byte(text+"\n"), got, "byte-exact")
			})
		}
	}
	t.Run("single-spaced", func(t *testing.T) {
		for _, agent := range []string{"", "apex-agent-sm"} {
			dir := t.TempDir()
			in := assemblePromptLine(agent, "fix the parser — naïvely")
			line, argsFile, err := spillPromptLine(agent, "fix the parser — naïvely", in, dir)
			require.NoError(t, err)
			require.Equal(t, in, line)
			require.Empty(t, argsFile)
			require.NoFileExists(t, filepath.Join(dir, runlog.PromptArgsFile))
		}
	})
}
