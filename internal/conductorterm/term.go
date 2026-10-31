// Package conductorterm is the orchestrator terminal a person's conductor
// drives (APEX v1.4.2, D659 A8, D660.2): one supervisor process per
// project owns a PTY running Claude Code with /apex-orchestrator, types
// the conductor's messages into it — marked, one line, only while the
// session is idle — and reports the terminal's liveness as project events
// (orchestrator-dead, orchestrator-idle with a message pending).
//
// The conductor talks to the supervisor through files under the project's
// conductor folder (runlog.ConductorDir): it queues a message in inbox/
// (Send), reads the orchestrator's replies from its transcript (Read), and
// reads the supervisor's state (LoadState). Nothing here is remote-aware:
// a remote project is the same calls over ssh, on its host.
package conductorterm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// The liveness events (D660.2, D661).
const (
	EventDead = apexv1.EventOrchestratorDead
	EventIdle = apexv1.EventOrchestratorIdle
)

// DefaultIdleAlert is conductor.idle_alert's default.
const DefaultIdleAlert = 30 * time.Minute

// MaxMessage bounds one message: typed as one line, under Claude Code's
// paste collapse (800 characters) with room for the mark.
const MaxMessage = 600

// Message is one queued conductor message.
type Message struct {
	Seq  int64  `json:"seq"`
	At   string `json:"at"`
	Text string `json:"text"`
}

// Delivered is a message typed into the terminal.
//
//nolint:tagliatelle // ape's state files are snake_case
type Delivered struct {
	Seq     int64  `json:"seq"`
	TypedAt string `json:"typed_at"`
}

// State is the supervisor's record, rewritten on every change.
//
//nolint:tagliatelle // ape's state files are snake_case
type State struct {
	PID       int         `json:"pid"`
	Name      string      `json:"name"`
	StartedAt string      `json:"started_at"`
	Alive     bool        `json:"alive"`
	Idle      bool        `json:"idle"`
	IdleSince string      `json:"idle_since,omitempty"`
	Pending   []int64     `json:"pending"`
	Delivered []Delivered `json:"delivered"`
	// Transcript is the orchestrator session's transcript, once found.
	Transcript string `json:"transcript,omitempty"`
	// LastReplyAt is the newest assistant message's stamp.
	LastReplyAt string `json:"last_reply_at,omitempty"`
	EndedAt     string `json:"ended_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

func statePath(root string) string { return filepath.Join(runlog.ConductorDir(root), "terminal.json") }
func inboxDir(root string) string  { return filepath.Join(runlog.ConductorDir(root), "inbox") }

// PanePath is the terminal's last pane, rewritten by the supervisor:
// what `ape conductor watch` shows.
func PanePath(root string) string { return filepath.Join(runlog.ConductorDir(root), "pane.txt") }

// LogPath is the supervisor's own log.
func LogPath(root string) string { return filepath.Join(runlog.ConductorDir(root), "supervisor.log") }

// ErrNoTerminal: no supervisor ever ran for the project.
var ErrNoTerminal = errors.New("conductorterm: no orchestrator terminal for this project (ape conductor start)")

// LoadState reads the supervisor's record.
func LoadState(root string) (*State, error) {
	data, err := os.ReadFile(statePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoTerminal
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveState(root string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runlog.ConductorDir(root), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(statePath(root), append(data, '\n'))
}

// Send queues text for the terminal and returns its message. The text
// becomes one line; it is refused when longer than MaxMessage (write the
// rest to a file in the project and name it).
func Send(root, text string, now time.Time) (*Message, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil, errors.New("conductorterm: an empty message")
	}
	if n := len([]rune(text)); n > MaxMessage {
		return nil, fmt.Errorf("conductorterm: the message is %d characters, over %d: write the details to a file in the project and name its path", n, MaxMessage)
	}
	m := &Message{Seq: now.UnixNano(), At: now.UTC().Format(time.RFC3339Nano), Text: text}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(inboxDir(root), 0o755); err != nil {
		return nil, err
	}
	return m, atomicfile.Write(filepath.Join(inboxDir(root), strconv.FormatInt(m.Seq, 10)+".json"), data)
}

// queued are the inbox's messages, oldest first.
func queued(root string) []Message {
	entries, _ := os.ReadDir(inboxDir(root))
	var out []Message
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(inboxDir(root), e.Name()))
		if err != nil {
			continue
		}
		var m Message
		if json.Unmarshal(data, &m) == nil {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b Message) int { return int(a.Seq - b.Seq) })
	return out
}

// Mark is a message as ape types it: apexv1.DeliveryTag, the message's seq
// and the supervisor's nonce, the text, one line. The user-prompt hook
// records such a prompt as a notification, so it never grants a person
// act; apex-orchestrator reads it as its person's delegate.
func Mark(seq int64, nonce, text string) string {
	return fmt.Sprintf(`%sseq="%d" nonce=%q> %s </ape-delivery>`, apexv1.DeliveryTag, seq, nonce, text)
}

// Reply is one assistant message of the orchestrator.
type Reply struct {
	N    int    `json:"n"`
	At   string `json:"at"`
	Text string `json:"text"`
}

// Read returns the orchestrator's assistant messages after the n-th
// (0: all), numbered from 1, from the transcript the supervisor recorded.
func Read(root string, since int) ([]Reply, error) {
	s, err := LoadState(root)
	if err != nil {
		return nil, err
	}
	if s.Transcript == "" {
		return []Reply{}, nil
	}
	all, err := replies(s.Transcript)
	if err != nil {
		return nil, err
	}
	out := []Reply{}
	for _, r := range all {
		if r.N > since {
			out = append(out, r)
		}
	}
	return out, nil
}

// replies are a transcript's assistant text messages (not sub-agents',
// not meta), numbered.
func replies(path string) ([]Reply, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxTranscriptLine)
	var out []Reply
	for sc.Scan() {
		var l struct {
			Type        string `json:"type"`
			IsMeta      bool   `json:"isMeta"`
			IsSidechain bool   `json:"isSidechain"`
			Timestamp   string `json:"timestamp"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Type != "assistant" || l.IsMeta || l.IsSidechain {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(blk.Text)
			}
		}
		if b.Len() > 0 {
			out = append(out, Reply{N: len(out) + 1, At: l.Timestamp, Text: b.String()})
		}
	}
	return out, sc.Err()
}

