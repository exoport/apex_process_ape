package apexv1

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// E2: prompt-attested person acts (D178, D179, Q98, Q102). In a person's
// own Claude Code session (CLAUDECODE set, no APE_SESSION) a person-only
// command runs only on the person's own words:
//
//  1. `ape hook user-prompt` (UserPromptSubmit) records every prompt the
//     person types, {ts, session_id, transcript_path, prompt, prompt_id},
//     in _apex/state/prompts/<session>.jsonl (ignored). Under APE_SESSION
//     it records nothing: ape types those prompts itself (T7).
//  2. `ape hook pre-tool`, on a Bash command that runs a person-only act,
//     grants it only when the tool call's prompt_id is the recorded
//     prompt's (the prompt that opened this turn, Q102), the transcript's
//     user message with that promptId holds the same words, and the
//     matcher (OI-5) finds the act's words and its exact target there with
//     no negation. The grant is _apex/state/grants/<sha256 of the
//     command line>.json, single-use, for GrantTTL; a prompt grants each
//     act and target once.
//  3. The command consumes the grant (ConsumeGrant) and records
//     `by: <git config user.name> (prompt <ts>: "<quote>")`. No grant is
//     session.person-only, as before: it fails closed — no hook, no
//     record, no grant, no act.
//
// A long act (milestone approve, close --accept-red, archive
// --allow-pending-human; D179, OI-12) records its PersonAct in the run
// lock: a later call of the same command line attaches to that run with
// no grant (exit 75, run again), and the detached worker acts on the
// lock's record (LockPersonAct) instead of a grant of its own.

// The guarded acts, as person_act.act records them.
const (
	ActMilestoneApprove  = "milestone approve"
	ActFixApprove        = "change approve"
	ActAnswer            = "change answer"
	ActAcceptTask        = "change accept"
	ActAcceptConfig      = "project accept-config"
	ActAcceptRed         = "milestone close --accept-red"
	ActAllowPendingHuman = "change archive --allow-pending-human"
	ActDecisionAccept    = "decision accept"
)

// PersonActSpec is one guarded act: the command a model runs and what
// the person types for it (A12: `ape hook pre-tool --help` lists them).
type PersonActSpec struct {
	Act     string
	Command string
	Phrase  string
	// Long: a detached long run (D179: a re-run attaches without a grant).
	Long bool
}

// PersonActs are the 8 guarded acts (D47 Q6, D45, OI-4).
var PersonActs = []PersonActSpec{
	{ActMilestoneApprove, "ape milestone approve <M>", "approve <M>", true},
	{ActFixApprove, "ape change approve <fix-id>", "approve <fix-id>", false},
	{ActAnswer, `ape change answer <change-id> --text "<answer>"`, "answer <change-id>: <answer>", false},
	{ActAcceptTask, "ape change accept <change-id> T<n>", "accept T<n> of <change-id>", false},
	{ActAcceptConfig, "ape project accept-config", "accept the config", false},
	{ActAcceptRed, `ape milestone close <M> --accept-red "<reason>"`, "accept red on <M>", true},
	{ActAllowPendingHuman, "ape change archive <change-id> --allow-pending-human", "allow pending human on <change-id>", true},
	{ActDecisionAccept, "ape decision accept <ADR-NNNN>", "accept <ADR-NNNN>", false},
}

// PersonActGrammar is the phrase grammar, one line per guarded act.
func PersonActGrammar() string {
	var b strings.Builder
	for _, s := range PersonActs {
		fmt.Fprintf(&b, "  %-54s  %s\n", s.Command, s.Phrase)
	}
	return strings.TrimRight(b.String(), "\n")
}

// GrantTTL is how long a grant waits for its command: the moment between
// the pre-tool hook and the command's start.
var GrantTTL = 120 * time.Second

// quoteMax bounds the quoted clause a record keeps (T9).
const quoteMax = 160

// promptMax bounds the prompt a record keeps.
const promptMax = 64 << 10

