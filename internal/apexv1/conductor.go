package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/exoport/apex_process_ape/internal/cost"
)

// The delegated conductor (v1.4.2, D659 A2–A7, D660): a person's
// conductor performs a person act only through `ape conductor act`, which
// checks the person's standing orders (orders.go), the act's tier, the
// conditions the orders set and the evidence the act rests on, then runs
// the act on the same code path a person's command takes, recording
// `by: conductor under orders <person>@<commit> (<reason>)`, an event
// (`delegated`, so the notifier runs on every act) and a line in the
// person's decision log. Every other route from a model session stays
// refused (session.person-only).

// CodeOrdersEscalate: a category the orders' escalate: list sends to the
// person holds for this act (D663 2c).
const CodeOrdersEscalate = "orders.escalate"

// CodeOrdersRefused: a delegated act ape refuses whatever the orders say —
// a push to the base branch, main or a tag; a PR with no forge CLI.
const CodeOrdersRefused = "orders.refused"

// CodeOrdersCheckRed: the mechanical check behind a delegated act, re-run
// by ape at act time (D662.2), is red: the act does not run.
const CodeOrdersCheckRed = "orders.check-red"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeOrdersEscalate, 1, ActionPerson, "`ape conductor act` (D663 2c): the act falls in a category the person's orders always send to them (escalate: touches-gate, changes-config, tokens-over), named in the message, even though the orders name the act: the conductor relays it"},
		CodeDoc{CodeOrdersRefused, 1, ActionPerson, "`ape conductor act` refuses this whatever the orders say: push-branch to main, the base branch, the remote's default branch or a tag, a branch that does not exist here; open-pr without the gh CLI (the message names the push URL); prepare-release for a version the changelog at HEAD (docs.changelog, else CHANGELOG.md) does not name"},
		CodeDoc{CodeOrdersCheckRed, 1, ActionPerson, "`ape conductor act` (D662.2): ape re-ran the mechanical check behind the act before acting — a milestone approve's gate self-test (`ape milestone gate-test` on its gate folder), a tweak approve's change check — and it is not as it must be, so the act did not run; the conductor relays it, whatever the orchestrator reported"},
	)
}

// EventDelegated: a conductor performed a person act under standing
// orders (v1.4.2). action is the act and its target, reason the
// conductor's reason, orders the orders it acted under.
const EventDelegated = "delegated"

// DelegatedAct is one `ape conductor act` request.
type DelegatedAct struct {
	// Act is the orders' name for it (OrderAnswer, …).
	Act string
	// Person whose orders it acts under (email or slug).
	Person string
	// Target: the change (answer, accept-task), the tweak, the milestone,
	// or the decision; Task is accept-task's T<n>.
	Target, Task string
	// Text is answer's answer.
	Text string
	// Reason quotes what the act rests on (D660.1); Evidence names it.
	Reason   string
	Evidence []string
}

// EvidenceRef is an evidence path as the act read it.
type EvidenceRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// DelegatedResult is what `ape conductor act` did.
type DelegatedResult struct {
	Act      string        `json:"act"`
	Target   string        `json:"target"`
	By       string        `json:"by"`
	Orders   string        `json:"orders"`
	Evidence []EvidenceRef `json:"evidence"`
	// Checks are the mechanical checks ape re-ran before acting (D662.2).
	Checks []string `json:"checks"`
	// Next are the commands left to the person (prepare-release's tag and
	// push), or what ape did beyond the record (push-branch's push, the
	// PR's URL).
	Next []string `json:"next,omitempty"`
	Seq  int64    `json:"seq"`
}

// limitBlockPrefixes open the reasons ape's own limits block a change
// with (attempts, attempts_total, replans): a block that is no question.
var limitBlockPrefixes = []string{"attempts exceeded", "attempts_total exceeded", "replans exceeded"}

// BlockedOnLimit reports whether a blocked change's reason is one of ape's
// limits rather than a question for a person.
func BlockedOnLimit(reason string) bool {
	for _, p := range limitBlockPrefixes {
		if strings.HasPrefix(reason, p) {
			return true
		}
	}
	return false
}

