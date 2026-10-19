package apexv1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/effort"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"gopkg.in/yaml.v3"
)

// Autonomous acceptance (D75, D76). A human-review task waits for a person
// (awaiting_human) unless _apex/config.yaml delegates it:
//
//	human_review:
//	  delegate: apex-accept                  # default when the block is present
//	  args: "--task {task} --change {id}"    # the default (D77, argstemplate.go)
//	  model: sonnet                          # optional; else models.<skill>
//	  effort: high                           # optional
//
// Delegation is off by default (D78): `project init` writes no human_review
// block, and without one --delegate is refused (delegate.off).
//
// The one default-on exception (D152): the docs-accuracy check of the
// milestone's docs change (D130). A change whose change.md frontmatter says
// `docs: true` has its human-review tasks taken by apex-accept, with the
// default args and model, and no human_review block — but only while the
// change's diff from its base_sha to its tip touches nothing outside the
// user-facing docs — release.docs, the changelog and docs.user_docs
// (Config.DocsDefaultScope, D158) — and the development folder (else
// delegate.docs-scope, a person's), so a code change marked docs: true
// still stops for a person.
// `human_review: {docs: person}` turns it off; a block with only docs:
// delegates no other task. A configured delegate (a block with any other
// key) takes every human-review task, the docs change's included (D78).
//
// `ape change accept <id> T<n> --delegate` starts the delegate skill in its
// own detached session (an ape task, like `change review --run`), marked
// APE_DELEGATE=<id>/<T>. The skill inspects the task's artefact read-only
// and records its verdict with `ape change accept <id> T<n> --verdict
// pass|fail --reason …`, which only that session may run: a verdict file in
// _apex/state/runs/delegate/<run-id>.json, ignored state, never a commit.
// When the session ends ape settles it: a session that committed, switched
// branch or left a file outside the change's assets/accept-* is voided;
// a pass is recorded by ape exactly as a person's acceptance is, by
// "delegate:<skill>" with the run's id — never as a person. A fail that
// names a defect in the change's own diff (--defect <path>, D188) goes
// back to the change, as a review block does: a reviewed change returns
// to in-progress, its review cleared, the return recorded in its state
// (delegate_returns.<T>) — once per task until a person answers the
// change or accepts the task. Any other fail leaves the task awaiting a
// person, its verdict shown by `ape status`: one that names no in-diff
// defect, a second fail after a return, a fail on an archived change. A
// session that ends without recording a verdict is a fail, "no verdict"
// (D78): its last message is never read for one. A fail is not retried on
// the same artefact: --delegate refuses (delegate.already-failed) until
// the change's tip moves.
//
// human_review is under the config.changed guard: a model cannot add the
// block and accept its own work. --delegate refuses while a commit nobody
// accepted changed it, and while the working tree's block differs from
// HEAD's.

func init() {
	findingRules = append(findingRules, rule("", `^human_review config: `, CodeDelegateInvalidConfig))
}

// humanReviewFindings is `ape project check` on the human_review block:
// an args template with an unknown placeholder (D77), an effort that is
// not a level.
func (p *Project) humanReviewFindings() []Finding {
	cfg, err := LoadConfig(p.Root)
	if err != nil || cfg.HumanReview == nil {
		return nil
	}
	var out []Finding
	if err := ValidateArgsTemplate(cfg.HumanReview.Args); err != nil {
		out = append(out, Finding{File: configYAML, Message: "human_review config: " + err.Error() + " — fix human_review.args (placeholders: " +
			strings.Join(ArgsPlaceholders, ", ") + ")"})
	}
	if err := effort.CheckLevel(strings.TrimSpace(cfg.HumanReview.Effort)); err != nil {
		out = append(out, Finding{File: configYAML, Message: "human_review config: effort " + err.Error() + " — fix human_review.effort"})
	}
	return out
}

// EnvDelegate marks a delegate session: its value is <change>/<task>.
const EnvDelegate = "APE_DELEGATE"

// DefaultDelegateSkill is human_review.delegate when the block is present
// without one (D76).
const DefaultDelegateSkill = "apex-accept"

// DefaultDelegateArgs is human_review.args when unset.
const DefaultDelegateArgs = "--task {task} --change {id}"

// DelegateByPrefix starts a delegated acceptance's By.
const DelegateByPrefix = "delegate:"

// human_review.docs values (D152 Q70): who takes a docs change's
// docs-accuracy check. Absent means DocsDelegate.
const (
	DocsDelegate = "delegate"
	DocsPerson   = "person"
)

// Why a delegate may take a task now (ape status's delegable, D152 Q71).
const (
	DelegableDocsDefault = "docs-default"
	DelegableConfigured  = "configured"
)

// HumanReviewConfig is _apex/config.yaml's human_review block.
type HumanReviewConfig struct {
	Delegate string `json:"delegate,omitempty" yaml:"delegate"`
	Args     string `json:"args,omitempty"     yaml:"args"`
	Model    string `json:"model,omitempty"    yaml:"model"`
	Effort   string `json:"effort,omitempty"   yaml:"effort"`
	// Docs is DocsDelegate, DocsPerson or "" (the docs default, D152).
	Docs string `json:"docs,omitempty" yaml:"docs"`
	// docsOnly: the block sets docs: and nothing else, which delegates no
	// other task (Delegates).
	docsOnly bool
}