// Codes (rc.7, rc7-e2).
const (
	// CodeByMismatch: a --by in a model session that is not git config
	// user.name (D178 Q-OI-2).
	CodeByMismatch = "session.by-mismatch"
	// CodeHooksPromptAbsent: project check's notice when the
	// UserPromptSubmit hook is missing or altered (T10).
	CodeHooksPromptAbsent = "hooks.prompt-absent"
)

// PersonCall is what one command line asks: the act, its target, and the
// sha256 of the normalised command line (argv) a grant is keyed by.
type PersonCall struct {
	Act    string
	Target string
	// Task is accept's T<n>; Text the answer's --text; By the --by given.
	Task, Text, By string
	ArgvSHA256     string
	// Line is the command as a reason shows it.
	Line string
	// Literal: no word holds $ or a backquote, so the command line the
	// hook judged is the one the command will see.
	Literal bool
}

// Spec is the call's act.
func (c PersonCall) Spec() PersonActSpec {
	for _, s := range PersonActs {
		if s.Act == c.Act {
			return s
		}
	}
	return PersonActSpec{Act: c.Act}
}

// Phrase is what the person types for this call.
func (c PersonCall) Phrase() string {
	switch c.Act {
	case ActMilestoneApprove, ActFixApprove:
		return "approve " + c.Target
	case ActAnswer:
		return "answer " + c.Target + ": " + c.Text
	case ActAcceptTask:
		return "accept " + c.Task + " of " + c.Target
	case ActAcceptConfig:
		return "accept the config"
	case ActAcceptRed:
		return "accept red on " + c.Target
	case ActAllowPendingHuman:
		return "allow pending human on " + c.Target
	case ActDecisionAccept:
		return "accept " + c.Target
	}
	return ""
}

// words are the phrase's words the matcher looks for, lowercased; an
// answer's text is matched apart.
func (c PersonCall) words() []string {
	ph := c.Phrase()
	if c.Act == ActAnswer {
		ph = "answer " + c.Target
	}
	return strings.Fields(strings.ToLower(ph))
}

// targetKey names the act and target a prompt may grant once.
func (c PersonCall) targetKey() string { return c.Act + "\x00" + c.Target + "\x00" + c.Task }

// ArgvSHA256 is the sha256 a grant is keyed by: the normalised command
// line, its words joined by NUL.
func ArgvSHA256(argv []string) string {
	sum := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(sum[:])
}

// PersonAct is a person act a command carries out, as a run lock and its
// result record it (person_act): who, and the prompt that granted it.
type PersonAct struct {
	Act        string `json:"act"`
	Target     string `json:"target"`
	ArgvSHA256 string `json:"argv_sha256"`
	By         string `json:"by"`
	PromptTS   string `json:"prompt_ts,omitempty"`
	Quote      string `json:"quote,omitempty"`
	// Grant is the consumed grant's record under grants/consumed/.
	Grant string `json:"grant,omitempty"`
	// granted: the act rests on the person's words in a model session
	// (ConsumeGrant, LockPersonAct) — never set from a file.
	granted bool
}

// Same reports whether a is the act c asks for: the same act, target and
// command line.
func (a *PersonAct) Same(c PersonCall) bool {
	return a != nil && a.Act == c.Act && a.Target == c.Target && a.ArgvSHA256 == c.ArgvSHA256
}

type personActKey struct{}

// WithPersonAct carries the act a command carries out to the body that
// checks it (requirePerson) and records its by.
func WithPersonAct(ctx context.Context, a *PersonAct) context.Context {
	return context.WithValue(ctx, personActKey{}, a)
}

// PersonActFrom is the act ctx carries, or nil.
func PersonActFrom(ctx context.Context) *PersonAct {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(personActKey{}).(*PersonAct)
	return a
}

// PersonBy is the by a granted act records (D178 Q-OI-2): the person's
// git user.name and the prompt that asked for it.
func PersonBy(name, promptTS, quote string) string {
	return name + " (prompt " + promptTS + `: "` + quote + `")`
}

// GitUserName is `git config user.name` in the project, "" when unset.
func GitUserName(ctx context.Context, root string) string {
	name, _ := git(ctx, root, "config", "user.name")
	return strings.TrimSpace(name)
}

