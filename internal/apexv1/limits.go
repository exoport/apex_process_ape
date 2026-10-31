package apexv1

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/usage"
	"gopkg.in/yaml.v3"
)

// DefaultAttempts is limits.attempts when the config sets none. The loop
// limits (backlog 30) are ceilings ape enforces itself, so a loop that does
// not converge ends with a recorded reason instead of running on:
//
//	limits:
//	  budget: enforce          # optional; default off
//	  attempts: 5              # red verifies per change start; default 5
//	  attempts_total: 15       # red verifies per change, re-starts included; default 15
//	  replans: 3               # re-plans (re-starts with a changed plan) per change; default 3
//	  consecutive_blocks: 2    # blocked changes in a row per milestone; default 2
//	  context_relay: 0.6       # relay the loop at its next child launch at this context fill; default 0.6
//	  relays: 5                # relays per loop run; default 5
//	  usage:                   # the usage guard before every ape task launch (W4); defaults below
//	    seven_day: 0.85        # stop (exit 14, a person's call) over this 7-day window fill
//	    five_hour: 0.95        # hold until the 5-hour window resets, over this fill with the launch's share
//	    ceiling: 0.95          # stop when the 7-day fill with the launch's share is over it
//	    share: auto            # the launch's share: the skill's median, else 0.05; or a number
//	    max_age: 10m           # a reading older than this is unknown: launch, with a note
//	    off: false             # true: no guard
//	  session:                 # caps on each ape task session (v1.4.2 D1 L3); both off by default
//	    tokens: 20M            # tokens_total (all four kinds): stop at it (termination max_tokens)
//	    wall: 3h               # wall time since the session started (termination max_duration)
const DefaultAttempts = 5

// The defaults of the limits re-planning cannot reset (H19, D47 Q9).
const (
	DefaultAttemptsTotal     = 15
	DefaultReplans           = 3
	DefaultConsecutiveBlocks = 2
)

// StopBudget is the limit-stop reason milestones.json and the run
// manifest's termination kind record for a session stopped at the
// milestone's budget.
const StopBudget = "budget"

// The session caps' stop reasons (v1.4.2 D1 L3): the run manifest's
// termination kinds max_tokens and max_duration.
const (
	StopMaxTokens   = "max_tokens"
	StopMaxDuration = "max_duration"
)

// The units a budget stop names (v1.4.2 D1 L2).
const (
	UnitUSD    = "usd"
	UnitTokens = "tokens"
	UnitWall   = "wall"
)

// Limits is _apex/config.yaml's limits: block. Unset values take their
// defaults; a value ape cannot use fails the config's parse, like any
// unparseable config (Open refuses it rather than guess).
type Limits struct {
	// Budget is "enforce" or "off" (the default, also when absent).
	Budget string `yaml:"budget"`
	// Attempts caps red verifies per change start: the red verify that
	// reaches it blocks the change.
	Attempts *int `yaml:"attempts"`
	// AttemptsTotal caps a change's red verifies across its re-starts —
	// a re-plan resets Attempts' count, not this one (H19) — since a
	// person last answered it.
	AttemptsTotal *int `yaml:"attempts_total"`
	// Replans caps a change's re-plans since a person last answered it:
	// the re-start past it blocks the change.
	Replans *int `yaml:"replans"`
	// ConsecutiveBlocks caps a milestone's changes blocked in a row
	// (milestones.json consecutive_blocks): past it, no change of the
	// milestone is planned or started and no orchestrator runs it.
	ConsecutiveBlocks *int `yaml:"consecutive_blocks"`

	// rc.7 (rc7-relay).
	// ContextRelay is the loop session's context fill (0 < x ≤ 0.95) at
	// which its next child launch is refused and ape relays the loop to a
	// fresh session (OI-11, D174, D181; relay.go).
	ContextRelay *float64 `yaml:"context_relay"`
	// Relays caps the relays of one loop run: the relay past it stops the
	// run (limits.relays, a person's).
	Relays *int `yaml:"relays"`

	// v1.1 W4.
	// Usage is the usage guard ape applies before every ape task launch
	// (internal/usage): unset keys take their defaults.
	Usage *UsageLimits `yaml:"usage"`

	// v1.4.2 D1 L3.
	// Session caps each ape task session in tokens and wall time; unset,
	// no cap. `ape task --max-tokens` overrides tokens for one session.
	Session *SessionLimits `yaml:"session"`
}

