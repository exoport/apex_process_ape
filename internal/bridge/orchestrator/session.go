// Package orchestrator runs the parent-side wiring for `ape chat` and
// `ape pipeline` web mode: IPC listener, broker, claude subprocess,
// io.Pipe stdin bootstrap, and the lifecycle glue (Stop, browser-close,
// bridge crash). PLAN-5 / C1 + C3.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/broker"
	"github.com/diegosz/apex_process_ape/internal/bridge/config"
	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
)

// ExitCodeStop is the exit code returned when the user clicks Stop in
// the web UI (or when /api/stop is POSTed). Distinguishable from
// natural step failure (1+) and ctrl-C (130). PLAN-5 / C1.
const ExitCodeStop = 137

// Options configures a Session. The orchestrator does not own claude's
// argv; the caller composes Cmd and hands it over. The orchestrator
// sets Stdin/Stdout/Stderr, attaches the inline configs, and manages
// the subprocess lifecycle.
type Options struct {
	// APEBin is the absolute path to the ape binary used as the MCP
	// server command. Required.
	APEBin string
	// ClaudeBin is the absolute path to the `claude` binary. Empty
	// means "claude" (resolved by exec.LookPath). The orchestrator
	// builds argv from ClaudeArgs.
	ClaudeBin string
	// ClaudeExtraArgs are appended to the orchestrator's built-in
	// argv (after --strict-mcp-config, --mcp-config <inline>,
	// --settings <inline>, --system-prompt <prompt>).
	ClaudeExtraArgs []string
	// SystemPrompt is the --system-prompt value. Empty omits the flag.
	SystemPrompt string
	// BootstrapInput is the synthetic user-turn written to claude's
	// stdin via io.Pipe after the bridge signals ready. Empty skips
	// the bootstrap goroutine. PLAN-5 / C3 — quoted verbatim by callers.
	BootstrapInput string
	// Stdout / Stderr are where claude's output lands. Defaults to
	// the parent process's stdout / stderr (caller passes those in).
	Stdout io.Writer
	Stderr io.Writer
	// PageHTML is the body served at GET /. C1 leaves a tiny
	// placeholder when empty; C8 callers pass the full HTMX page.
	PageHTML string
	// MountExtras is forwarded to broker.Options.Mux. The caller
	// uses it to mount /assets/... (C8) and /dashboard (C7).
	MountExtras func(mux *http.ServeMux)
	// FragmentRenderer, when set, is consulted on every internally-
	// emitted SSE event (reply, await-pending, await-resolved,
	// stopped, error, hook, pipeline-init) to produce the HTML
	// payload. Empty / nil renderer falls back to the inline
	// placeholder strings the orchestrator used before C8.
	FragmentRenderer FragmentRenderer
	// OnReply is invoked when the bridge forwards a TypeReply frame
	// (skill called reply()). The orchestrator passes the content
	// to the broker as an SSE `reply` event; OnReply lets the
	// caller record it (checkpoints.jsonl per PLAN-5 / C6).
	OnReply func(content string)
	// OnCall is invoked for every TypeCall frame the bridge mirrors.
	// Caller uses this to write bridge-calls.jsonl (PLAN-5 / C6).
	OnCall func(call ToolCall)
	// OnHook is invoked for every TypeHook frame. Caller writes
	// hook-events.jsonl and routes per the in-memory
	// sessionID → step table (PLAN-5 / C4 + C6).
	OnHook func(hook HookEvent)
	// IgnoreProjectSettings translates to
	//   --setting-sources user --settings <inline>
	// so only user-global + ape hooks fire; project + local
	// .claude/settings*.json are skipped. PLAN-5 / C1.
	IgnoreProjectSettings bool
	// ConsumeBridgeReadyMs overrides the 30 s default deadline for
	// the io.Pipe bootstrap. Tests use this to make the timeout
	// observable; production uses the default.
	ConsumeBridgeReadyMs int
}

// ToolCall mirrors an MCP tools/call seen at the bridge stdio layer.
// PLAN-5 / C6 — bridge-calls.jsonl schema source.
type ToolCall struct {
	ID        string
	Tool      string
	Params    json.RawMessage
	Result    json.RawMessage
	SessionID string
	At        time.Time
}

// HookEvent is one `ape notify` forward. PLAN-5 / C6 — hook-events.jsonl source.
type HookEvent struct {
	Event     string
	SessionID string
	AgentID   string
	Step      string
	Payload   json.RawMessage
	At        time.Time
}