// --- records ---------------------------------------------------------------

// PromptsDir holds the person's recorded prompts, one log per session.
func (p *Project) PromptsDir() string { return filepath.Join(p.StateDir(), "prompts") }

// GrantsDir holds pre-tool's grants, issued markers and consumed grants.
func (p *Project) GrantsDir() string { return filepath.Join(p.StateDir(), "grants") }

// PromptRecord is one line of _apex/state/prompts/<session>.jsonl.
type PromptRecord struct {
	TS             string `json:"ts"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Prompt         string `json:"prompt"`
	PromptID       string `json:"prompt_id"`
}

// RecordPrompt is `ape hook user-prompt`: it appends the prompt a person
// typed to the session's log. Nothing under APE_SESSION (T7), nothing
// outside a v1 project.
func RecordPrompt(payload HookPayload) error {
	if os.Getenv(envApeSession) != "" {
		return nil
	}
	cwd := payload.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	p, err := Open(FindRoot(cwd))
	if err != nil || !p.IsV1() {
		return nil //nolint:nilerr // not a v1 project: nothing to record
	}
	prompt := payload.Prompt
	if len(prompt) > promptMax {
		prompt = prompt[:promptMax]
	}
	rec := PromptRecord{
		TS: Stamp(), SessionID: payload.SessionID, TranscriptPath: payload.TranscriptPath,
		Prompt: prompt, PromptID: payload.PromptID,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	p.ensureState()
	if err := os.MkdirAll(p.PromptsDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p.promptLogPath(payload.SessionID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (p *Project) promptLogPath(sessionID string) string {
	key := sanitizeKey(sessionID)
	if key == "" {
		key = "no-session"
	}
	return filepath.Join(p.PromptsDir(), key+".jsonl")
}

// promptRecord is the session's last recorded prompt with promptID; nil
// when there is none.
func (p *Project) promptRecord(sessionID, promptID string) *PromptRecord {
	data, err := os.ReadFile(p.promptLogPath(sessionID))
	if err != nil {
		return nil
	}
	var found *PromptRecord
	for line := range strings.SplitSeq(string(data), "\n") {
		var r PromptRecord
		if line == "" || json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		if r.SessionID == sessionID && r.PromptID == promptID {
			found = &r
		}
	}
	return found
}

// grant is _apex/state/grants/<argv sha256>.json.
type grant struct {
	SessionID  string `json:"session_id"`
	PromptID   string `json:"prompt_id"`
	PromptTS   string `json:"prompt_ts"`
	Act        string `json:"act"`
	Target     string `json:"target"`
	Task       string `json:"task,omitempty"`
	ArgvSHA256 string `json:"argv_sha256"`
	Quote      string `json:"quote"`
	Expires    string `json:"expires"`
}

func (p *Project) grantPath(argvSHA string) string {
	return filepath.Join(p.GrantsDir(), sanitizeKey(argvSHA)+".json")
}

func (p *Project) issuedPath(sessionID, promptID string, c PersonCall) string {
	return filepath.Join(p.GrantsDir(), "issued", ArgvSHA256([]string{sessionID, promptID, c.targetKey()}))
}

func (p *Project) consumedDir() string { return filepath.Join(p.GrantsDir(), "consumed") }

func loadGrant(path string) *grant {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var g grant
	if json.Unmarshal(data, &g) != nil || g.ArgvSHA256 == "" {
		return nil
	}
	return &g
}

func (g *grant) live() bool {
	exp, err := time.Parse(time.RFC3339Nano, g.Expires)
	return err == nil && now().Before(exp)
}

func (g *grant) matches(c PersonCall) bool {
	return g.Act == c.Act && g.Target == c.Target && g.Task == c.Task && g.ArgvSHA256 == c.ArgvSHA256
}

// HasGrant reports whether a live grant for c waits to be consumed.
func (p *Project) HasGrant(c PersonCall) bool {
	g := loadGrant(p.grantPath(c.ArgvSHA256))
	return g != nil && g.matches(c) && g.live()
}

// ConsumeGrant takes the grant pre-tool wrote for c, once: nil (and no
// error) when there is none, it expired, or another call took it first.
// The grant moves to grants/consumed/, where a long run's worker finds it
// (LockPersonAct). The act's By is the caller's to fill in.
func (p *Project) ConsumeGrant(c PersonCall) *PersonAct {
	path := p.grantPath(c.ArgvSHA256)
	g := loadGrant(path)
	if g == nil || !g.matches(c) {
		return nil
	}
	if !g.live() {
		_ = os.Remove(path)
		return nil
	}
	if err := os.MkdirAll(p.consumedDir(), 0o700); err != nil {
		return nil
	}
	name := sanitizeKey(c.ArgvSHA256) + "-" + time.Now().UTC().Format("20060102T150405") + "-" + randomHex(3) + ".json"
	// A rename is atomic: of two calls racing for one grant, one wins.
	if os.Rename(path, filepath.Join(p.consumedDir(), name)) != nil {
		return nil
	}
	return &PersonAct{
		Act: c.Act, Target: c.Target, ArgvSHA256: c.ArgvSHA256,
		PromptTS: g.PromptTS, Quote: g.Quote, Grant: name, granted: true,
	}
}

// LockPersonAct is the person act a long run's worker carries out: its
// lock's, when the lock records one for this very command line and, in a
// model session, it rests on a grant its caller consumed. nil otherwise:
// the worker's own requirePerson then refuses in a model session.
func (p *Project) LockPersonAct(l *RunLock, argvSHA string) *PersonAct {
	if l == nil || l.PersonAct == nil || l.PersonAct.ArgvSHA256 != argvSHA {
		return nil
	}
	a := *l.PersonAct
	if ModelSession() == "CLAUDECODE" {
		if a.Grant == "" || strings.ContainsAny(a.Grant, `/\`) {
			return nil
		}
		g := loadGrant(filepath.Join(p.consumedDir(), a.Grant))
		if g == nil || g.ArgvSHA256 != argvSHA || g.Act != a.Act || g.Target != a.Target {
			return nil
		}
		a.granted = true
	}
	return &a
}

// pruneGrants drops grants, issued markers and consumed grants a day
// old: none of them is read after its turn.
func (p *Project) pruneGrants() {
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, dir := range []string{p.GrantsDir(), filepath.Join(p.GrantsDir(), "issued"), p.consumedDir()} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// --- the pre-tool side -------------------------------------------------------

// PersonActGate is pre-tool's E2 verdict on a Bash command whose person
// acts are calls (D179 first: an attach needs no grant). It allows (no
// output) outside a v1 project, in a session ape launched (the command
// refuses there, as before, T7) and for a command with no person act; it
// allows and writes a grant when the person's own words in this turn ask
// for the act; it denies otherwise, naming the phrase to type.
func PersonActGate(ctx context.Context, payload HookPayload, calls []PersonCall) PreToolDecision {
	if len(calls) == 0 || os.Getenv(envApeSession) != "" {
		return PreToolDecision{}
	}
	cwd := payload.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	p, err := Open(FindRoot(cwd))
	if err != nil || !p.IsV1() {
		return PreToolDecision{}
	}
	if len(calls) > 1 {
		return PreToolDecision{Deny: true, Reason: "this command runs " + strconv.Itoa(len(calls)) +
			" person acts — run each person act as a command of its own, each on the person's own words (E2)"}
	}
	c := calls[0]
	if !c.Literal {
		return personActDenied(c, "its command line holds $ or a backquote, so the command it runs is not the one judged here — write every word of a person act literally")
	}
	if c.Spec().Long {
		if d, decided := p.attachVerdict(ctx, c); decided {
			return d
		}
	}
	if c.By != "" {
		if name := GitUserName(ctx, p.Root); name != "" && c.By != name {
			return PreToolDecision{Deny: true, Reason: byMismatchMsg(c.By, name)}
		}
	}
	return p.grantVerdict(ctx, payload, c)
}

// attachVerdict decides a long act's call that meets a run on its scope
// (D179): the same act, target and command line attach with no grant —
// a live run, or an ended one whose result is unprinted while HEAD is
// its HeadAfter; another run is run.busy. decided false: no run, a grant
// is needed.
func (p *Project) attachVerdict(ctx context.Context, c PersonCall) (PreToolDecision, bool) {
	verb, scope := longVerb(c.Act), c.Target
	if l := p.LoadRunLock(scope); l != nil && l.Alive() {
		if l.Verb == verb && l.PersonAct.Same(c) {
			return PreToolDecision{}, true
		}
		return PreToolDecision{Deny: true, Reason: RunBusyPersonAct(l, c).Error()}, true
	}
	if r := p.LoadRunResult(scope); r != nil && !r.Reported && r.Verb == verb && r.PersonAct.Same(c) && r.HeadAfter == HeadSHA(ctx, p.Root) {
		return PreToolDecision{}, true
	}
	return PreToolDecision{}, false
}

// longVerb is the run-lock verb of a long act.
func longVerb(act string) string {
	switch act {
	case ActAcceptRed:
		return "milestone close"
	case ActAllowPendingHuman:
		return "change archive"
	}
	return act
}

// RunBusyPersonAct is run.busy for a person act meeting a run on its scope
// that is not that act (D179, T13): never an attach that would drop the
// person's flag.
func RunBusyPersonAct(l *RunLock, c PersonCall) error {
	what := "another run"
	if l.PersonAct == nil {
		what = "a run without this person act"
	}
	return &ConflictError{Code: CodeRunBusy, Action: ActionRetry, Msg: fmt.Sprintf(
		"%s: `ape %s %s` is running on it since %s (pid %d, log %s), %s — wait for it to end (`ape %s %s` attaches to it), then run %s: a new run, which needs the person's own words again",
		l.Scope, l.Verb, l.Scope, l.Started, l.PID, l.Log, what, l.Verb, l.Scope, "`"+c.Line+"`")}
}

// grantVerdict grants c when the prompt that opened this turn asks for it
// (Q102: the tool call's prompt_id is the prompt's), the transcript's
// user message with that promptId holds the same words, and this prompt
// has not granted the act and target before.
func (p *Project) grantVerdict(ctx context.Context, payload HookPayload, c PersonCall) PreToolDecision {
	if payload.PromptID == "" || payload.SessionID == "" {
		return personActDenied(c, "this tool call names no prompt (no prompt_id in the hook payload), so no prompt can grant it")
	}
	rec := p.promptRecord(payload.SessionID, payload.PromptID)
	if rec == nil {
		return personActDenied(c, "no prompt of this turn is recorded — is `"+HookCmdUserPrompt+"` the project's UserPromptSubmit hook? (`ape project init` merges it into .claude/settings.json)")
	}
	quote, raw, ok := matchAct(rec.Prompt, c)
	if !ok {
		return personActDenied(c, "the person's message that opened this turn does not ask for it")
	}
	transcript := payload.TranscriptPath
	if transcript == "" || rec.TranscriptPath != transcript || !transcriptHolds(transcript, payload.PromptID, raw) {
		return personActDenied(c, "the session's transcript does not show the person typing it in this turn")
	}
	if existing := loadGrant(p.grantPath(c.ArgvSHA256)); existing != nil && existing.live() && existing.matches(c) &&
		existing.SessionID == payload.SessionID && existing.PromptID == payload.PromptID {
		return PreToolDecision{} // granted already, not used yet
	}
	issued := p.issuedPath(payload.SessionID, payload.PromptID, c)
	if _, err := os.Stat(issued); err == nil {
		return personActDenied(c, "the person's words in this turn granted it once already, and a grant is used once")
	}
	p.ensureState()
	if err := os.MkdirAll(filepath.Dir(issued), 0o700); err != nil {
		return personActDenied(c, "the grant could not be written ("+err.Error()+")")
	}
	p.pruneGrants()
	g := grant{
		SessionID: payload.SessionID, PromptID: payload.PromptID, PromptTS: rec.TS,
		Act: c.Act, Target: c.Target, Task: c.Task, ArgvSHA256: c.ArgvSHA256,
		Quote: quoteFor(ctx, quote), Expires: now().Add(GrantTTL).UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err == nil {
		err = os.WriteFile(issued, []byte(Stamp()+"\n"), 0o600)
	}
	if err == nil {
		err = writeFileAtomic(p.grantPath(c.ArgvSHA256), string(data)+"\n")
	}
	if err != nil {
		return personActDenied(c, "the grant could not be written ("+err.Error()+")")
	}
	return PreToolDecision{}
}

// personActDenied is pre-tool's deny of a person act, naming why and the
// phrase that grants it.
func personActDenied(c PersonCall, why string) PreToolDecision {
	return PreToolDecision{Deny: true, Reason: fmt.Sprintf(
		"`%s` is a person's act (E2), and %s — ask the person to type **%s** in this session: their own words then grant this one command line once, for %s. "+
			"A `!` line fires no hook and cannot grant it; a person may also run the command from a terminal outside Claude Code. The phrases: `ape hook pre-tool --help`.",
		c.Line, why, c.Phrase(), GrantTTL)}
}

func byMismatchMsg(by, name string) string {
	return fmt.Sprintf("--by %q is not this machine's person: in a model session by: is always git config user.name (%q) — drop --by, or pass --by %q",
		by, name, name)
}

// ByMismatch is session.by-mismatch: a --by in a model session that is
// not git config user.name (D178 Q-OI-2).
func ByMismatch(by, name string) error {
	return &FailError{Code: CodeByMismatch, Action: ActionPerson, Msg: byMismatchMsg(by, name)}
}

// NoGrant is session.person-only for a person act in a person's Claude
// Code session with no grant: it names the phrase.
func NoGrant(c PersonCall) error {
	return &UsageError{Code: CodePersonOnly, Msg: fmt.Sprintf(
		"`%s` is for a person, and this runs in a model session (CLAUDECODE is set) with no grant from the person's own words — "+
			"the person types **%s** in this Claude Code session and the model runs the command in that turn (`ape hook pre-tool --help`); "+
			"a `!` line fires no hook and cannot grant it; or the person runs it from a terminal outside Claude Code", c.Line, c.Phrase())}
}

// NoUserName refuses a granted act when git has no user.name to record.
func NoUserName() error {
	return &UsageError{Code: CodePersonOnly, Msg: "a person act from a model session records by: git config user.name, which is not set" +
		" — the person sets it (`git config user.name \"<name>\"`), or runs the command from a terminal outside Claude Code"}
}