// SessionLimits is limits.session (v1.4.2 D1 L3): tokens a token count
// (ParseTokens: 20M, 500k), wall a Go duration (3h, 90m).
type SessionLimits struct {
	Tokens *string `yaml:"tokens"`
	Wall   *string `yaml:"wall"`
}

func (s *SessionLimits) validate() error {
	if s == nil {
		return nil
	}
	if s.Tokens != nil {
		if _, err := ParseTokens(*s.Tokens); err != nil {
			return fmt.Errorf("limits.session.tokens: %w", err)
		}
	}
	if s.Wall != nil {
		if d, err := time.ParseDuration(strings.TrimSpace(*s.Wall)); err != nil || d <= 0 {
			return fmt.Errorf("limits.session.wall: %q is not a duration above 0 (3h, 90m)", *s.Wall)
		}
	}
	return nil
}

// SessionCaps is limits.session read: 0 is no cap.
func (l Limits) SessionCaps() SessionCaps {
	var c SessionCaps
	if s := l.Session; s != nil {
		if s.Tokens != nil {
			c.Tokens, _ = ParseTokens(*s.Tokens)
		}
		if s.Wall != nil {
			c.Wall, _ = time.ParseDuration(strings.TrimSpace(*s.Wall))
		}
	}
	return c
}

// SessionCaps are one session's caps (limits.session, --max-tokens): 0 is
// no cap.
type SessionCaps struct {
	Tokens int64
	Wall   time.Duration
}

// UsageLimits is limits.usage (W4). The fractions are 0 < x ≤ 1 (0.85 is
// 85 %); max_age is a Go duration.
type UsageLimits struct {
	SevenDay *float64 `yaml:"seven_day"`
	FiveHour *float64 `yaml:"five_hour"`
	Ceiling  *float64 `yaml:"ceiling"`
	// Share is "auto" (the default) or a fraction.
	Share  *UsageShare `yaml:"share"`
	MaxAge *string     `yaml:"max_age"`
	Off    bool        `yaml:"off"`
}

// UsageShare is limits.usage.share: auto, or a fraction.
type UsageShare struct {
	Auto  bool
	Value float64
}

// UnmarshalYAML reads auto or a number.
func (s *UsageShare) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && strings.TrimSpace(n.Value) == "auto" {
		*s = UsageShare{Auto: true}
		return nil
	}
	var v float64
	if err := n.Decode(&v); err != nil {
		return fmt.Errorf("limits.usage.share: %q is not auto or a fraction", n.Value)
	}
	*s = UsageShare{Value: v}
	return nil
}

// MarshalYAML writes it back as it reads.
func (s UsageShare) MarshalYAML() (any, error) {
	if s.Auto {
		return "auto", nil
	}
	return s.Value, nil
}

// MarshalJSON writes "auto" or the number.
func (s UsageShare) MarshalJSON() ([]byte, error) {
	if s.Auto {
		return []byte(`"auto"`), nil
	}
	return json.Marshal(s.Value)
}

func (u *UsageLimits) validate() error {
	if u == nil {
		return nil
	}
	for name, v := range map[string]*float64{"seven_day": u.SevenDay, "five_hour": u.FiveHour, "ceiling": u.Ceiling} {
		if v != nil && (*v <= 0 || *v > 1) {
			return fmt.Errorf("limits.usage.%s: %g is not a fraction above 0 and at most 1 (0.85 is 85%%)", name, *v)
		}
	}
	if s := u.Share; s != nil && !s.Auto && (s.Value <= 0 || s.Value > 1) {
		return fmt.Errorf("limits.usage.share: %g is not auto or a fraction above 0 and at most 1", s.Value)
	}
	if u.MaxAge != nil {
		d, err := time.ParseDuration(strings.TrimSpace(*u.MaxAge))
		if err != nil || d <= 0 {
			return fmt.Errorf("limits.usage.max_age: %q is not a duration above 0 (10m, 1h)", *u.MaxAge)
		}
	}
	return nil
}

