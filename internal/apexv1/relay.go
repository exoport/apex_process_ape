package apexv1

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// The loop relay (OI-11; D173, D174, D181; operator-interface design §7.1).
//
// Children start fresh (D61), but the loop session that launches them lives
// as long as its milestone, and nothing but Claude Code's auto-compact
// bounded its context. So ape relays it: at the loop's next child launch
// with its context fill at or over limits.context_relay (default 0.6), the
// launch is refused with session.relay (action stop) — the one point where
// a fresh session loses nothing, since the previous child has ended and the
// next step goes to a new session anyway. The loop replies and ends, and the
// `ape task <loop>` process starts a fresh loop session with the same args,
// its manifest recording relay_of the previous run, under the one lock the
// process already holds. The fresh session rebuilds everything from `ape
// status --json`, as any resumed loop does.
//
// Bounds: past limits.relays (default 5) the launch is refused with
// limits.relays (action person) and the run stops. A session that only waits
// on a long child is never relayed (waiting costs little context), and a
// loop typed into a person's own session is not ape-launched, so it has no
// record and is not relayed. Both keys are in the guarded limits: block, so
// the loop cannot edit its way out of a relay.
//
// Fill is the newest main turn's prompt (input + cache read + cache
// creation tokens) over the window of that turn's model (cost.ContextWindow,
// the raw spelling so `[1m]` selects 1M). An unknown window is an unknown
// fill: no relay, logged once in the run's output. On a 1M-window model the
// relay effectively never fires (atomix's loops peaked at 13 %, D181): it
// bounds a runaway session, not routine growth.

// rc.7 (rc7-relay): the relay's codes.
const (
	// CodeSessionRelay: the loop's child launch refused because its
	// context is at or over limits.context_relay; ape relays the loop.
	CodeSessionRelay = "session.relay"
	// CodeRelaysExceeded: the relay past limits.relays; the run stops.
	CodeRelaysExceeded = "limits.relays"
)

// The relay's defaults and the threshold's ceiling.
const (
	DefaultContextRelay = 0.6
	MaxContextRelay     = 0.95
	DefaultRelays       = 5
)

// ContextUse is a session's context as its transcript's newest main turn
// shows it. Window 0 is unknown, never a window of zero.
type ContextUse struct {
	Tokens int
	Window int
	// Model is the turn's model in the transcript's raw spelling.
	Model string
}

// Known reports whether the fill can be computed.
func (c ContextUse) Known() bool { return c.Window > 0 }

// Fill is Tokens over Window, 0 when the window is unknown.
func (c ContextUse) Fill() float64 {
	if c.Window <= 0 {
		return 0
	}
	return float64(c.Tokens) / float64(c.Window)
}

// TranscriptContext is the context of the session whose main transcript is
// path; ok is false when it holds no turn yet (or cannot be read).
func TranscriptContext(path string) (use ContextUse, ok bool) {
	tokens, model, ok := cost.LatestContext(path)
	if !ok {
		return ContextUse{}, false
	}
	window, _ := cost.ContextWindow(model)
	return ContextUse{Tokens: tokens, Window: window, Model: model}, true
}