// --- the matcher (OI-5) -------------------------------------------------------

// negations void an act's words in their sentence (T4).
var negations = map[string]bool{
	"not": true, "no": true, "never": true, "dont": true, "don't": true, "wait": true, "hold": true,
	"cannot": true, "nope": true, "stop": true,
}

func isNegation(w string) bool { return negations[w] || strings.HasSuffix(w, "n't") }

// ptoken is one word of a prompt, lowercased, with its byte span.
type ptoken struct {
	s          string
	start, end int
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '\''
}

func promptTokens(text string) []ptoken {
	var out []ptoken
	start := -1
	for i, r := range text {
		if wordRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, ptoken{strings.ToLower(strings.Trim(text[start:i], "'")), start, i})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, ptoken{strings.ToLower(strings.Trim(text[start:], "'")), start, len(text)})
	}
	return out
}

const sentenceEnds = ".!?;\n"

// matchAct finds c's phrase in prompt (OI-5): its words, in order, the
// target exact (case aside), with no negation word before them in their
// sentence nor among the three words after the target, the sentence no
// question — and, for an answer, the --text itself after the target. It
// returns the quote (the sentence, at most quoteMax characters) and the
// raw sentence the transcript must hold.
func matchAct(prompt string, c PersonCall) (quote, raw string, ok bool) {
	text := strings.NewReplacer("’", "'", "‘", "'").Replace(prompt)
	toks := promptTokens(text)
	want := c.words()
	if len(want) == 0 {
		return "", "", false
	}
next:
	for i := 0; i+len(want) <= len(toks); i++ {
		for k, w := range want {
			if toks[i+k].s != w {
				continue next
			}
		}
		first, last := toks[i], toks[i+len(want)-1]
		start := strings.LastIndexAny(text[:first.start], sentenceEnds) + 1
		end := len(text)
		if c.Act != ActAnswer {
			if j := strings.IndexAny(text[last.end:], sentenceEnds); j >= 0 {
				end = last.end + j
			}
			if end < len(text) && text[end] == '?' {
				continue
			}
		}
		for _, t := range toks {
			if t.start >= start && t.end <= first.start && isNegation(t.s) {
				continue next
			}
		}
		if c.Act != ActAnswer {
			after := 0
			for _, t := range toks[i+len(want):] {
				if t.end > end || after == 3 {
					break
				}
				if isNegation(t.s) {
					continue next
				}
				after++
			}
		} else if ans := normSpace(c.Text); ans == "" || !strings.Contains(normSpace(text[last.end:]), ans) {
			continue
		}
		raw = strings.TrimSpace(text[start:end])
		return clip(raw, quoteMax), raw, true
	}
	return "", "", false
}