// UsageGuard is limits.usage with its defaults filled in.
func (l Limits) UsageGuard() usage.GuardConfig {
	g := usage.DefaultGuard()
	u := l.Usage
	if u == nil {
		return g
	}
	if u.SevenDay != nil {
		g.SevenDay = *u.SevenDay
	}
	if u.FiveHour != nil {
		g.FiveHour = *u.FiveHour
	}
	if u.Ceiling != nil {
		g.Ceiling = *u.Ceiling
	}
	if u.Share != nil && !u.Share.Auto {
		v := u.Share.Value
		g.Share = &v
	}
	if u.MaxAge != nil {
		if d, err := time.ParseDuration(strings.TrimSpace(*u.MaxAge)); err == nil && d > 0 {
			g.MaxAge = d
		}
	}
	g.Off = u.Off
	return g
}

// UnmarshalYAML validates the block as it parses.
func (l *Limits) UnmarshalYAML(n *yaml.Node) error {
	type plain Limits
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	switch strings.TrimSpace(p.Budget) {
	case "", "off", "enforce":
		p.Budget = strings.TrimSpace(p.Budget)
	default:
		return fmt.Errorf("limits.budget: %q is not enforce or off", p.Budget)
	}
	for name, v := range map[string]*int{"attempts": p.Attempts, "attempts_total": p.AttemptsTotal, "consecutive_blocks": p.ConsecutiveBlocks} {
		if v != nil && *v < 1 {
			return fmt.Errorf("limits.%s: %d is not a positive whole number", name, *v)
		}
	}
	if p.Replans != nil && *p.Replans < 0 {
		return fmt.Errorf("limits.replans: %d is not a whole number", *p.Replans)
	}
	if p.Relays != nil && *p.Relays < 0 {
		return fmt.Errorf("limits.relays: %d is not a whole number", *p.Relays)
	}
	if v := p.ContextRelay; v != nil && (*v <= 0 || *v > MaxContextRelay) {
		return fmt.Errorf("limits.context_relay: %g is not a fraction above 0 and at most %g (0.6 is 60%%)", *v, MaxContextRelay)
	}
	if err := p.Usage.validate(); err != nil {
		return err
	}
	if err := p.Session.validate(); err != nil {
		return err
	}
	*l = Limits(p)
	return nil
}

// BudgetEnforced reports limits.budget: enforce.
func (l Limits) BudgetEnforced() bool { return l.Budget == "enforce" }

// AttemptsCap is limits.attempts, or DefaultAttempts.
func (l Limits) AttemptsCap() int {
	if l.Attempts != nil {
		return *l.Attempts
	}
	return DefaultAttempts
}

// AttemptsTotalCap is limits.attempts_total, or DefaultAttemptsTotal.
func (l Limits) AttemptsTotalCap() int { return intOr(l.AttemptsTotal, DefaultAttemptsTotal) }

// ReplansCap is limits.replans, or DefaultReplans.
func (l Limits) ReplansCap() int { return intOr(l.Replans, DefaultReplans) }

// ConsecutiveBlocksCap is limits.consecutive_blocks, or
// DefaultConsecutiveBlocks.
func (l Limits) ConsecutiveBlocksCap() int {
	return intOr(l.ConsecutiveBlocks, DefaultConsecutiveBlocks)
}

// ContextRelayAt is limits.context_relay, or DefaultContextRelay.
func (l Limits) ContextRelayAt() float64 {
	if l.ContextRelay != nil {
		return *l.ContextRelay
	}
	return DefaultContextRelay
}

// RelaysCap is limits.relays, or DefaultRelays.
func (l Limits) RelaysCap() int { return intOr(l.Relays, DefaultRelays) }

