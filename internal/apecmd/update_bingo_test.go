package apecmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDetectBingoPin: `ape update` from `~/go/bin/ape → ape-v0.8.3` wrote
// the new release into `ape-v0.8.3`, falsifying the pin. The pin is
// recognised by the resolved binary's name, and -l is carried exactly when
// the bare name is bingo's symlink to it.
func TestDetectBingoPin(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("symlinks need privileges on Windows")
	}
	write := func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, os.WriteFile(path, []byte("bin"), 0o755))
	}

	t.Run("linked pin, reached through the bare name", func(t *testing.T) {
		dir := t.TempDir()
		pinned := filepath.Join(dir, "ape-v0.8.3")
		write(t, pinned)
		require.NoError(t, os.Symlink(pinned, filepath.Join(dir, "ape")))

		p, ok := detectBingoPin(filepath.Join(dir, "ape"))
		require.True(t, ok)
		require.True(t, p.linked)
		require.Equal(t, "bingo get -l "+apeModulePath+"@v0.8.4", p.command("0.8.4"))
		require.Equal(t, "bingo get -l "+apeModulePath+"@latest", p.command(""))
	})
	t.Run("unlinked pin", func(t *testing.T) {
		dir := t.TempDir()
		pinned := filepath.Join(dir, "ape-v0.8.3")
		write(t, pinned)

		p, ok := detectBingoPin(pinned)
		require.True(t, ok)
		require.False(t, p.linked, "no bare symlink, so no -l")
		require.Equal(t, "bingo get "+apeModulePath+"@v0.8.4", p.command("v0.8.4"))
	})
	t.Run("bare name pointing at another version is not this pin's link", func(t *testing.T) {
		dir := t.TempDir()
		old, other := filepath.Join(dir, "ape-v0.8.3"), filepath.Join(dir, "ape-v0.8.4")
		write(t, old)
		write(t, other)
		require.NoError(t, os.Symlink(other, filepath.Join(dir, "ape")))

		p, ok := detectBingoPin(old)
		require.True(t, ok)
		require.False(t, p.linked)
	})
	t.Run("release installs are not pins", func(t *testing.T) {
		for _, name := range []string{"ape", "ape.bak", "ape-v0.8.3.bak", "ape-dev", "ape-v0.8"} {
			dir := t.TempDir()
			bin := filepath.Join(dir, name)
			write(t, bin)
			_, ok := detectBingoPin(bin)
			require.False(t, ok, name)
		}
	})
	t.Run("a prerelease pin is a pin", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "ape-v0.9.0-rc.1")
		write(t, bin)
		_, ok := detectBingoPin(bin)
		require.True(t, ok)
	})
}