func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip cuts s to at most n characters, an ellipsis marking the cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// quoteFor is the clause as a record keeps it (T9): secrets redacted by
// the commit scan's rules, on one line, with no `"` (the record quotes
// it) and no `#` (a roadmap Status line ends at " #").
func quoteFor(ctx context.Context, clause string) string {
	redacted, ok := redactText(ctx, clause)
	if !ok {
		return "(quote withheld: the secret scan could not run)"
	}
	var b strings.Builder
	for _, r := range normSpace(redacted) {
		switch {
		case r == '"':
			b.WriteRune('\'')
		case r == '#':
			b.WriteRune('＃')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	return clip(b.String(), quoteMax)
}

// redactText hides every secret the commit scan's rules find in s; false
// when the scan could not run.
func redactText(ctx context.Context, s string) (string, bool) {
	found, err := scanAdded(ctx, []addedHunk{{path: "prompt.txt", start: 1, text: s + "\n"}}, nil)
	if err != nil {
		return "", false
	}
	for _, f := range found {
		s = strings.ReplaceAll(s, f.value, f.Redacted)
	}
	return s, true
}

// --- the transcript cross-check -----------------------------------------------

// transcriptLineMax bounds one transcript line (Claude Code writes lines
// over 100 KB).
const transcriptLineMax = 16 << 20

// transcriptHolds reports whether the transcript at path has a person's
// user message with promptID — not a tool result, not a meta or sidechain
// message — whose text holds clause (spaces collapsed, case aside).
func transcriptHolds(path, promptID, clause string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	want := strings.ToLower(normSpace(clause))
	needle := []byte(promptID)
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := readLine(r)
		if len(line) > 0 && bytes.Contains(line, needle) {
			if text, ok := personMessage(line, promptID); ok && strings.Contains(strings.ToLower(normSpace(text)), want) {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// readLine reads one line, keeping at most transcriptLineMax bytes of it.
func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(out)+len(chunk) <= transcriptLineMax {
			out = append(out, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return out, err
	}
}

// personMessage is the text of a transcript line that is a person's user
// message with promptID.
func personMessage(line []byte, promptID string) (string, bool) {
	var e struct {
		Type        string `json:"type"`
		IsMeta      bool   `json:"isMeta"`
		IsSidechain bool   `json:"isSidechain"`
		PromptID    string `json:"promptId"`
		Message     struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "user" || e.IsMeta || e.IsSidechain || e.PromptID != promptID || e.Message.Role != "user" {
		return "", false
	}
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return s, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return "", false
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type != "text" {
			return "", false // a tool result is text the model read, never a prompt (T2)
		}
		b.WriteString(bl.Text + "\n")
	}
	return b.String(), true
}