func intOr(v *int, def int) int {
	if v != nil {
		return *v
	}
	return def
}

// limits is the project's limits, defaults when the config cannot be read
// (Open has already refused an unparseable one).
func (p *Project) limits() Limits {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return Limits{}
	}
	return cfg.Limits
}

// LimitStop is one session ape stopped at a milestone limit, as
// milestones.json records it under the milestone's "stops".
type LimitStop struct {
	Reason string `json:"reason"`
	At     string `json:"at"`
	// Run is the stopped session's run id (its manifest under
	// _apex/state/runs/tasks/<skill>/<run>/), Skill its skill.
	Run   string `json:"run"`
	Skill string `json:"skill"`
	// The milestone's spend when it was stopped (recorded runs plus this
	// session), its budget, and this session's own cost — which its
	// manifest does not hold, since a manifest counts only completed steps.
	SpentUSD   float64 `json:"spent_usd"`
	BudgetUSD  float64 `json:"budget_usd"`
	SessionUSD float64 `json:"session_usd"`
	// Unit is the budget it reached (v1.4.2 D1 L2): usd (also when absent,
	// a stop recorded before 1.4.2), tokens or wall. The session's tokens
	// and wall time are recorded whatever the unit, so a milestone's use
	// counts the stopped session at what ape measured.
	Unit            string  `json:"unit,omitempty"`
	SpentTokens     int64   `json:"spent_tokens,omitempty"`
	BudgetTokens    int64   `json:"budget_tokens,omitempty"`
	SessionTokens   int64   `json:"session_tokens,omitempty"`
	SpentWallSecs   float64 `json:"spent_wall_secs,omitempty"`
	BudgetWallSecs  float64 `json:"budget_wall_secs,omitempty"`
	SessionWallSecs float64 `json:"session_wall_secs,omitempty"`
}

// Describe is the stop in one line.
func (s *LimitStop) Describe() string {
	if s.Reason != StopBudget {
		return "stopped (" + s.Reason + ") " + s.At + " run " + s.Run
	}
	switch s.Unit {
	case UnitTokens:
		return fmt.Sprintf("stopped at token budget %s: %s of %s tokens (%s run %s)", s.At, FormatTokens(s.SpentTokens), FormatTokens(s.BudgetTokens), s.Skill, s.Run)
	case UnitWall:
		return fmt.Sprintf("stopped at wall budget %s: %s of %s (%s run %s)", s.At, formatSecs(s.SpentWallSecs), formatSecs(s.BudgetWallSecs), s.Skill, s.Run)
	}
	return fmt.Sprintf("stopped at budget %s: $%.2f of $%.2f (%s run %s)", s.At, s.SpentUSD, s.BudgetUSD, s.Skill, s.Run)
}

// FormatTokens is a token count for a person: 61.2M, 512k, 900.
func FormatTokens(n int64) string {
	switch {
	case n >= tokensG:
		return strconv.FormatFloat(float64(n)/tokensG, 'f', 1, 64) + "G"
	case n >= tokensM:
		return strconv.FormatFloat(float64(n)/tokensM, 'f', 1, 64) + "M"
	case n >= tokensK:
		return strconv.FormatFloat(float64(n)/tokensK, 'f', 0, 64) + "k"
	}
	return strconv.FormatInt(n, 10)
}