// CheckDelegated runs every check of a delegated act and returns the
// orders it would act under and the evidence it read. Nothing is written.
func (p *Project) CheckDelegated(ctx context.Context, a DelegatedAct) (*Orders, []EvidenceRef, error) {
	if strings.TrimSpace(a.Reason) == "" {
		return nil, nil, &UsageError{Code: CodeOrdersNoReason, Msg: "--reason is required: what the act rests on, quoting the evidence you read (ape status, the gate run, the review verdict, the brief)"}
	}
	tier := OrderTierOf(a.Act)
	switch tier {
	case TierDelegable, TierNamed:
	case 0:
		return nil, nil, &UsageError{Code: "usage.act", Msg: fmt.Sprintf("%q is no act (acts: %s)", a.Act, strings.Join(OrderActs(), ", "))}
	case TierNever:
		return nil, nil, &FailError{Code: CodeOrdersNonDelegable, Msg: a.Act + " is never delegated: only the person does it"}
	}
	o, err := p.LoadOrders(ctx, a.Person)
	if err != nil {
		return nil, nil, err
	}
	switch o.State {
	case OrdersRevoked:
		return nil, nil, &FailError{Code: CodeOrdersRevoked, Msg: fmt.Sprintf("%s revoked these orders (%s)", o.Person, o.Revoked.At)}
	case OrdersUnaccepted:
		return nil, nil, &FailError{Code: CodeOrdersUnaccepted, Msg: fmt.Sprintf("%s's orders (%s on %s) changed since they were accepted, or were never accepted: the person runs `ape orders accept --by %s` from a terminal", o.Person, o.Path, o.Branch, o.Person)}
	}
	if !o.Names(a.Act) {
		return nil, nil, &FailError{Code: CodeOrdersNotNamed, Msg: fmt.Sprintf("%s is delegated only when the orders name it under acts:, and %s's do not (acts: %s)", a.Act, o.Person, strings.Join(o.Acts, ", "))}
	}
	if err := p.checkConditions(ctx, o, a); err != nil {
		return nil, nil, err
	}
	if err := p.checkEscalate(ctx, o, a); err != nil {
		return nil, nil, err
	}
	ev, err := p.checkEvidence(a)
	if err != nil {
		return nil, nil, err
	}
	return o, ev, nil
}

// checkConditions checks what the orders set for the act, and what the
// act needs of its target.
func (p *Project) checkConditions(ctx context.Context, o *Orders, a DelegatedAct) error {
	cond := func(msg string) error { return &FailError{Code: CodeOrdersCondition, Msg: msg} }
	switch a.Act {
	case OrderTweakApprove:
		if c := o.Conditions.TweakApprove; c != nil && len(c.Types) > 0 {
			ci, err := p.requireChange(ctx, a.Target)
			if err != nil {
				return err
			}
			if typ := ci.Doc.Meta.Type; !slices.Contains(c.Types, typ) {
				return cond(fmt.Sprintf("tweak %s is of type %q, and the orders approve tweaks of types %s only", a.Target, typ, strings.Join(c.Types, ", ")))
			}
		}
	case OrderMilestoneApprove:
		if c := o.Conditions.MilestoneApprove; c != nil {
			return p.checkMilestoneConditions(ctx, a.Target, c.MaxChanges, c.Gate)
		}
	case OrderAnswer:
		if c := o.Conditions.Answer; c != nil && c.QuestionOnly {
			ci, err := p.requireChange(ctx, a.Target)
			if err != nil {
				return err
			}
			if r := ci.State.BlockedReason; r != nil && BlockedOnLimit(*r) {
				return cond(fmt.Sprintf("change %s is blocked on a limit (%s), and the orders answer only questions", a.Target, *r))
			}
		}
	case OrderAcceptTask:
		cfg, err := LoadConfig(p.Root)
		if err != nil {
			return err
		}
		if !cfg.HumanReview.Delegates() {
			return cond("a human-review task is delegated only where the project names a delegate for it (_apex/config.yaml human_review:), and this one names none")
		}
	}
	return nil
}

