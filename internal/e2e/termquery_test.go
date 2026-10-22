//go:build !windows

package e2e

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/require"
)

// W8b (D365): no ape command asks the terminal anything at startup.
// Bubble Tea v1's package init() sent an OSC 11 background-colour query
// from every ape process whose stdout was a tty under an xterm-like TERM,
// and waited up to 5 s for an answer. A terminal that never answers — a
// PTY nobody reads for replies, as a session driver or a test fake hands
// its hooks — made every command, `ape notify` and `ape hook stop`
// included, take 5 s. The PTY here never answers: each command must end
// well inside that and write no query.
func TestApe_NoTerminalQueryAtStartup(t *testing.T) {
	ape := buildApe(t)
	home := t.TempDir()
	for _, args := range [][]string{{"version"}, {"notify", "--event", "Stop"}, {"hook", "stop"}} {
		for _, term := range []string{"xterm-256color", ""} {
			t.Run(strings.Join(args, " ")+" TERM="+term, func(t *testing.T) {
				out, took := runOnSilentPTY(t, ape, term, home, args...)
				require.NotContains(t, out, "\x1b]11;?", "an OSC 11 query went to the terminal")
				require.NotContains(t, out, "\x1b[6n", "a cursor-position query went to the terminal")
				require.Less(t, took, 2*time.Second, "output: %q", out)
			})
		}
	}
}

// runOnSilentPTY runs ape with stdout on a PTY that is its controlling
// terminal, as `script` gives a command, stdin on a pipe holding an empty
// hook payload, as Claude Code gives a hook, and stderr on a pipe, which
// keeps the update check (a tty-stderr notice) off the network. Nothing
// answers what ape writes. It returns what reached the terminal and how
// long the command took.
func runOnSilentPTY(t *testing.T, ape, term, home string, args ...string) (string, time.Duration) {
	t.Helper()
	p, err := pty.New()
	require.NoError(t, err)
	defer p.Close()
	up, ok := p.(pty.UnixPty)
	require.True(t, ok)
	var out bytes.Buffer
	read := make(chan struct{})
	go func() {
		defer close(read)
		_, _ = io.Copy(&out, up.Master())
	}()

	cmd := exec.Command(ape, args...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	if term != "" {
		cmd.Env = append(cmd.Env, "TERM="+term)
	}
	cmd.Dir = home
	cmd.Stdin = strings.NewReader("{}")
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = up.Slave(), &stderr
	// The PTY is the child's controlling terminal (fd 1): a terminal
	// query is only sent to one, from the foreground process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 1}
	start := time.Now()
	require.NoError(t, cmd.Run(), "stderr: %s", stderr.String())
	took := time.Since(start)
	// The child is gone and this is the last slave descriptor: the
	// master's reader sees EOF (EIO) and ends.
	_ = up.Slave().Close()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("the PTY's reader did not end")
	}
	return out.String(), took
}