// UnmarshalYAML validates the block as it parses: its keys (a typo would
// otherwise turn a docs-only block into a delegate for every task) and
// docs:.
func (h *HumanReviewConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKeys(n, "human_review", "delegate", "args", "model", "effort", "docs"); err != nil {
		return err
	}
	type plain HumanReviewConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	p.Docs = strings.TrimSpace(p.Docs)
	switch p.Docs {
	case "", DocsDelegate, DocsPerson:
	default:
		return fmt.Errorf("human_review.docs: %q is not %s or %s", p.Docs, DocsDelegate, DocsPerson)
	}
	*h = HumanReviewConfig(p)
	h.docsOnly = n.Kind == yaml.MappingNode && len(n.Content) == 2 && n.Content[0].Value == "docs"
	return nil
}

// Delegates reports whether the block configures a delegate for every
// human-review task (D75, D78): it is present and sets a key other than
// docs: (an empty block delegates, as it always has).
func (h *HumanReviewConfig) Delegates() bool { return h != nil && !h.docsOnly }

// DocsDefault reports whether a docs change's docs-accuracy check is a
// delegate's by default (D152): human_review.docs is not person.
func (h *HumanReviewConfig) DocsDefault() bool { return h == nil || h.Docs != DocsPerson }

// Skill is the delegate skill: delegate, else DefaultDelegateSkill.
func (h *HumanReviewConfig) Skill() string {
	if h == nil {
		return ""
	}
	if s := strings.TrimSpace(h.Delegate); s != "" {
		return s
	}
	return DefaultDelegateSkill
}

// ArgsTemplate is args, else DefaultDelegateArgs.
func (h *HumanReviewConfig) ArgsTemplate() string {
	if strings.TrimSpace(h.Args) == "" {
		return DefaultDelegateArgs
	}
	return h.Args
}

// SessionModel is the delegate session's models entry: human_review's
// model and effort, each else models.<skill>'s (else, in the launch
// decision, the D65 defaults init wrote there, or the asking session's).
func (h *HumanReviewConfig) SessionModel(models map[string]SkillModel) SkillModel {
	m := models[h.Skill()]
	if v := strings.TrimSpace(h.Model); v != "" {
		m.Model = v
	}
	if v := strings.TrimSpace(h.Effort); v != "" {
		m.Effort = v
	}
	return m
}

// acceptedByTrailer is the trailer naming a delegated acceptance, on its
// record commit and on the archive squash (D75).
func acceptedByTrailer(a Accept) string {
	return "Accepted-By: " + a.By + " " + a.Task + " run " + a.Run
}

// delegatedTrailers are st's delegated acceptances as trailer lines.
func delegatedTrailers(st *State) []string {
	var out []string
	for _, a := range st.Accepted {
		if a.Run != "" && strings.HasPrefix(a.By, DelegateByPrefix) {
			out = append(out, acceptedByTrailer(a))
		}
	}
	return out
}

// DelegateRun is ape's record of the delegate session it started for one
// task: _apex/state/logs/<id>/delegate-<task>.json (ignored). A second
// --delegate attaches to it while it runs.
type DelegateRun struct {
	Change string `json:"change"`
	Task   string `json:"task"`
	Skill  string `json:"skill"`
	Args   string `json:"args"`
	// Folder is the change's folder the session inspects (project-
	// relative): changes/<id> in flight, its archive folder once archived.
	Folder     string `json:"folder"`
	DetachedID string `json:"detached_id"`
	StartedAt  string `json:"started_at"`
	// HeadSHA and Branch are HEAD when the session started: it may move
	// neither.
	HeadSHA string `json:"head_sha"`
	Branch  string `json:"branch,omitempty"`
	// ChangeTip is what the session judged (changeTip): a fail there is
	// not delegated again until it moves.
	ChangeTip string `json:"change_tip,omitempty"`
	// Parent is the APE_SESSION of the session that asked, "" from a shell.
	Parent string `json:"parent,omitempty"`
	Launch Launch `json:"launch"`
}

// DelegateRunPath is where a task's delegate-run record lives.
func (p *Project) DelegateRunPath(id, task string) string {
	return filepath.Join(p.LogDir(id), "delegate-"+task+".json")
}

// LoadDelegateRun reads the record; nil when there is none.
func (p *Project) LoadDelegateRun(id, task string) *DelegateRun {
	var r DelegateRun
	if !readJSON(p.DelegateRunPath(id, task), &r) {
		return nil
	}
	return &r
}

// SaveDelegateRun writes the record.
func (p *Project) SaveDelegateRun(r *DelegateRun) error {
	p.ensureState()
	return writeJSON(p.DelegateRunPath(r.Change, r.Task), r)
}

// ClearDelegateRun removes the record once its outcome is settled.
func (p *Project) ClearDelegateRun(id, task string) {
	_ = os.Remove(p.DelegateRunPath(id, task))
}

// DelegateVerdict is what a delegate session recorded: _apex/state/runs/
// delegate/<run>.json. Voided lists why ape voided it, when it did.
type DelegateVerdict struct {
	Change   string     `json:"change"`
	Task     string     `json:"task"`
	Skill    string     `json:"skill,omitempty"`
	Run      string     `json:"run"`
	Verdict  string     `json:"verdict"`
	Reason   string     `json:"reason"`
	Evidence []Evidence `json:"evidence,omitempty"`
	At       string     `json:"at"`
	Voided   []string   `json:"voided,omitempty"`
	// ChangeTip is the change's tip the verdict judged (DelegateRun).
	ChangeTip string `json:"change_tip,omitempty"`
	// Defects are the paths a fail names as defective (--defect, D188),
	// project-relative, as the session gave them: ape counts only those in
	// the change's own diff when it settles the run.
	Defects []string `json:"defects,omitempty"`
}