// checkMilestoneConditions: a milestone approve's change count and the
// state of its last gate run.
func (p *Project) checkMilestoneConditions(ctx context.Context, m string, maxChanges *int, gate string) error {
	if maxChanges != nil {
		if n := p.milestoneChangeCount(ctx, m); n > *maxChanges {
			return &FailError{Code: CodeOrdersCondition, Msg: fmt.Sprintf("%s has %d changes, and the orders approve milestones of at most %d", m, n, *maxChanges)}
		}
	}
	if gate == "" {
		return nil
	}
	state := "none"
	if last := p.LastGateRun(m); last != nil {
		state = "red"
		if last.Exit == 0 {
			state = "green"
		}
	}
	if state != gate {
		return &FailError{Code: CodeOrdersCondition, Msg: fmt.Sprintf("%s's last gate run is %s, and the orders approve only on a %s one", m, state, gate)}
	}
	return nil
}

// checkEscalate refuses an act in a category the orders send to the
// person (D663 2c).
func (p *Project) checkEscalate(ctx context.Context, o *Orders, a DelegatedAct) error {
	e := o.Escalate
	esc := func(cat, why string) error {
		return &FailError{Code: CodeOrdersEscalate, Msg: fmt.Sprintf("escalate: %s — %s; %s's orders send this to them", cat, why, o.Person)}
	}
	var changes []string
	milestone := ""
	switch a.Act {
	case OrderMilestoneApprove:
		milestone = a.Target
		for _, ci := range p.changes(ctx) {
			if ci.Doc != nil && ci.Doc.Meta.Milestone == milestone {
				changes = append(changes, ci.ID)
			}
		}
	case OrderAnswer, OrderTweakApprove, OrderAcceptTask:
		changes = []string{a.Target}
		milestone = p.ChangeMilestone(ctx, a.Target)
	case OrderQuestionAnswer:
		if q := p.question(a.Target); q != nil {
			milestone = q.Milestone
			if q.Change != "" {
				changes = []string{q.Change}
			}
		}
	}
	if e.TouchesGate || e.ChangesConfig {
		for _, id := range changes {
			for _, f := range ClassifyFindings(p.CheckChangeAnywhere(ctx, id)) {
				switch {
				case e.TouchesGate && f.Code == CodeTouchesGate:
					return esc(EscalateTouchesGate, "change "+id+" touches its milestone's gate")
				case e.ChangesConfig && f.Code == CodeGuardedConfigTask:
					return esc(EscalateChangesConfig, "change "+id+" edits a guarded block of _apex/config.yaml")
				}
			}
		}
		if e.TouchesGate && a.Act == OrderMilestoneApprove {
			if recs, err := p.LoadMilestoneRecords(); err == nil && recs[milestone] != nil && recs[milestone].Approval != nil {
				return esc(EscalateTouchesGate, milestone+" was approved before: approving it again re-approves its gate")
			}
		}
	}
	if e.TokensOver > 0 && milestone != "" {
		if n := p.milestoneTokens(ctx, milestone); n > e.TokensOver {
			return esc(EscalateTokensOver, fmt.Sprintf("%s has used %d tokens since its approval, over %d", milestone, n, e.TokensOver))
		}
	}
	return nil
}

// milestoneTokens are the tokens of m's runs since its approval (every
// run when it is not approved), a tweak's runs left out — as its spend.
func (p *Project) milestoneTokens(ctx context.Context, m string) int {
	rm := p.roadmap()
	if rm == nil {
		return 0
	}
	ms := rm.Find(m)
	if ms == nil {
		return 0
	}
	runs, _ := cost.RunCosts(p.Root)
	since := approvedSince(ms)
	tweaks := p.tweakIDs(ctx)
	n := 0
	for i := range runs {
		r := &runs[i]
		if r.StartedAt.Before(since) || tweaks[RunChangeArg(r.Args)] {
			continue
		}
		n += r.TokensTotal
	}
	return n
}

// question is the question id, nil when there is none.
func (p *Project) question(id string) *Question {
	qs, _ := p.Questions()
	for i := range qs {
		if qs[i].ID == id {
			return &qs[i]
		}
	}
	return nil
}

// milestoneChangeCount is how many changes, active or archived, name m.
func (p *Project) milestoneChangeCount(ctx context.Context, m string) int {
	n := 0
	for _, ci := range append(p.changes(ctx), p.archivedChanges()...) {
		if ci.Doc != nil && ci.Doc.Meta.Milestone == m {
			n++
		}
	}
	return n
}

