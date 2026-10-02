package apecmd

import (
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
		line, argsFile, err := spillPromptLine("", long, long, dir)
		require.NoError(t, err)
		require.Equal(t, long, line, "a plain prompt is a plain message either way")
		require.Empty(t, argsFile)
	})
}