// maxTranscriptLine bounds one transcript line (Claude Code writes
// api-request rows past 100 KB).
const maxTranscriptLine = 8 << 20

// Driver is what the supervisor needs of the PTY: the repl package in
// production, a fake in tests.
type Driver interface {
	Start(ctx context.Context) error
	// Type types one line and submits it.
	Type(ctx context.Context, line string) error
	// Pane is the rendered screen.
	Pane(ctx context.Context) (string, error)
	// LastOutput is when the terminal last printed.
	LastOutput() (time.Time, bool)
	// Done is closed when the session's process ended.
	Done() <-chan struct{}
	Stop(ctx context.Context) error
}

// Config is one supervisor.
type Config struct {
	Root   string
	Name   string
	Driver Driver
	// First is typed once the terminal is ready ("/apex-orchestrator").
	First string
	Nonce string
	// IdleAlert, Poll and Quiet: an orchestrator idle (ready, no output
	// for Quiet) past IdleAlert with a message pending logs
	// orchestrator-idle, once per idle stretch; the supervisor looks every
	// Poll.
	IdleAlert, Poll, Quiet time.Duration
	// Ready reports whether a pane shows the REPL waiting for input.
	Ready func(pane string) bool
	// Transcript finds the session's transcript (empty until there is
	// one), given when the session started.
	Transcript func(started time.Time) string
	// Emit logs a project event (apexv1.Project.Emit in production).
	Emit func(event, reason string)
	Now  func() time.Time
}