// checkEvidence reads each evidence path (D660.1): it must exist in the
// project; a milestone approve must name the milestone's latest gate run's
// log when one is recorded.
func (p *Project) checkEvidence(a DelegatedAct) ([]EvidenceRef, error) {
	var out []EvidenceRef
	for _, e := range a.Evidence {
		abs := e
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Root, filepath.FromSlash(e))
		}
		rel, err := filepath.Rel(p.Root, filepath.Clean(abs))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, &FailError{Code: CodeOrdersEvidence, Msg: e + " is outside the project: evidence is a path in it"}
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, &FailError{Code: CodeOrdersEvidence, Msg: "evidence " + e + " cannot be read: " + err.Error()}
		}
		h := sha256.Sum256(data)
		out = append(out, EvidenceRef{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(h[:])})
	}
	if want := p.latestGateLog(a); want != "" && !slices.ContainsFunc(out, func(r EvidenceRef) bool { return r.Path == want }) {
		return nil, &FailError{Code: CodeOrdersEvidence, Msg: fmt.Sprintf("approving %s rests on its latest gate run: name its log as evidence (--evidence %s)", a.Target, want)}
	}
	return out, nil
}

// latestGateLog is the project-relative log of a milestone approve's
// latest gate run, "" for another act or a milestone with none.
func (p *Project) latestGateLog(a DelegatedAct) string {
	if a.Act != OrderMilestoneApprove {
		return ""
	}
	last := p.LastGateRun(a.Target)
	if last == nil || last.Log == "" {
		return ""
	}
	want := last.Log
	if filepath.IsAbs(want) {
		if r, err := filepath.Rel(p.Root, want); err == nil {
			want = r
		}
	}
	return filepath.ToSlash(filepath.Clean(want))
}

// RunDelegated checks a delegated act and, when every check holds, runs it
// as the person's command does, under a person act ape built from the
// orders (never from a file a model could write). It records the act's
// event and its line in the person's decision log.
func (p *Project) RunDelegated(ctx context.Context, a DelegatedAct, progress io.Writer) (*DelegatedResult, error) {
	o, ev, err := p.CheckDelegated(ctx, a)
	if err != nil {
		return nil, err
	}
	checks, more, err := p.recheck(ctx, a, progress)
	if err != nil {
		return nil, err
	}
	ev = append(ev, more...)
	commit, _ := git(ctx, p.Root, "log", "-1", "--format=%h", o.Branch, "--", o.Path)
	ordersRef := o.Slug + "@" + commit
	reason := strings.Join(strings.Fields(a.Reason), " ")
	by := "conductor under orders " + ordersRef + " (" + truncateRunes(reason, byReasonRunes) + ")"
	target := a.Target
	if a.Task != "" {
		target += " " + a.Task
	}
	if act := orderActs[a.Act].Act; act != "" {
		ctx = WithPersonAct(ctx, &PersonAct{Act: act, Target: target, By: by, granted: true})
	}
	milestone, change := "", a.Target
	var next []string
	switch a.Act {
	case OrderAnswer:
		_, err = p.AnswerChange(ctx, a.Target, a.Text, by)
	case OrderTweakApprove:
		_, err = p.ApproveTweak(ctx, a.Target, by)
	case OrderMilestoneApprove:
		milestone, change = a.Target, ""
		_, err = p.ApproveMilestone(ctx, a.Target, by, progress)
	case OrderAcceptTask:
		evidence := make([]string, 0, len(ev))
		for _, e := range ev {
			evidence = append(evidence, e.Path)
		}
		_, err = p.Accept(ctx, a.Target, a.Task, by, evidence...)
	case OrderDecisionAccept:
		change = ""
		_, err = p.AcceptDecision(ctx, a.Target, by)
	case OrderQuestionAnswer:
		var q *Question
		if q, err = p.AnswerQuestion(ctx, a.Target, a.Text, by); err == nil {
			milestone, change = q.Milestone, q.Change
		}
	case OrderPrepareRelease:
		change = ""
		// D666+ F4: the base branch the release commit is on goes first,
		// alone (an explicit refspec, no tag following it), then the tag.
		base, berr := p.baseBranch(ctx)
		if berr != nil || base == "" {
			base = "main"
		}
		next = []string{
			"git push --no-follow-tags origin refs/heads/" + base + ":refs/heads/" + base,
			"git tag -a " + a.Target + ` -m "` + a.Target + `"`,
			"git push origin refs/tags/" + a.Target,
		}
	case OrderPushBranch:
		change = ""
		next, err = p.pushBranch(ctx, a.Target)
	case OrderOpenPR:
		change = ""
		next, err = p.openPR(ctx, a.Target, a.Text, a.Reason)
	}
	if err != nil {
		return nil, err
	}
	if milestone == "" && change != "" {
		milestone = p.ChangeMilestone(ctx, change)
	}
	logged := p.Emit(ctx, Event{Event: EventDelegated, Milestone: milestone, Change: change, Action: a.Act + " " + target, Reason: reason, Orders: ordersRef}, progress)
	res := &DelegatedResult{Act: a.Act, Target: target, By: by, Orders: ordersRef, Evidence: ev, Checks: checks, Next: next, Seq: logged.Seq}
	if res.Evidence == nil {
		res.Evidence = []EvidenceRef{}
	}
	if _, lerr := p.logDecision(o.Slug, DecisionEntry{Kind: DecisionAct, Text: a.Act + " " + target, Reason: reason, Orders: ordersRef, Evidence: ev}); lerr != nil && progress != nil {
		fmt.Fprintf(progress, "decision log (%s): %v — the act is done and its event logged\n", o.Slug, lerr)
	}
	return res, nil
}