// Session owns one bridged claude invocation. Run blocks until claude
// exits or ctx is cancelled. ExitCode is set after Run returns.
type Session struct {
	opts Options

	broker     *broker.Broker
	brokerAddr string

	ipcLn   net.Listener
	ipcAddr string
	ipcPort int

	cmd *exec.Cmd

	bridgeMu      sync.Mutex
	bridgeConn    net.Conn
	bridgeReadyCh chan struct{}
	bridgeReadyOk bool

	stopOnce sync.Once
	stopped  bool

	ExitCode int
}

// New constructs an unstarted session. Call Listen, then Run.
func New(opts Options) *Session {
	return &Session{
		opts:          opts,
		bridgeReadyCh: make(chan struct{}),
	}
}

// Listen reserves the IPC + broker ports. Idempotent. Returns the web
// UI URL the caller should print on stdout.
func (s *Session) Listen() (webURL string, err error) {
	if s.ipcLn != nil {
		return "http://" + s.brokerAddr + "/", nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("orchestrator.Listen: ipc: %w", err)
	}
	s.ipcLn = ln
	s.ipcAddr = ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(s.ipcAddr)
	s.ipcPort, _ = strconv.Atoi(portStr)

	s.broker = broker.New(broker.Options{
		PageHTML:     s.opts.PageHTML,
		OnSend:       s.handleSend,
		OnStop:       s.requestStop,
		ReplayEvents: s.replayEvents,
		Mux:          s.opts.MountExtras,
	})
	addr, err := s.broker.Listen()
	if err != nil {
		s.ipcLn.Close()
		s.ipcLn = nil
		return "", fmt.Errorf("orchestrator.Listen: broker: %w", err)
	}
	s.brokerAddr = addr
	return "http://" + addr + "/", nil
}

// BrokerPort returns the TCP port the broker is bound to. Used by the
// caller to set APE_BRIDGE_PORT in hook env / inline settings.
func (s *Session) BrokerPort() int {
	if s.broker == nil {
		return 0
	}
	_, portStr, _ := net.SplitHostPort(s.brokerAddr)
	p, _ := strconv.Atoi(portStr)
	return p
}

// IPCPort returns the TCP port the IPC listener is bound to. Used by
// BuildMCPConfig.
func (s *Session) IPCPort() int { return s.ipcPort }

// Publish forwards an event to all SSE subscribers. Callers use this to
// emit pipeline-init / stage-start / etc.
func (s *Session) Publish(name, data string) {
	if s.broker == nil {
		return
	}
	s.broker.Publish(broker.Event{Name: name, Data: data})
}

// Run starts the broker, spawns claude, and blocks until exit.
func (s *Session) Run(ctx context.Context) error {
	if s.ipcLn == nil {
		if _, err := s.Listen(); err != nil {
			return err
		}
	}

	// Build inline configs.
	mcpCfg, err := config.BuildMCPConfig(config.MCPOptions{
		APEBin:  s.opts.APEBin,
		IPCPort: s.ipcPort,
	})
	if err != nil {
		return err
	}
	// BridgePort points hooks at the parent's IPC listener (same port
	// the bridge subprocess dials via APE_IPC_PORT). `ape notify` and
	// the bridge share the listener; the orchestrator demuxes by
	// first frame type. PLAN-5 / C4.
	settings, err := config.BuildSettings(config.SettingsOptions{
		APEBin:     s.opts.APEBin,
		BridgePort: s.ipcPort,
		Mode:       config.ModeWeb,
	})
	if err != nil {
		return err
	}

	// Run the broker.
	brokerCtx, brokerCancel := context.WithCancel(ctx)
	defer brokerCancel()
	brokerErrCh := make(chan error, 1)
	go func() { brokerErrCh <- s.broker.Serve(brokerCtx) }()

	// Accept IPC connections (bridge + many `ape notify` invocations).
	// Runs concurrent with claude startup.
	go s.acceptLoop()

	// Spawn claude.
	bin := s.opts.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	argv := []string{
		"--strict-mcp-config",
		"--mcp-config", string(mcpCfg),
		"--settings", string(settings),
	}
	if s.opts.IgnoreProjectSettings {
		argv = append(argv, "--setting-sources", "user")
	}
	if s.opts.SystemPrompt != "" {
		argv = append(argv, "--system-prompt", s.opts.SystemPrompt)
	}
	argv = append(argv, s.opts.ClaudeExtraArgs...)
	s.cmd = exec.CommandContext(ctx, bin, argv...)
	s.cmd.Stdout = s.opts.Stdout
	s.cmd.Stderr = s.opts.Stderr

	// Put claude in its own process group so we can SIGTERM the
	// whole group (claude + the bridge grandchild it spawns) on Stop.
	s.cmd.SysProcAttr = newSysProcAttr()

	// io.Pipe bootstrap — write the synthetic user turn after the
	// bridge signals ready over IPC, or after the deadline.
	pr, pw := io.Pipe()
	s.cmd.Stdin = pr
	bootDeadline := 30 * time.Second
	if s.opts.ConsumeBridgeReadyMs > 0 {
		bootDeadline = time.Duration(s.opts.ConsumeBridgeReadyMs) * time.Millisecond
	}
	go func() {
		defer pw.Close()
		if s.opts.BootstrapInput == "" {
			return
		}
		select {
		case <-s.bridgeReadyCh:
		case <-time.After(bootDeadline):
			fmt.Fprintf(stderr(s.opts.Stderr), "[orchestrator] bridge not ready after %s, sending bootstrap anyway\n", bootDeadline)
		}
		_, _ = io.WriteString(pw, s.opts.BootstrapInput)
		if !endsWithNewline(s.opts.BootstrapInput) {
			_, _ = io.WriteString(pw, "\n")
		}
	}()

	runErr := s.cmd.Run()
	brokerCancel()

	if s.stopped {
		s.ExitCode = ExitCodeStop
		return nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			s.ExitCode = exitErr.ExitCode()
			return nil
		}
		return runErr
	}
	s.ExitCode = 0
	return nil
}