// RelayRecord is one live loop run's relay record,
// <runs>/relays/<run>.json (runlog.RelaysRoot): written by the `ape task`
// process running the loop when the run starts and when its session's
// transcript is known, read and amended by the child launches it makes,
// removed when the run ends.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type RelayRecord struct {
	Run string `json:"run"`
	// RelayOf is the loop run this one relays (its manifest's relay_of),
	// Relays how many relays the loop run has had before this session.
	RelayOf    string    `json:"relay_of,omitempty"`
	Relays     int       `json:"relays"`
	Transcript string    `json:"transcript,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	// ContextUnknown is the model a child launch found no context window
	// for: the loop is not relayed, and ape says so once.
	ContextUnknown string `json:"context_unknown,omitempty"`
	// Requested is the relay a child launch asked for by refusing.
	Requested *RelayRequest `json:"requested,omitempty"`
}

// RelayRequest is the refusal a child launch recorded.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type RelayRequest struct {
	At        time.Time `json:"at"`
	Verb      string    `json:"verb"`
	Tokens    int       `json:"tokens"`
	Window    int       `json:"window"`
	Model     string    `json:"model"`
	Threshold float64   `json:"threshold"`
	// Exhausted: limits.relays was reached, so the run stops instead.
	Exhausted bool `json:"exhausted,omitempty"`
}

// Use is the context the request saw.
func (r *RelayRequest) Use() ContextUse {
	return ContextUse{Tokens: r.Tokens, Window: r.Window, Model: r.Model}
}

// RelayRecordPath is the record of the loop run run.
func RelayRecordPath(root, run string) string {
	return filepath.Join(runlog.RelaysRoot(root), safeRunName(run)+".json")
}

// ReadRelayRecord reads the loop run's record; nil, nil when it has none
// (not a relayable loop run, or one already ended).
func ReadRelayRecord(root, run string) (*RelayRecord, error) {
	if run == "" {
		return nil, nil
	}
	data, err := os.ReadFile(RelayRecordPath(root, run))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec RelayRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%s: %w", RelayRecordPath(root, run), err)
	}
	return &rec, nil
}

// WriteRelayRecord writes rec whole (temp file and rename: a reader never
// sees it half written).
func WriteRelayRecord(root string, rec *RelayRecord) error {
	path := RelayRecordPath(root, rec.Run)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'))
}

// RemoveRelayRecord removes the loop run's record.
func RemoveRelayRecord(root, run string) {
	if run != "" {
		_ = os.Remove(RelayRecordPath(root, run))
	}
}

func safeRunName(run string) string {
	return filepath.Base(filepath.Clean("/" + run))
}

// Percent is a fill as a whole percentage, rounded down: 0.599 is 59 %, so a
// message never shows a fill under its threshold as equal to it.
func Percent(f float64) int { return int(math.Floor(f*100 + 1e-9)) }

// RelayCheck is the relay's verdict on a child launch (verb, e.g. "ape
// task") of a loop session whose context is use, in a loop run that has
// relayed relays times already: nil below limits.context_relay (or with an
// unknown window); a UsageError session.relay (action stop) at or over it;
// a FailError limits.relays (action person) at or over it once relays has
// reached limits.relays.
func RelayCheck(verb string, use ContextUse, limits Limits, relays int) error {
	threshold, capRelays := limits.ContextRelayAt(), limits.RelaysCap()
	if !use.Known() || use.Fill() < threshold {
		return nil
	}
	at := fmt.Sprintf("this %s session's context is at %d %% (%d of %d tokens, %s), at or over limits.context_relay %d %%",
		OrchestratorSkill, Percent(use.Fill()), use.Tokens, use.Window, use.Model, Percent(threshold))
	if relays >= capRelays {
		return &FailError{Code: CodeRelaysExceeded, Action: ActionPerson, Msg: fmt.Sprintf(
			"%s refused: %s, and this run has relayed %d time(s) (limits.relays %d), so ape starts no fresh session and the run stops"+
				" — stop now and report where the milestone stands, starting nothing; a person looks at why the loop needs that much context, then resumes it with a new run or raises limits.relays (guarded: `ape project accept-config`)",
			verb, at, relays, capRelays)}
	}
	return &UsageError{Code: CodeSessionRelay, Action: ActionStop, Msg: fmt.Sprintf(
		"%s refused: %s — end this session now: reply with where the milestone stands and stop, starting nothing; ape starts a fresh %s session with the same args (relay %d of %d), which picks up from `ape status`",
		verb, at, OrchestratorSkill, relays+1, capRelays)}
}

// RelaysExhausted is the error a loop run stops with when its session ended
// on a limits.relays refusal: a person's.
func RelaysExhausted(req *RelayRequest, relays, capRelays int) error {
	use := req.Use()
	return &FailError{Code: CodeRelaysExceeded, Action: ActionPerson, Msg: fmt.Sprintf(
		"the %s run stopped: its session's context reached %d %% (%d of %d tokens) at or over limits.context_relay %d %% after %d relay(s), limits.relays %d"+
			" — a person looks at why the loop needs that much context, then resumes it or raises limits.relays (guarded: `ape project accept-config`)",
		OrchestratorSkill, Percent(use.Fill()), use.Tokens, use.Window, Percent(req.Threshold), relays, capRelays)}
}

// LoopRelay is a live loop run's context and relays, for `ape status`
// (LiveSession.Relay).
//
//nolint:tagliatelle // ape's snake_case --json contract
type LoopRelay struct {
	ContextTokens int `json:"context_tokens"`
	// ContextWindow is 0 and ContextFill null when the window is unknown
	// (then the loop is not relayed), or before the session's first turn.
	ContextWindow int      `json:"context_window"`
	ContextFill   *float64 `json:"context_fill"`
	ContextRelay  float64  `json:"context_relay"`
	Relays        int      `json:"relays"`
	RelaysCap     int      `json:"relays_cap"`
	RelayOf       string   `json:"relay_of,omitempty"`
}

// Describe is the relay as a status phrase: "context 12 % (relay at 60 %),
// relays 1/5".
func (r *LoopRelay) Describe() string {
	ctx := "context unknown"
	if r.ContextFill != nil {
		ctx = fmt.Sprintf("context %d %%", Percent(*r.ContextFill))
	}
	s := fmt.Sprintf("%s (relay at %d %%), relays %d/%d", ctx, Percent(r.ContextRelay), r.Relays, r.RelaysCap)
	if r.RelayOf != "" {
		s += ", relay of " + r.RelayOf
	}
	return s
}

// loopRelay is the loop run's relay status, nil when it has no record.
func (p *Project) loopRelay(run string) *LoopRelay {
	rec, err := ReadRelayRecord(p.Root, run)
	if err != nil || rec == nil {
		return nil
	}
	l := p.limits()
	r := &LoopRelay{ContextRelay: l.ContextRelayAt(), Relays: rec.Relays, RelaysCap: l.RelaysCap(), RelayOf: rec.RelayOf}
	if use, ok := TranscriptContext(rec.Transcript); ok {
		r.ContextTokens, r.ContextWindow = use.Tokens, use.Window
		if use.Known() {
			f := use.Fill()
			r.ContextFill = &f
		}
	}
	return r
}
