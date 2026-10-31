package conductorterm

import (
	"context"
	"time"

	"github.com/exoport/apex_process_ape/internal/repl"
)

// ReplDriver is the orchestrator terminal on ape's own PTY (internal/repl):
// Claude Code started as a person would start it in the project — no
// APE_SESSION, so the orchestrator starts its loops as a person's session
// does — with bypass permissions, and its own session id so the
// supervisor finds its transcript.
type ReplDriver struct {
	Name, Dir string
	Argv, Env []string
	// ReadyTimeout bounds the wait for the REPL (default 3 m).
	ReadyTimeout time.Duration
}

// Start spawns the session and waits for its REPL.
func (d *ReplDriver) Start(ctx context.Context) error {
	if err := repl.NewSessionWithEnv(ctx, d.Name, d.Dir, d.Argv, d.Env); err != nil {
		return err
	}
	timeout := d.ReadyTimeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return repl.WaitForReady(rctx, d.Name)
}

// Type types line and submits it.
func (d *ReplDriver) Type(ctx context.Context, line string) error {
	return repl.SendCommand(ctx, d.Name, line)
}

// Pane is the rendered screen.
func (d *ReplDriver) Pane(ctx context.Context) (string, error) { return repl.CapturePane(ctx, d.Name) }

// LastOutput is when the terminal last printed.
func (d *ReplDriver) LastOutput() (time.Time, bool) { return repl.LastOutputAt(d.Name) }

// Done is closed when the session ended.
func (d *ReplDriver) Done() <-chan struct{} {
	if ch := repl.SessionDone(context.Background(), d.Name); ch != nil {
		return ch
	}
	done := make(chan struct{})
	close(done)
	return done
}

// Stop ends the session.
func (d *ReplDriver) Stop(ctx context.Context) error { return repl.KillSession(ctx, d.Name) }