// DelegateReturn is one delegate fail ape sent back to the change (D188):
// the run, its reason, and the defects in the change's diff it named.
type DelegateReturn struct {
	Run     string   `json:"run"`
	Reason  string   `json:"reason"`
	Defects []string `json:"defects"`
	At      string   `json:"at"`
}

// Why a delegate fail went to a person rather than back to the change
// (DelegateOutcome.ToPerson, D188).
const (
	// ToPersonNoDefect: the fail names no defect in the change's own diff
	// ("no verdict" included).
	ToPersonNoDefect = "no-defect-in-diff"
	// ToPersonSecondFail: the delegate returned the task once already
	// since a person last answered the change or accepted the task.
	ToPersonSecondFail = "second-fail"
	// ToPersonArchived: ape cannot reopen an archived change.
	ToPersonArchived = "archived"
	// ToPersonNotReviewed: the change is no longer reviewed.
	ToPersonNotReviewed = "not-reviewed"
)

// DelegateVerdictPath is run's verdict file.
func (p *Project) DelegateVerdictPath(run string) string {
	return filepath.Join(runlog.DelegateRoot(p.Root), run+".json")
}

// LoadDelegateVerdict reads run's verdict; nil when there is none.
func (p *Project) LoadDelegateVerdict(run string) *DelegateVerdict {
	var v DelegateVerdict
	if run == "" || !readJSON(p.DelegateVerdictPath(run), &v) {
		return nil
	}
	return &v
}

// lastDelegateVerdicts is change id's newest delegate verdict per task,
// as `ape status` shows it; nil when there is none.
func (p *Project) lastDelegateVerdicts(id string) map[string]*DelegateVerdict {
	files, _ := filepath.Glob(filepath.Join(runlog.DelegateRoot(p.Root), "*.json"))
	var out map[string]*DelegateVerdict
	for _, f := range files {
		var v DelegateVerdict
		if !readJSON(f, &v) || v.Change != id || v.Task == "" {
			continue
		}
		if cur := out[v.Task]; cur == nil || v.At > cur.At || v.At == cur.At && v.Run > cur.Run {
			if out == nil {
				out = map[string]*DelegateVerdict{}
			}
			out[v.Task] = &v
		}
	}
	return out
}

// DelegatePlan is what --delegate starts: the skill, its filled args and
// the session's models entry, for one awaiting task.
type DelegatePlan struct {
	Change string
	Task   string
	Skill  string
	Args   string
	Folder string
	Model  SkillModel
	// ChangeTip is the change's tip now (changeTip).
	ChangeTip string
	// Reason is why a delegate may take the task: DelegableConfigured,
	// or DelegableDocsDefault (D152).
	Reason string
}

// PrepareDelegate checks that task of change id may be accepted by a
// delegate now, and plans the session: human_review is set, valid and
// accepted (no unaccepted config.changed names it, and the working tree's
// block is HEAD's); the task is a human-review task awaiting a person (the
// change reviewed or archived, the task not accepted). Without a
// configured delegate, only the docs default may take it (D152,
// prepareDocsDelegate).
func (p *Project) PrepareDelegate(ctx context.Context, id, task string) (*DelegatePlan, error) {
	if v := os.Getenv(EnvDelegate); v != "" {
		return nil, &UsageError{Code: "delegate.nested", Msg: "this is a delegate session (" + EnvDelegate + "=" + v + "): a delegate cannot start another delegate"}
	}
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return nil, err
	}
	hr := cfg.HumanReview
	if !hr.Delegates() {
		return p.prepareDocsDelegate(ctx, cfg, id, task)
	}
	if err := p.refuseUnacceptedConfig(ctx, "human_review"); err != nil {
		return nil, err
	}
	tg, err := p.delegateTarget(ctx, id, task)
	if err != nil {
		return nil, err
	}
	return p.planDelegate(ctx, cfg, hr, tg, DelegableConfigured)
}

// prepareDocsDelegate is PrepareDelegate with no configured delegate: the
// docs default (D152) — apex-accept, with the default args and model —
// takes a human-review task of a docs change (change.md docs: true) when
// human_review.docs is not person and the change's diff from its base_sha
// to its tip touches only the docs (Config.DocsDefaultScope) and the
// development folder. Any other task is a person's (delegate.off), as
// before D152. release.docs and the docs block bound the default, so they
// are guarded here with human_review.
func (p *Project) prepareDocsDelegate(ctx context.Context, cfg Config, id, task string) (*DelegatePlan, error) {
	hr := cfg.HumanReview
	tg, terr := p.delegateTarget(ctx, id, task)
	if tg == nil || tg.doc == nil || !tg.doc.Meta.Docs {
		return nil, delegateOff(id, task, hr)
	}
	// The guard first (Q90): a model's own unaccepted docs: person is
	// delegate.unaccepted-config, as the help says, not delegate.off.
	if err := p.refuseUnacceptedConfig(ctx, "human_review", "release", "docs"); err != nil {
		return nil, err
	}
	if !hr.DocsDefault() {
		return nil, &FailError{Code: "delegate.off", Msg: "the docs delegate is off: _apex/config.yaml sets human_review.docs: person, so only a person accepts " + task +
			" of the docs change " + id + " — a person runs `ape change accept " + id + " --task " + task + " --by <who>`"}
	}
	if terr != nil {
		return nil, terr
	}
	scope := cfg.DocsDefaultScope()
	outside, err := p.docsScopeOutside(ctx, scope, id, tg.state)
	if err != nil || len(outside) > 0 {
		why := "touches " + strings.Join(limit(outside, 10), ", ")
		if err != nil {
			why = "has a diff ape cannot read (" + err.Error() + ")"
		}
		return nil, &FailError{Code: "delegate.docs-scope", Msg: "the docs change " + id + " " + why + ", outside the docs (" + strings.Join(scope, ", ") +
			": release.docs, docs.changelog and docs.user_docs) and the development folder, so its docs check " + task + " is not a delegate's by default (D152) — a person reviews it (`ape change accept " + id + " --task " + task +
			" --by <who>`), or re-plans the non-docs work into a change of its own"}
	}
	if hr == nil {
		hr = &HumanReviewConfig{}
	}
	return p.planDelegate(ctx, cfg, hr, tg, DelegableDocsDefault)
}