func (c *Config) defaults() {
	if c.IdleAlert == 0 {
		c.IdleAlert = DefaultIdleAlert
	}
	if c.Poll == 0 {
		c.Poll = 2 * time.Second
	}
	if c.Quiet == 0 {
		c.Quiet = 5 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Emit == nil {
		c.Emit = func(string, string) {}
	}
}

// Serve runs the terminal until ctx ends (Stop) or the session dies; it
// returns nil on a stop, the cause on a death.
func Serve(ctx context.Context, c Config) error {
	c.defaults()
	started := c.Now()
	s := &State{PID: os.Getpid(), Name: c.Name, StartedAt: started.UTC().Format(time.RFC3339), Alive: true, Pending: []int64{}, Delivered: []Delivered{}}
	_ = saveState(c.Root, s)
	if err := c.Driver.Start(ctx); err != nil {
		s.Alive, s.Error, s.EndedAt = false, err.Error(), c.Now().UTC().Format(time.RFC3339)
		_ = saveState(c.Root, s)
		c.Emit(EventDead, "the orchestrator terminal did not start: "+err.Error())
		return err
	}
	firstSent := c.First == ""
	idleAlerted := false
	t := time.NewTicker(c.Poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.Driver.Stop(context.WithoutCancel(ctx))
			s.Alive, s.EndedAt = false, c.Now().UTC().Format(time.RFC3339)
			_ = saveState(c.Root, s)
			return nil
		case <-c.Driver.Done():
			pane, _ := c.Driver.Pane(ctx)
			s.Alive, s.EndedAt = false, c.Now().UTC().Format(time.RFC3339)
			_ = saveState(c.Root, s)
			_ = os.WriteFile(PanePath(c.Root), []byte(pane), 0o600)
			c.Emit(EventDead, "the orchestrator terminal's session ended; last lines: "+lastLines(pane, 5))
			return errors.New("conductorterm: the orchestrator session ended")
		case <-t.C:
		}
		pane, err := c.Driver.Pane(ctx)
		if err != nil {
			continue
		}
		_ = os.WriteFile(PanePath(c.Root), []byte(pane), 0o600)
		if s.Transcript == "" && c.Transcript != nil {
			s.Transcript = c.Transcript(started)
		}
		last, _ := c.Driver.LastOutput()
		idle := c.Ready(pane) && c.Now().Sub(last) >= c.Quiet
		if idle && !s.Idle {
			s.IdleSince = c.Now().UTC().Format(time.RFC3339)
		}
		if !idle {
			s.IdleSince, idleAlerted = "", false
		}
		s.Idle = idle
		if idle {
			firstSent = c.deliver(ctx, s, firstSent)
		}
		s.Pending = c.pending(s)
		if idle && len(s.Pending) > 0 && !idleAlerted {
			if since, err := time.Parse(time.RFC3339, s.IdleSince); err == nil && c.Now().Sub(since) >= c.IdleAlert {
				c.Emit(EventIdle, fmt.Sprintf("the orchestrator has been idle since %s with %d conductor message(s) pending", s.IdleSince, len(s.Pending)))
				idleAlerted = true
			}
		}
		_ = saveState(c.Root, s)
	}
}

// deliver types into an idle terminal: the first line once, then the
// oldest queued message, marked. It returns whether the first line went.
func (c *Config) deliver(ctx context.Context, s *State, firstSent bool) bool {
	if !firstSent {
		return c.Driver.Type(ctx, c.First) == nil
	}
	q := queued(c.Root)
	if len(q) == 0 {
		return true
	}
	m := q[0]
	if err := c.Driver.Type(ctx, Mark(m.Seq, c.Nonce, m.Text)); err == nil {
		s.Delivered = append(s.Delivered, Delivered{Seq: m.Seq, TypedAt: c.Now().UTC().Format(time.RFC3339Nano)})
		_ = os.Remove(filepath.Join(inboxDir(c.Root), strconv.FormatInt(m.Seq, 10)+".json"))
	}
	return true
}

// pending are the messages queued and not typed, and those typed with no
// reply after them.
func (c *Config) pending(s *State) []int64 {
	out := []int64{}
	for _, m := range queued(c.Root) {
		out = append(out, m.Seq)
	}
	lastReply := time.Time{}
	if s.Transcript != "" {
		if rs, err := replies(s.Transcript); err == nil && len(rs) > 0 {
			s.LastReplyAt = rs[len(rs)-1].At
			lastReply, _ = time.Parse(time.RFC3339Nano, rs[len(rs)-1].At)
		}
	}
	for _, d := range s.Delivered {
		typed, err := time.Parse(time.RFC3339Nano, d.TypedAt)
		if err == nil && !lastReply.After(typed) {
			out = append(out, d.Seq)
		}
	}
	return out
}

func lastLines(pane string, n int) string {
	lines := strings.Split(strings.TrimRight(pane, "\n "), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