// requestStop is invoked from POST /api/stop. SIGTERMs claude's process
// group and marks the session stopped so Run reports ExitCodeStop.
func (s *Session) requestStop() {
	s.stopOnce.Do(func() {
		s.stopped = true
		if s.cmd != nil && s.cmd.Process != nil {
			terminateGroup(s.cmd)
		}
		s.Publish("stopped", s.fragRenderer().Stopped())
	})
}

// handleSend is invoked from POST /api/send. Forwards to the bridge
// as a TypeMessage IPC frame. PLAN-5 / C3.
func (s *Session) handleSend(content string) {
	s.bridgeMu.Lock()
	conn := s.bridgeConn
	s.bridgeMu.Unlock()
	if conn == nil {
		return
	}
	_ = ipc.Write(conn, ipc.Message{Type: ipc.TypeMessage, Content: content})
}

// SendStepBind tells the bridge which step a session id belongs to so
// hook events and mirrored calls can be tagged correctly. PLAN-5 / C4.
func (s *Session) SendStepBind(sessionID, step string) {
	s.bridgeMu.Lock()
	conn := s.bridgeConn
	s.bridgeMu.Unlock()
	if conn == nil {
		return
	}
	_ = ipc.Write(conn, ipc.Message{Type: ipc.TypeStepBind, SessionID: sessionID, Step: step})
}

// DeliverHook is invoked by the hook receiver (TCP listener in `ape notify`'s
// target). Wired by the caller via the same listener that the bridge
// dials — `ape notify` and the bridge share the IPC listener. The
// listener routes hook frames to OnHook (caller's run-artefact writer)
// and forwards display-worthy hooks to the broker as SSE events.
func (s *Session) DeliverHook(hook HookEvent) {
	if s.opts.OnHook != nil {
		s.opts.OnHook(hook)
	}
	s.Publish("hook", s.fragRenderer().HookFromEvent(hook))
}

func (s *Session) replayEvents() []broker.Event {
	// PLAN-5 / C3 — no backlog replay. Lead with a `connected` status
	// flip so the page banner becomes "connected" deterministically,
	// then a fresh pipeline-init to reset the lists. C8 will fold in
	// stage state here when we track it.
	return []broker.Event{
		{Name: "connected", Data: s.fragRenderer().Connected()},
		{Name: "pipeline-init", Data: s.fragRenderer().PipelineInit()},
	}
}

func (s *Session) fragRenderer() FragmentRenderer {
	if s.opts.FragmentRenderer != nil {
		return s.opts.FragmentRenderer
	}
	return defaultRenderer{}
}

// defaultRenderer falls back to the inline placeholders the
// orchestrator used before C8. Tests use these; production runs the
// C8 web template renderer.
type defaultRenderer struct{}