// delegateOff is delegate.off for task of change id under hr (nil, or a
// block that sets only docs:).
func delegateOff(id, task string, hr *HumanReviewConfig) error {
	why := "_apex/config.yaml has no human_review: block"
	if hr != nil {
		why = "_apex/config.yaml's human_review block sets only docs:, which delegates no other task"
	}
	return &FailError{Code: "delegate.off", Msg: "delegation is off: " + why + ", so only a person accepts " + task +
		" (the one default exception, D152: a docs change's docs check — change.md docs: true, its diff only the docs and the development folder)" +
		" — a person runs `ape change accept " + id + " --task " + task + " --by <who>`, or adds human_review: {delegate: " + DefaultDelegateSkill + "} (a person's change: config.changed guards it)"}
}

// planDelegate is the session for tg's task: hr's skill, filled args and
// models entry, refused while the task's last delegate verdict is a fail
// at the change's current tip (D78).
func (p *Project) planDelegate(ctx context.Context, cfg Config, hr *HumanReviewConfig, tg *delegateTargetInfo, reason string) (*DelegatePlan, error) {
	id, task, folder := tg.id, tg.task, tg.folder
	tip := p.changeTip(ctx, id, folder, tg.archived)
	if last := p.lastDelegateVerdicts(id)[task]; last != nil && last.Verdict == "fail" && len(last.Voided) == 0 && last.ChangeTip != "" && last.ChangeTip == tip {
		return nil, &FailError{Code: "delegate.already-failed", Msg: "the delegate failed " + task + " of " + id + " at this tip already (run " + last.Run + ": " + last.Reason +
			") — a person reviews it (`ape change accept " + id + " --task " + task + " --by <who>`); the delegate runs again only once the change moves"}
	}
	args, err := FillArgs(hr.ArgsTemplate(), ArgsVarsFor(id, task, folder))
	if err != nil {
		return nil, &FailError{Code: CodeDelegateInvalidConfig, Msg: "human_review.args: " + err.Error() + " — a person fixes _apex/config.yaml"}
	}
	if err := effort.CheckLevel(strings.TrimSpace(hr.Effort)); err != nil {
		return nil, &FailError{Code: CodeDelegateInvalidConfig, Msg: "human_review.effort: " + err.Error() + " — a person fixes _apex/config.yaml"}
	}
	return &DelegatePlan{Change: id, Task: task, Skill: hr.Skill(), Args: args, Folder: folder, Model: hr.SessionModel(cfg.Models), ChangeTip: tip, Reason: reason}, nil
}

// docsScopeOutside lists the paths the change's diff from its base_sha to
// its tip touches outside docs (Config.DocsDefaultScope), the development folder and
// ape's state (D152 Q69 (b)): what keeps the docs default off a change
// that is not docs only. The tip is change/<id>'s while the change is
// unarchived, the branch tip archive recorded once archived. Renames count
// as both paths, so code moved into docs/ is still code it touched.
func (p *Project) docsScopeOutside(ctx context.Context, docs []string, id string, st *State) ([]string, error) {
	touched, err := p.changeTouched(ctx, id, st)
	if err != nil {
		return nil, err
	}
	return p.outsideDocs(docs, touched), nil
}

// changeTouched lists the paths the change's diff from its base_sha to its
// tip touches (docsScopeOutside's tip), renames as both paths.
func (p *Project) changeTouched(ctx context.Context, id string, st *State) ([]string, error) {
	if st == nil || st.BaseSHA == "" {
		return nil, errors.New("no base_sha: the change was never started")
	}
	tip := st.BranchTip
	if st.Status != StatusArchived {
		tip, _ = git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+ChangeBranch(id))
	}
	if tip == "" {
		return nil, errors.New("no branch tip")
	}
	out, err := git(ctx, p.Root, "diff", "--name-only", "--no-renames", st.BaseSHA, tip)
	if err != nil {
		return nil, err
	}
	var touched []string
	for f := range strings.SplitSeq(out, "\n") {
		if f != "" {
			touched = append(touched, f)
		}
	}
	return touched, nil
}

// outsideDocs lists the paths of touched outside docs, the development
// folder and ape's state.
func (p *Project) outsideDocs(docs, touched []string) []string {
	var outside []string
	for _, f := range touched {
		if !p.notCode(f) && !inReleaseDocs(docs, f) {
			outside = append(outside, f)
		}
	}
	return outside
}

// inReleaseDocs reports whether rel (project-relative) is one of docs or
// inside one of them.
func inReleaseDocs(docs []string, rel string) bool {
	for _, d := range docs {
		c := path.Clean(strings.TrimSpace(filepath.ToSlash(d)))
		if rel == c || strings.HasPrefix(rel, c+"/") {
			return true
		}
	}
	return false
}