// formatSecs is a duration in seconds for a person: 4h0m0s → 4h, 1h30m.
func formatSecs(secs float64) string {
	d := time.Duration(secs * float64(time.Second)).Round(time.Second)
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// LimitStopsJSON is _apex/state/limit-stops.json: milestone id → the
// sessions ape stopped at a limit. Ignored by git, like attempts.json: a
// stop is a run event, not a status change, and the stopped session may be
// on a change branch (D38), where a commit to a milestone record would
// break D11 (milestone commits stay on the base branch) and conflict at
// the change's rebase. The run manifest (termination "budget") is the
// run's own record of it.
func (p *Project) LimitStopsJSON() string { return filepath.Join(p.StateDir(), "limit-stops.json") }

func (p *Project) loadLimitStops() map[string][]LimitStop {
	out := map[string][]LimitStop{}
	data, err := os.ReadFile(p.LimitStopsJSON())
	if err != nil {
		return out
	}
	if json.Unmarshal(data, &out) != nil || out == nil {
		return map[string][]LimitStop{}
	}
	return out
}

// RecordLimitStop appends a stop to the milestone's list in
// limit-stops.json. It commits nothing.
func (p *Project) RecordLimitStop(milestone string, stop LimitStop) error {
	all := p.loadLimitStops()
	all[milestone] = append(all[milestone], stop)
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	p.ensureState()
	return writeFileAtomic(p.LimitStopsJSON(), string(data)+"\n")
}

// milestoneStops is the recorded limit stops of a milestone: those an
// earlier build committed into milestones.json, then this checkout's.
func (p *Project) milestoneStops(id string) []LimitStop {
	var out []LimitStop
	if recs, err := p.LoadMilestoneRecords(); err == nil && recs[id] != nil {
		out = append(out, recs[id].Stops...)
	}
	return append(out, p.loadLimitStops()[id]...)
}

// approvedSince is when m was approved; zero when it was not.
func approvedSince(m *Milestone) time.Time {
	t, err := time.Parse(time.RFC3339, m.ApprovedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// MilestoneUse is what a milestone's runs used since its approval: USD
// (from the price table), tokens (tokens_total: all four kinds) and wall
// time (the summed duration of its sessions, not the calendar time since
// the approval: Q-L2).
type MilestoneUse struct {
	USD      float64
	Tokens   int64
	WallSecs float64
	// CacheRead is the cache-read share of Tokens, from the manifests.
	CacheRead int64
}

// NonCache is the tokens that are not cache reads.
func (u MilestoneUse) NonCache() int64 { return max(u.Tokens-u.CacheRead, 0) }

// milestoneUse is the use of the runs started since m's approval (every
// run when it is not approved): each run's manifest figures, or what ape
// measured when a limit stopped it, whichever is more — a stopped
// session's manifest records the stopped step's transcript scan (D159),
// and the limit's own measure may be the later of the two. A tweak's runs
// — whose --args names a change of `milestone: none` (D40) — count toward
// no milestone.
func milestoneUse(m *Milestone, runs []cost.RunCost, stops []LimitStop, tweaks map[string]bool) MilestoneUse {
	since := approvedSince(m)
	stopped := map[string]MilestoneUse{}
	for i := range stops {
		s, was := &stops[i], stopped[stops[i].Run]
		stopped[s.Run] = MilestoneUse{USD: max(was.USD, s.SessionUSD), Tokens: max(was.Tokens, s.SessionTokens), WallSecs: max(was.WallSecs, s.SessionWallSecs)}
	}
	var total MilestoneUse
	for i := range runs {
		run := &runs[i]
		if run.StartedAt.Before(since) || tweaks[RunChangeArg(run.Args)] {
			continue
		}
		u := MilestoneUse{USD: run.CostUSD, Tokens: int64(run.TokensTotal), WallSecs: run.DurationSecs}
		if s, ok := stopped[run.RunID]; ok {
			u = MilestoneUse{USD: max(u.USD, s.USD), Tokens: max(u.Tokens, s.Tokens), WallSecs: max(u.WallSecs, s.WallSecs)}
		}
		total.USD += u.USD
		total.Tokens += u.Tokens
		total.WallSecs += u.WallSecs
		total.CacheRead += int64(run.TokensCacheRead)
	}
	return total
}

// TaskLimits is what `ape task` enforces on one session of a v1 project:
// the current milestone's budgets — USD (Budget:), tokens (Budget-tokens:)
// and wall time (Budget-wall:) — only with limits.budget: enforce and the
// line, and the session's own caps (limits.session, --max-tokens).
type TaskLimits struct {
	Milestone string
	Skill     string
	// Budget is the milestone's USD budget (0: none); SpentUSD the
	// milestone's recorded spend when the session started.
	Budget   float64
	SpentUSD float64
	// BudgetTokens / BudgetWallSecs are its token and wall budgets (0:
	// none); SpentTokens / SpentWallSecs what its runs used at the start.
	BudgetTokens   int64
	SpentTokens    int64
	BudgetWallSecs float64
	SpentWallSecs  float64
	// Session are the session's own caps.
	Session SessionCaps
}

// SessionUse is what the running session used so far: its cost, its
// tokens (tokens_total) and its wall time since it started.
type SessionUse struct {
	USD    float64
	Tokens int64
	Wall   time.Duration
}

// LimitHit is a limit a running session reached.
type LimitHit struct {
	Reason  string
	Message string
	Stop    LimitStop
}

// TaskLimits decides the limits of a session of skill, or refuses to start
// it: FailError milestone.budget-exceeded when the milestone's use is
// already at an enforced budget. The milestone is the current one as
// `ape status` derives it, and only when it is approved; with none, or
// with no budget enforced, nothing applies and it returns nil.
func (p *Project) TaskLimits(skill string) (*TaskLimits, error) {
	return p.TaskLimitsFor(context.Background(), skill, "")
}

// TaskLimitsFor is TaskLimits for a session started with --args args: a
// tweak's session (args names a change of `milestone: none`, D40) counts
// toward no milestone, so no milestone budget applies to it. The session
// caps are limits.session's.
func (p *Project) TaskLimitsFor(ctx context.Context, skill, args string) (*TaskLimits, error) {
	return p.TaskLimitsWith(ctx, skill, args, p.limits().SessionCaps())
}

// TaskLimitsWith is TaskLimitsFor with the session's caps given
// (limits.session with `ape task --max-tokens` over it). nil when nothing
// applies.
func (p *Project) TaskLimitsWith(ctx context.Context, skill, args string, caps SessionCaps) (*TaskLimits, error) {
	tl := &TaskLimits{Skill: skill, Session: caps}
	var m *Milestone
	if rm := p.roadmap(); rm != nil && !p.IsTweakRun(ctx, args) {
		m = rm.Current()
	}
	budgeted := m != nil && m.Status == MilestoneApproved && p.limits().BudgetEnforced() &&
		(m.Budget != nil || m.BudgetTokens != nil || m.BudgetWallSecs != nil)
	if !budgeted {
		if caps == (SessionCaps{}) {
			return nil, nil //nolint:nilnil // no limit applies: the documented nil
		}
		return tl, nil
	}
	runs, _ := cost.RunCosts(p.Root)
	use := milestoneUse(m, runs, p.milestoneStops(m.ID), p.tweakIDs(ctx))
	tl.Milestone, tl.SpentUSD, tl.SpentTokens, tl.SpentWallSecs = m.ID, use.USD, use.Tokens, use.WallSecs
	if m.Budget != nil {
		tl.Budget = *m.Budget
	}
	if m.BudgetTokens != nil {
		tl.BudgetTokens = *m.BudgetTokens
	}
	if m.BudgetWallSecs != nil {
		tl.BudgetWallSecs = *m.BudgetWallSecs
	}
	refuse := func(what, line string) error {
		return &FailError{Code: "milestone.budget-exceeded", Msg: m.ID + " has " + what +
			" — a person raises " + line + " in roadmap.md or re-scopes the milestone; no session starts until then"}
	}
	switch {
	case tl.Budget > 0 && tl.SpentUSD >= tl.Budget:
		return nil, refuse(fmt.Sprintf("spent $%.2f of its $%.2f budget", tl.SpentUSD, tl.Budget), "Budget:")
	case tl.BudgetTokens > 0 && tl.SpentTokens >= tl.BudgetTokens:
		return nil, refuse(fmt.Sprintf("used %s of its %s-token budget", FormatTokens(tl.SpentTokens), FormatTokens(tl.BudgetTokens)), "Budget-tokens:")
	case tl.BudgetWallSecs > 0 && tl.SpentWallSecs >= tl.BudgetWallSecs:
		return nil, refuse(fmt.Sprintf("run %s of its %s wall budget (summed session time)", formatSecs(tl.SpentWallSecs), formatSecs(tl.BudgetWallSecs)), "Budget-wall:")
	}
	return tl, nil
}

// Check judges the running session by what it used so far: nil while the
// session stays under its own caps and the milestone under its budgets.
// The session's caps are judged first: they are the narrower.
func (l *TaskLimits) Check(u SessionUse) *LimitHit {
	if l == nil {
		return nil
	}
	stop := LimitStop{
		At: Stamp(), Skill: l.Skill,
		SessionUSD: round2(u.USD), SessionTokens: u.Tokens, SessionWallSecs: math.Round(u.Wall.Seconds()),
	}
	switch {
	case l.Session.Tokens > 0 && u.Tokens >= l.Session.Tokens:
		stop.Reason = StopMaxTokens
		return &LimitHit{Reason: StopMaxTokens, Stop: stop, Message: fmt.Sprintf(
			"session token cap reached: %s of %s tokens (limits.session.tokens / --max-tokens) — a person's call: raise the cap, or resume the work in a new session",
			FormatTokens(u.Tokens), FormatTokens(l.Session.Tokens))}
	case l.Session.Wall > 0 && u.Wall >= l.Session.Wall:
		stop.Reason = StopMaxDuration
		return &LimitHit{Reason: StopMaxDuration, Stop: stop, Message: fmt.Sprintf(
			"session wall cap reached: %s of %s (limits.session.wall) — a person's call: raise the cap, or resume the work in a new session",
			formatSecs(u.Wall.Seconds()), formatSecs(l.Session.Wall.Seconds()))}
	}
	if l.Milestone == "" {
		return nil
	}
	stop.Reason = StopBudget
	stop.SpentUSD, stop.BudgetUSD = round2(l.SpentUSD+u.USD), l.Budget
	stop.SpentTokens, stop.BudgetTokens = l.SpentTokens+u.Tokens, l.BudgetTokens
	stop.SpentWallSecs, stop.BudgetWallSecs = math.Round(l.SpentWallSecs+u.Wall.Seconds()), l.BudgetWallSecs
	raise := func(line string) string {
		return " — a person raises " + line + " in roadmap.md or re-scopes the milestone"
	}
	switch {
	case l.Budget > 0 && l.SpentUSD+u.USD >= l.Budget:
		stop.Unit = UnitUSD
		return &LimitHit{Reason: StopBudget, Stop: stop, Message: fmt.Sprintf("%s budget reached: $%.2f of $%.2f ($%.2f recorded + $%.2f this session)",
			l.Milestone, l.SpentUSD+u.USD, l.Budget, l.SpentUSD, u.USD) + raise("Budget:")}
	case l.BudgetTokens > 0 && stop.SpentTokens >= l.BudgetTokens:
		stop.Unit = UnitTokens
		return &LimitHit{Reason: StopBudget, Stop: stop, Message: fmt.Sprintf("%s token budget reached: %s of %s tokens (%s recorded + %s this session)",
			l.Milestone, FormatTokens(stop.SpentTokens), FormatTokens(l.BudgetTokens), FormatTokens(l.SpentTokens), FormatTokens(u.Tokens)) + raise("Budget-tokens:")}
	case l.BudgetWallSecs > 0 && l.SpentWallSecs+u.Wall.Seconds() >= l.BudgetWallSecs:
		stop.Unit = UnitWall
		return &LimitHit{Reason: StopBudget, Stop: stop, Message: fmt.Sprintf("%s wall budget reached: %s of %s (%s recorded + %s this session)",
			l.Milestone, formatSecs(l.SpentWallSecs+u.Wall.Seconds()), formatSecs(l.BudgetWallSecs), formatSecs(l.SpentWallSecs), formatSecs(u.Wall.Seconds())) + raise("Budget-wall:")}
	}
	return nil
}