// byReasonRunes bounds the reason quoted in by: (the whole reason is in
// the event and the decision log).
const byReasonRunes = 160

// recheck re-runs the mechanical check behind the act, at HEAD, before it
// runs (D662.2): the claim the conductor acts on is checked by ape, not
// taken from a report. It returns what ran and the logs it read as
// evidence; a check not as it must be is orders.check-red.
func (p *Project) recheck(ctx context.Context, a DelegatedAct, progress io.Writer) ([]string, []EvidenceRef, error) {
	red := func(msg string) error { return &FailError{Code: CodeOrdersCheckRed, Msg: msg} }
	switch a.Act {
	case OrderMilestoneApprove:
		folders, _ := filepath.Glob(filepath.Join(p.GatesDir(), a.Target+"-*"))
		if len(folders) == 0 {
			// A gate command with no folder: approve's own self-check runs it.
			return []string{"approve's gate self-check (" + a.Target + " has no gate folder)"}, nil, nil
		}
		res, err := p.GateTest(ctx, folders[0], "", progress)
		if err != nil {
			return nil, nil, red(fmt.Sprintf("ape milestone gate-test %s: %v", p.Rel(folders[0]), err))
		}
		var ev []EvidenceRef
		var verdicts []string
		for i := range res.Runs {
			r := &res.Runs[i]
			verdicts = append(verdicts, fmt.Sprintf("%s exit %d %s", r.Mode, r.Exit, r.Verdict))
			if data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(r.Log))); err == nil {
				h := sha256.Sum256(data)
				ev = append(ev, EvidenceRef{Path: r.Log, SHA256: hex.EncodeToString(h[:])})
			}
		}
		check := "ape milestone gate-test " + res.Folder + ": " + strings.Join(verdicts, ", ")
		if !res.OK {
			return nil, nil, red(check + " — the gate is not as approval needs it (stub green, plain red)")
		}
		return []string{check}, ev, nil
	case OrderTweakApprove:
		if fs := p.CheckChangeAnywhere(ctx, a.Target); len(fs) > 0 {
			msgs := make([]string, 0, len(fs))
			for _, f := range fs {
				msgs = append(msgs, f.Message)
			}
			return nil, nil, red("ape change check " + a.Target + " is red: " + strings.Join(limit(msgs, 3), "; "))
		}
		return []string{"ape change check " + a.Target + ": clean"}, nil, nil
	case OrderPrepareRelease:
		// D666+ F2: the configured changelog (docs.changelog, else
		// CHANGELOG.md), and any of its version heading forms.
		path := p.releaseChangelog()
		data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(path)))
		if err != nil || !changelogNames(string(data), a.Target) {
			return nil, nil, &FailError{Code: CodeOrdersRefused, Msg: path + " at HEAD names no section for " + a.Target +
				" (## " + a.Target + ", ## " + strings.TrimPrefix(a.Target, "v") + " or ## [" + strings.TrimPrefix(a.Target, "v") + "]): a release is prepared for a version the changelog already names (D663+ Q-D663-2)"}
		}
		return []string{path + " at HEAD names " + a.Target}, nil, nil
	}
	return []string{}, nil, nil
}