// guardedKeysRe reads the keys out of a config.changed finding.
var guardedKeysRe = regexp.MustCompile(`^guarded config keys \(([^)]*)\)`)

// refuseUnacceptedConfig refuses delegation while one of blocks (the
// human_review block; release and docs too for the docs default, whose
// scope they bound) is not a person's: a commit since the v1 epoch changed
// it and no person accepted it (config.changed's rule, applied to blocks,
// so docs, which only the delegate needs guarded, is no config.changed),
// or the working tree's block is not the one committed at HEAD.
func (p *Project) refuseUnacceptedConfig(ctx context.Context, blocks ...string) error {
	if !p.isWorkTree(ctx) {
		return nil
	}
	refuse := func(block, why string) error {
		return &FailError{Code: "delegate.unaccepted-config", Msg: "_apex/config.yaml's " + block + " block " + why +
			" — a model may not delegate a person's acceptance: a person reviews it and runs `ape project accept-config --by <who>` (or reverts it)"}
	}
	if epoch := p.v1Epoch(ctx, "HEAD"); epoch != "" {
		for _, f := range p.configFindingsFor(ctx, epoch, blocks) {
			m := guardedKeysRe.FindStringSubmatch(f.Message)
			if len(m) < 2 {
				continue
			}
			for _, b := range blocks {
				if slices.Contains(strings.Split(m[1], ", "), b) {
					return refuse(b, "was changed by a commit no person accepted (config.changed: "+f.Message+")")
				}
			}
		}
	}
	committed := p.configBlocksAt(ctx, "HEAD", configYAML)
	live := map[string]any{}
	if data, err := os.ReadFile(ConfigPath(p.Root)); err == nil {
		_ = yaml.Unmarshal(data, &live)
	}
	for _, b := range blocks {
		if !reflect.DeepEqual(committed[b], live[b]) {
			return refuse(b, "differs from the one committed at HEAD (an uncommitted edit)")
		}
	}
	return nil
}

// delegateTargetInfo is the change a delegate would judge: its folder,
// project-relative — changes/<id> in flight, its archive folder once
// archived (as this checkout's tree holds it) — its change.md and its
// state.json.
type delegateTargetInfo struct {
	id, task, folder string
	archived         bool
	doc              *Change
	state            *State
}

// delegateTarget checks task of change id awaits a person. On a refusal
// about the task it still returns the change it read (doc and state), for
// prepareDocsDelegate to tell a docs change; nil when there is none.
func (p *Project) delegateTarget(ctx context.Context, id, task string) (*delegateTargetInfo, error) {
	ci, err := p.requireChange(ctx, id)
	if fe, ok := errors.AsType[*FailError](err); ok && fe.Code == "change.archived" {
		return p.archivedDelegateTarget(id, task)
	}
	if err != nil {
		return nil, err
	}
	tg := &delegateTargetInfo{id: id, task: task, folder: p.Rel(p.ChangeDir(id)), doc: ci.Doc, state: ci.State}
	if err := humanTask(ci.Doc, id, task); err != nil {
		return tg, err
	}
	st := p.withBaseAccepted(ctx, id, ci.State)
	if !slices.Contains(awaitingHuman(st, ci.Doc), task) {
		if st.Status != StatusReviewed {
			return tg, notAwaiting(id, task, "the change is "+st.Status+", and a human-review task awaits a person once the change is reviewed")
		}
		return tg, notAwaiting(id, task, "it is accepted already")
	}
	return tg, nil
}

// archivedDelegateTarget is delegateTarget for an archived change: its
// archive folder, as this checkout's tree holds it.
func (p *Project) archivedDelegateTarget(id, task string) (*delegateTargetInfo, error) {
	name, ok := p.ArchivedIDs()[id]
	if !ok {
		return nil, &FailError{Code: CodeChangeNotFound, Msg: "the archived change " + id + " is not in this checkout's tree — run the delegate on the branch it was archived into"}
	}
	dir := p.archivedPath(name)
	st, err := LoadState(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, &FailError{Code: CodeStateUnreadable, Msg: err.Error()}
	}
	doc := p.loadChangeDoc(filepath.Join(dir, "change.md"))
	if doc == nil {
		return nil, &FailError{Code: CodeNoChangeMD, Msg: "the archived change " + id + " has no change.md"}
	}
	tg := &delegateTargetInfo{id: id, task: task, folder: p.Rel(dir), archived: true, doc: doc, state: st}
	if err := humanTask(doc, id, task); err != nil {
		return tg, err
	}
	if !slices.Contains(awaitingHuman(st, doc), task) {
		return tg, notAwaiting(id, task, "it is accepted already")
	}
	return tg, nil
}

// notAwaiting is delegate.not-awaiting for task of change id.
func notAwaiting(id, task, why string) error {
	return &FailError{Code: "delegate.not-awaiting", Msg: "task " + task + " of " + id + " is not awaiting a person: " + why}
}

// changeTip is what a delegate judges, as a revision: change/<id>'s tip
// while the change is unarchived, the archive folder's tree at HEAD once
// archived; "" when there is none.
func (p *Project) changeTip(ctx context.Context, id, folder string, archived bool) string {
	rev := "refs/heads/" + ChangeBranch(id)
	if archived {
		rev = "HEAD:./" + folder
	}
	out, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		return ""
	}
	return out
}

// loadChangeDoc parses a change.md; nil when it cannot be read.
func (p *Project) loadChangeDoc(file string) *Change {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	return ParseChange(p.Rel(file), string(data))
}