func (defaultRenderer) PipelineInit() string          { return `<div id="stages"></div>` }
func (defaultRenderer) Connected() string             { return `<div id="status" class="connected">connected</div>` }
func (defaultRenderer) Reply(content string) string   { return `<div class="reply">` + htmlEscape(content) + `</div>` }
func (defaultRenderer) AwaitPending() string          { return `<form id="decision-gate" enabled></form>` }
func (defaultRenderer) AwaitResolved() string         { return `<form id="decision-gate" disabled></form>` }
func (defaultRenderer) Stopped() string               { return `<div id="status">Stopped by user</div>` }
func (defaultRenderer) BridgeError(msg string) string { return `<div id="status">Bridge error: ` + htmlEscape(msg) + `</div>` }
func (defaultRenderer) HookFromEvent(h HookEvent) string {
	return `<li>` + htmlEscape(h.Event) + ` ` + htmlEscape(h.SessionID) + ` ` + htmlEscape(h.Step) + `</li>`
}

// acceptLoop accepts unlimited connections on the IPC port. The bridge
// subprocess sends TypeReady as its first frame; we promote that
// connection to s.bridgeConn so handleSend can target it. Every other
// connection (one per `ape notify` invocation) is read-only — we never
// write back, the sender NDJSON-encodes one TypeHook frame and closes.
func (s *Session) acceptLoop() {
	for {
		conn, err := s.ipcLn.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Session) handleConn(conn net.Conn) {
	defer conn.Close()
	isBridge := false
	_ = ipc.Read(conn, func(m ipc.Message) {
		if m.Type == ipc.TypeReady && !isBridge {
			isBridge = true
			s.bridgeMu.Lock()
			s.bridgeConn = conn
			s.bridgeMu.Unlock()
		}
		s.onIPCFrame(m)
	})
	if isBridge {
		s.bridgeMu.Lock()
		if s.bridgeConn == conn {
			s.bridgeConn = nil
		}
		s.bridgeMu.Unlock()
	}
}

func (s *Session) onIPCFrame(m ipc.Message) {
	switch m.Type {
	case ipc.TypeReady:
		if !s.bridgeReadyOk {
			s.bridgeReadyOk = true
			close(s.bridgeReadyCh)
		}
	case ipc.TypeReply:
		s.Publish("reply", s.fragRenderer().Reply(m.Content))
		if s.opts.OnReply != nil {
			s.opts.OnReply(m.Content)
		}
	case ipc.TypeCall:
		if s.opts.OnCall != nil {
			s.opts.OnCall(ToolCall{
				ID:        m.ID,
				Tool:      m.Tool,
				Params:    m.Params,
				Result:    m.Result,
				SessionID: m.SessionID,
				At:        time.Now().UTC(),
			})
		}
		// Surface await pending / resolved as SSE so the
		// decision-gate form toggles in the UI. PLAN-5 / C3.
		if m.Tool == "await_message" {
			if isDeferredEntry(m.Params) {
				s.Publish("await-pending", s.fragRenderer().AwaitPending())
			} else if isFlushEntry(m.Params) {
				s.Publish("await-resolved", s.fragRenderer().AwaitResolved())
			}
		}
	case ipc.TypeHook:
		s.DeliverHook(HookEvent{
			Event:     m.Event,
			SessionID: m.SessionID,
			AgentID:   m.AgentID,
			Step:      m.Step,
			Payload:   m.Payload,
			At:        time.Now().UTC(),
		})
	case ipc.TypeBufferOvf:
		// Surface to the call sink so bridge-calls.jsonl records
		// the overflow event. PLAN-5 / C3.
		if s.opts.OnCall != nil {
			s.opts.OnCall(ToolCall{
				Tool:   "buffer-overflow",
				Params: json.RawMessage([]byte(strconv.Quote(m.Content))),
				At:     time.Now().UTC(),
			})
		}
	}
}

func isDeferredEntry(raw json.RawMessage) bool {
	var v struct {
		Deferred bool `json:"deferred"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Deferred
}

func isFlushEntry(raw json.RawMessage) bool {
	var v struct {
		Flush bool `json:"flush"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Flush
}

func endsWithNewline(s string) bool {
	return len(s) > 0 && s[len(s)-1] == '\n'
}

func htmlEscape(s string) string {
	// Minimal escape for SSE payloads. C8 will replace this with
	// template-driven escaping.
	var out []byte
	for _, r := range s {
		switch r {
		case '<':
			out = append(out, "&lt;"...)
		case '>':
			out = append(out, "&gt;"...)
		case '&':
			out = append(out, "&amp;"...)
		case '"':
			out = append(out, "&quot;"...)
		default:
			out = append(out, string(r)...)
		}
	}
	return string(out)
}

func stderr(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