// changelogNames reports a version heading for version in a changelog:
// "## v1.2.0", "## 1.2.0" or Keep a Changelog's "## [1.2.0]", "## [v1.2.0]",
// alone or followed by a space (a date, a link, "(unreleased)").
func changelogNames(doc, version string) bool {
	bare := strings.TrimPrefix(version, "v")
	forms := []string{"## " + bare, "## [" + bare + "]", "## v" + bare, "## [v" + bare + "]"}
	for l := range strings.SplitSeq(doc, "\n") {
		l = strings.TrimRight(l, "\r")
		for _, h := range forms {
			if l == h || strings.HasPrefix(l, h+" ") || (strings.HasPrefix(h, "## [") && strings.HasPrefix(l, h+"(")) {
				return true
			}
		}
	}
	return false
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// pushBranch pushes a work branch for the conductor (D663 3b): never the
// base branch, main or master, the remote's default branch or a tag, and
// only a branch that exists here. ape pushes it itself, with no tags.
func (p *Project) pushBranch(ctx context.Context, branch string) ([]string, error) {
	refused := func(why string) error {
		return &FailError{Code: CodeOrdersRefused, Msg: "push-branch " + branch + ": " + why}
	}
	b := strings.TrimPrefix(branch, "refs/heads/")
	if b == "" || strings.HasPrefix(branch, "refs/tags/") || strings.HasPrefix(b, "-") || strings.ContainsAny(b, ": ") {
		return nil, refused("not a work branch name")
	}
	if b == "main" || b == "master" {
		return nil, refused("never main or master: a conductor pushes work branches only")
	}
	if base, err := p.baseBranch(ctx); err == nil && b == base {
		return nil, refused("never the base branch")
	}
	if def, err := git(ctx, p.Root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && strings.TrimPrefix(def, "origin/") == b {
		return nil, refused("never the remote's default branch")
	}
	if _, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err != nil {
		return nil, refused("no such branch here")
	}
	ref := "refs/heads/" + b + ":refs/heads/" + b
	if _, err := git(ctx, p.Root, "push", "--no-follow-tags", "origin", ref); err != nil {
		return nil, &FailError{Code: CodeOrdersRefused, Msg: "git push failed: " + err.Error()}
	}
	return []string{"git push --no-follow-tags origin " + ref}, nil
}

// openPR opens a pull request for a pushed work branch through the gh CLI
// (D663 3b); merging stays the person's.
func (p *Project) openPR(ctx context.Context, branch, title, body string) ([]string, error) {
	if strings.TrimSpace(title) == "" {
		return nil, &UsageError{Code: "usage.text", Msg: "open-pr needs --text: the pull request's title"}
	}
	base, err := p.baseBranch(ctx)
	if err != nil {
		return nil, err
	}
	if branch == base || branch == "main" || branch == "master" {
		return nil, &FailError{Code: CodeOrdersRefused, Msg: "open-pr " + branch + ": a pull request is from a work branch"}
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		url, _ := git(ctx, p.Root, "remote", "get-url", "origin")
		return nil, &FailError{Code: CodeOrdersRefused, Msg: "open-pr: the gh CLI is not on PATH, so ape cannot open the pull request — the branch is pushed to " + url + "; the person opens it there"}
	}
	c := exec.CommandContext(ctx, gh, "pr", "create", "--head", branch, "--base", base, "--title", title, "--body", body)
	c.Dir = p.Root
	out, err := c.CombinedOutput()
	if err != nil {
		return nil, &FailError{Code: CodeOrdersRefused, Msg: "gh pr create failed: " + strings.TrimSpace(string(out))}
	}
	return []string{strings.TrimSpace(string(out))}, nil
}