// RecordDelegateVerdict is `ape change accept <id> <task> --verdict …`:
// the delegate session's verdict, recorded only from inside that session
// (APE_DELEGATE=<id>/<task>, its run still recorded, and — when set —
// APE_DETACHED_ID its run). Evidence paths are files in the repository (or
// URLs), recorded with their SHA-256 as they are now.
//
// defects (--defect, D188) go only with a fail: each is recorded as a
// project-relative path; whether it is in the change's diff is settled
// when the session ends, never here.
func (p *Project) RecordDelegateVerdict(ctx context.Context, id, task, verdict, reason string, evidence, defects []string) (*DelegateVerdict, error) {
	notIn := func(why string) error {
		return &UsageError{Code: "delegate.not-in-session", Msg: "--verdict records a delegate session's verdict, and " + why +
			" — only the session `ape change accept " + id + " " + task + " --delegate` starts records it"}
	}
	marker := os.Getenv(EnvDelegate)
	if marker == "" {
		return nil, notIn("this is not a delegate session (" + EnvDelegate + " is unset)")
	}
	if marker != id+"/"+task {
		return nil, notIn("this delegate session is for " + marker)
	}
	run := p.LoadDelegateRun(id, task)
	if run == nil {
		return nil, notIn("no delegate run of " + id + " " + task + " is in progress")
	}
	if d := os.Getenv(envDetachedRun); d != "" && d != run.DetachedID {
		return nil, notIn("this process belongs to run " + d + ", not to the delegate run " + run.DetachedID)
	}
	if verdict != "pass" && verdict != "fail" {
		return nil, &UsageError{Code: "usage.verdict", Msg: "--verdict must be pass or fail"}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, &UsageError{Code: "usage.reason", Msg: "--reason is required: why the artefact passes or fails the task"}
	}
	rels := p.defectPaths(defects)
	if len(rels) > 0 && verdict != "fail" {
		return nil, &UsageError{Code: "usage.delegate", Msg: "--defect names what a fail found wrong in the change's own diff: it goes only with --verdict fail"}
	}
	ev, err := p.resolveDelegateEvidence(ctx, run, evidence)
	if err != nil {
		return nil, err
	}
	v := &DelegateVerdict{
		Change: id, Task: task, Skill: run.Skill, Run: run.DetachedID, Verdict: verdict, Reason: reason, Evidence: ev, At: Stamp(),
		ChangeTip: run.ChangeTip, Defects: rels,
	}
	if err := p.saveDelegateVerdict(v); err != nil {
		return nil, err
	}
	return v, nil
}

// defectPaths is a fail's --defect values as project-relative, slash-
// separated paths, blanks and repeats dropped.
func (p *Project) defectPaths(items []string) []string {
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		abs := item
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Root, filepath.FromSlash(item))
		}
		if rel := p.Rel(filepath.Clean(abs)); !slices.Contains(out, rel) {
			out = append(out, rel)
		}
	}
	return out
}

// resolveDelegateEvidence resolves a delegate's --evidence (D78): a URL as
// given; the session's own accept-* note as it is on disk (ape commits it
// with the acceptance); any other path at change/<id>'s tip while the
// change is unarchived — where its design artefacts live, whatever branch
// HEAD is on — and in the working tree once it is archived, as a person's.
func (p *Project) resolveDelegateEvidence(ctx context.Context, run *DelegateRun, items []string) ([]Evidence, error) {
	archived := strings.HasPrefix(run.Folder, p.Rel(p.ArchiveDir())+"/")
	var out []Evidence
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		rel := item
		if !urlRe.MatchString(item) {
			abs := item
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(p.Root, filepath.FromSlash(item))
			}
			rel = p.Rel(filepath.Clean(abs))
		}
		var ev []Evidence
		var err error
		switch {
		case urlRe.MatchString(item), archived, p.delegateMayTouch(run, rel) && !strings.HasPrefix(rel, p.StateRel()+"/"):
			ev, err = p.resolveEvidence(ctx, []string{item})
		default:
			ev, err = p.resolveEvidenceAt(ctx, ChangeBranch(run.Change), []string{item})
		}
		if err != nil {
			return nil, err
		}
		out = append(out, ev...)
	}
	return out, nil
}

// envDetachedRun is the variable a detached run's supervisor sets on what
// it starts (apecmd envDetachedID).
const envDetachedRun = "APE_DETACHED_ID"

func (p *Project) saveDelegateVerdict(v *DelegateVerdict) error {
	p.ensureState()
	if err := os.MkdirAll(runlog.DelegateRoot(p.Root), 0o755); err != nil {
		return err
	}
	return writeJSON(p.DelegateVerdictPath(v.Run), v)
}

// NoVerdictReason is the reason of the fail ape records for a delegate
// session that ended without a verdict (D78).
const NoVerdictReason = "no verdict"

// DelegateOutcome is what a settled delegate session came to.
type DelegateOutcome struct {
	Verdict  string     `json:"verdict,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	// Notes are the accept-* notes the session left, committed with the
	// acceptance on a pass.
	Notes      []string `json:"notes,omitempty"`
	Violations []string `json:"violations,omitempty"`
	Voided     bool     `json:"voided,omitempty"`
	// Accepted is the acceptance ape recorded on a pass.
	Accepted *Accept `json:"accepted,omitempty"`
	// VerdictFile is the run's verdict file, project-relative.
	VerdictFile string `json:"verdict_file,omitempty"`
	// Background is the background work the session's last Stop reported
	// still running, when it ended without a verdict (D88).
	Background []string `json:"background,omitempty"`
	// Defects are a fail's --defect paths in the change's own diff (base
	// ..tip), DefectsOutside the ones that are not and so do not count
	// (D188).
	Defects        []string `json:"defects,omitempty"`
	DefectsOutside []string `json:"defects_outside,omitempty"`
	// Returned is the return ape recorded when the fail went back to the
	// change (delegate.returned); ToPerson says why a fail went to a
	// person instead (ToPerson*), and Returns are the task's earlier
	// returns when that is a second fail.
	Returned *DelegateReturn  `json:"returned,omitempty"`
	ToPerson string           `json:"to_person,omitempty"`
	Returns  []DelegateReturn `json:"returns,omitempty"`
}

// delegateMayTouch reports whether a delegate session may leave path
// (project-relative) dirty: an accept-* note in the change's assets
// folder, and ape's own ignored state.
func (p *Project) delegateMayTouch(run *DelegateRun, rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	if strings.HasPrefix(rel, p.StateRel()+"/") {
		return true
	}
	name, ok := strings.CutPrefix(rel, run.Folder+"/assets/accept-")
	return ok && name != "" && !strings.Contains(name, "/")
}

// SettleDelegateRun is the post-run assertion and, on a pass, the
// acceptance. The session is read-only by contract: one that committed,
// left HEAD elsewhere, or left a file other than an accept-* note in the
// change's assets folder has its verdict voided (recorded in the verdict
// file) — whatever it was. A pass is then recorded as AcceptDelegated
// records it; a fail, or no verdict, records nothing. background is what
// the session's last main-session Stop reported still running
// (LastStopBackground): a session with no verdict and live background
// work fails with LiveBackgroundReason rather than "no verdict" (D88).
// A fail is then settled by settleDelegateFail: back to the change, or a
// person's (D188).
func (p *Project) SettleDelegateRun(ctx context.Context, run *DelegateRun, background ...string) (*DelegateOutcome, error) {
	var violations, notes []string
	if run.Branch != "" {
		if cur := CurrentBranch(ctx, p.Root); cur != run.Branch {
			violations = append(violations, "switched HEAD to "+branchName(cur))
			if err := p.switchBranch(ctx, run.Branch); err != nil {
				return nil, err
			}
		}
	}
	if run.HeadSHA != "" && HeadSHA(ctx, p.Root) != run.HeadSHA {
		out, _ := git(ctx, p.Root, "log", "--format=%h %s", run.HeadSHA+"..HEAD")
		for line := range strings.SplitSeq(out, "\n") {
			if line != "" {
				violations = append(violations, "committed "+line)
			}
		}
		if out == "" {
			violations = append(violations, "moved HEAD from "+short(run.HeadSHA))
		}
	}
	status, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	for line := range strings.SplitSeq(status, "\n") {
		if len(line) <= 3 {
			continue
		}
		name := strings.Trim(line[3:], "\"")
		if i := strings.Index(name, " -> "); i >= 0 {
			name = name[i+4:]
		}
		switch {
		case !p.delegateMayTouch(run, name):
			violations = append(violations, "left "+name)
		case !strings.HasPrefix(name, p.StateRel()+"/"):
			notes = append(notes, name)
		}
	}
	out := &DelegateOutcome{Violations: violations, Notes: notes}
	v := p.LoadDelegateVerdict(run.DetachedID)
	if v != nil {
		started, _ := time.Parse(time.RFC3339, run.StartedAt)
		at, err := time.Parse(time.RFC3339, v.At)
		if err != nil || at.Before(started) || v.Change != run.Change || v.Task != run.Task {
			v = nil
		}
	}
	if v != nil {
		out.Verdict, out.Reason, out.Evidence = v.Verdict, v.Reason, v.Evidence
		out.VerdictFile = p.Rel(p.DelegateVerdictPath(run.DetachedID))
	}
	if len(violations) > 0 {
		if v == nil {
			v = &DelegateVerdict{Change: run.Change, Task: run.Task, Skill: run.Skill, Run: run.DetachedID, At: Stamp(), ChangeTip: run.ChangeTip}
		}
		v.Voided = violations
		if err := p.saveDelegateVerdict(v); err != nil {
			return nil, err
		}
		out.Voided = true
		return out, nil
	}
	if v == nil {
		// No verdict is a fail (D78), recorded as one: the session's last
		// message is never read for a verdict.
		reason := NoVerdictReason
		if len(background) > 0 {
			reason = LiveBackgroundReason(background)
			out.Background = background
		}
		v = &DelegateVerdict{
			Change: run.Change, Task: run.Task, Skill: run.Skill, Run: run.DetachedID, Verdict: "fail", Reason: reason,
			At: Stamp(), ChangeTip: run.ChangeTip,
		}
		if err := p.saveDelegateVerdict(v); err != nil {
			return nil, err
		}
		out.Verdict, out.Reason = v.Verdict, v.Reason
		out.VerdictFile = p.Rel(p.DelegateVerdictPath(run.DetachedID))
	}
	if v.Verdict != "pass" {
		return out, p.settleDelegateFail(ctx, run, v, out)
	}
	a, err := p.AcceptDelegated(ctx, run, v, notes)
	if err != nil {
		return out, err
	}
	out.Accepted = a
	return out, nil
}

// settleDelegateFail decides where a delegate's fail of run's task goes
// (D188). It goes back to the change — out.Returned, the caller's
// delegate.returned — when it names at least one defect in the change's
// own diff (base_sha..tip, changeTouched), the change is reviewed and not
// archived, and the task has no return since a person last answered the
// change or accepted the task (returnsSinceReset). The change goes to
// in-progress with its review cleared, so verify and review run again,
// and delegate_returns.<T> records the return, committed as "apex: <id>
// returned by delegate" where the change's state lives. Otherwise the
// task stays a person's (out.ToPerson says why; delegate.fail).
func (p *Project) settleDelegateFail(ctx context.Context, run *DelegateRun, v *DelegateVerdict, out *DelegateOutcome) error {
	if strings.HasPrefix(run.Folder, p.Rel(p.ArchiveDir())+"/") {
		out.ToPerson = ToPersonArchived
		return nil
	}
	if err := p.recoverArchive(ctx); err != nil {
		return err
	}
	ci, err := p.requireChange(ctx, run.Change)
	if fe, ok := errors.AsType[*FailError](err); ok && fe.Code == "change.archived" {
		out.ToPerson = ToPersonArchived
		return nil
	}
	if err != nil {
		return err
	}
	st := ci.State
	if len(v.Defects) > 0 {
		touched, err := p.changeTouched(ctx, run.Change, st)
		if err != nil {
			return err
		}
		for _, d := range v.Defects {
			if slices.Contains(touched, d) {
				out.Defects = append(out.Defects, d)
			} else {
				out.DefectsOutside = append(out.DefectsOutside, d)
			}
		}
	}
	if prior := returnsSinceReset(p.withBaseAccepted(ctx, run.Change, st), run.Task); len(prior) > 0 {
		out.ToPerson, out.Returns = ToPersonSecondFail, prior
		return nil
	}
	if len(out.Defects) == 0 {
		out.ToPerson = ToPersonNoDefect
		return nil
	}
	if st.Status != StatusReviewed {
		out.ToPerson = ToPersonNotReviewed
		return nil
	}
	ret := DelegateReturn{Run: v.Run, Reason: v.Reason, Defects: out.Defects, At: Stamp()}
	next := *st
	next.Status, next.Review = StatusInProgress, nil
	next.DelegateReturns = make(map[string][]DelegateReturn, len(st.DelegateReturns)+1)
	for task, rs := range st.DelegateReturns {
		next.DelegateReturns[task] = rs
	}
	next.DelegateReturns[run.Task] = append(slices.Clone(st.DelegateReturns[run.Task]), ret)
	if err := p.saveChangeState(ctx, ci, &next, "apex: "+run.Change+" returned by delegate"); err != nil {
		return err
	}
	*ci.State = next
	out.Returned = &ret
	return nil
}

// returnsSinceReset is task's delegate returns since a person last
// answered the change or accepted the task (by anyone but a delegate):
// the counter that makes a second fail a person's (D188 Q106).
func returnsSinceReset(st *State, task string) []DelegateReturn {
	if st == nil {
		return nil
	}
	reset := ""
	if st.Answer != nil {
		reset = st.Answer.At
	}
	for _, a := range st.Accepted {
		if a.Task == task && !strings.HasPrefix(a.By, DelegateByPrefix) && a.At > reset {
			reset = a.At
		}
	}
	var out []DelegateReturn
	for _, r := range st.DelegateReturns[task] {
		if reset == "" || r.At > reset {
			out = append(out, r)
		}
	}
	return out
}

// AcceptDelegated records a delegate's pass of run's task exactly as a
// person's acceptance is recorded (Accept: on the change's branch, on its
// base when reviewed off it, in the archive once archived), by
// "delegate:<skill>" with the run's id — never as a person — on the
// verdict's evidence, else the verdict file itself (ignored state, so
// recorded by its path and content's SHA-256 as it is on disk: it is on
// no branch); the session's accept-* notes are committed with the record.
func (p *Project) AcceptDelegated(ctx context.Context, run *DelegateRun, v *DelegateVerdict, notes []string) (*Accept, error) {
	ev := v.Evidence
	if len(ev) == 0 {
		var err error
		if ev, err = p.resolveEvidence(ctx, []string{p.DelegateVerdictPath(run.DetachedID)}); err != nil {
			return nil, err
		}
	}
	st, err := p.accept(ctx, run.Change, acceptance{
		task: run.Task, by: DelegateByPrefix + run.Skill, run: run.DetachedID, ev: ev, notes: notes,
	})
	if err != nil {
		return nil, err
	}
	for i := range st.Accepted {
		if st.Accepted[i].Task == run.Task {
			a := st.Accepted[i]
			return &a, nil
		}
	}
	return nil, errors.New("the acceptance of " + run.Task + " is not in the recorded state")
}

// readNotes is the content of the notes (project-relative files) to
// commit with an acceptance.
func (p *Project) readNotes(rels []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(rels))
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		out[rel] = data
	}
	return out, nil
}

// dropCommittedNotes removes the working tree's copies of notes committed
// on branch without a checkout: they are recorded there, and an untracked
// copy would stop a later switch to it.
func (p *Project) dropCommittedNotes(ctx context.Context, branch string, notes map[string][]byte) {
	if len(notes) == 0 {
		return
	}
	if here, err := p.checkedOutHere(ctx, branch); err != nil || here {
		return
	}
	for rel, data := range notes {
		abs := filepath.Join(p.Root, filepath.FromSlash(rel))
		if cur, err := os.ReadFile(abs); err == nil && bytes.Equal(cur, data) {
			_ = os.Remove(abs)
		}
	}
}

func readJSON(file string, v any) bool {
	data, err := os.ReadFile(file)
	return err == nil && json.Unmarshal(data, v) == nil
}

func writeJSON(file string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(file, string(data)+"\n")
}
