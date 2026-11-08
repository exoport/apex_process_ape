package apexv1

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The five integration modes (D876). solo is a project with no
// coordination: block; the other four are its integration and events.
// Mode is the one place the rules live: every coordinated path, `ape
// project check` and `ape status` read the mode through it.
const (
	ModeSolo        = "solo"
	ModeTrunk       = "trunk"
	ModeQueuePoll   = "queue/poll"
	ModeQueueWatch  = "queue/watch"
	ModeQueueRunner = "queue/runner"
)

// The values of coordination.integration and coordination.events.
const (
	IntegrationTrunk = "trunk"
	IntegrationQueue = "queue"
	EventsPoll       = "poll"
	EventsWatch      = "watch"
	EventsRunner     = "runner"
)

// CodeCoordIntegration: the coordination: block's integration, events or
// runner_label do not make one of the modes (D876).
const CodeCoordIntegration = "coord.integration"

// CodeTrunkOneMember: a trunk project's milestone claim by a person other
// than its owner (D876: trunk is for one person per repository), or a
// handover of its owner key refused.
const CodeTrunkOneMember = "coord.trunk-one-member"

// IntegrationModeCommand writes coordination.integration into a block from
// before 1.5 (the framework's derivable migration runs it).
const IntegrationModeCommand = "ape project integration-mode"

// runnerLabelRe is a label GitHub matches as one word, which YAML's flow
// sequence keeps whole: no comma, space or bracket.
var runnerLabelRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// defaultRunnerLabels are the labels GitHub gives every self-hosted runner
// of a kind (matched case-insensitively, as GitHub does): a job asking for
// one of them runs on any runner that has it (D871, V12-15).
var defaultRunnerLabels = []string{"self-hosted", "linux", "windows", "macos", "x64", "x86", "arm", "arm64"}

// CheckRunnerLabel refuses a label that is not the runner's own: empty, not
// one word, or one of GitHub's default labels.
func CheckRunnerLabel(label string) error {
	switch {
	case label == "":
		return fmt.Errorf("no runner_label: the queue's jobs would run on any self-hosted runner that matches")
	case !runnerLabelRe.MatchString(label):
		return fmt.Errorf("runner_label %q is not one word of letters, digits, '.', '_' or '-' (at most 64)", label)
	case slices.Contains(defaultRunnerLabels, strings.ToLower(label)):
		return fmt.Errorf("runner_label %q is one of GitHub's default runner labels (%s), which any runner carries — name the runner's own label", label, strings.Join(defaultRunnerLabels, ", "))
	}
	return nil
}

func integrationErr(format string, a ...any) error {
	return &FailError{Code: CodeCoordIntegration, Action: ActionPerson, Msg: fmt.Sprintf(format, a...) +
		" — a person fixes _apex/config.yaml's coordination: block (integration: trunk | queue; events: poll | watch | runner, queue only; runner_label, runner only)"}
}

// Mode is the block's integration mode, or why it is none (D876). A nil
// block is solo.
func (c *CoordinationConfig) Mode() (string, error) {
	if c == nil {
		return ModeSolo, nil
	}
	integration, events, label := strings.TrimSpace(c.Integration), strings.TrimSpace(c.Events), strings.TrimSpace(c.RunnerLabel)
	// A github: block names a repository only with a non-empty repo, as
	// queued() and member.Load read it (INT-1): queue needs one; trunk
	// refuses the block whatever it holds.
	ghBlock := c.GitHub != nil
	hasRepo := ghBlock && strings.TrimSpace(c.GitHub.Repo) != ""
	switch integration {
	case "":
		return "", &FailError{Code: CodeCoordIntegration, Action: ActionPerson, Msg: "the coordination: block names no integration (trunk or queue), which 1.5 requires — run `" + IntegrationModeCommand +
			"` (the framework's migration does): it writes queue where coordination.github is set, else trunk"}
	case IntegrationTrunk:
		switch {
		case ghBlock:
			return "", integrationErr("integration: trunk with coordination.github: trunk integrates by ape's local merge, and a GitHub repository is queue's")
		case events != "":
			return "", integrationErr("integration: trunk with events: %s: events are how a queue close learns its merge", events)
		case label != "":
			return "", integrationErr("integration: trunk with runner_label: a runner label is queue/runner's")
		}
		return ModeTrunk, nil
	case IntegrationQueue:
	default:
		return "", integrationErr("integration: %q is neither trunk nor queue", integration)
	}
	if !hasRepo {
		return "", integrationErr("integration: queue without coordination.github.repo: the queue is the repository's on GitHub (<owner>/<name>)")
	}
	switch events {
	case "", EventsPoll:
		if label != "" {
			return "", integrationErr("runner_label without events: runner")
		}
		return ModeQueuePoll, nil
	case EventsWatch:
		if label != "" {
			return "", integrationErr("runner_label without events: runner")
		}
		return ModeQueueWatch, nil
	case EventsRunner:
		if err := CheckRunnerLabel(label); err != nil {
			return "", integrationErr("events: runner: %v", err)
		}
		return ModeQueueRunner, nil
	}
	return "", integrationErr("events: %q is not poll, watch or runner", events)
}

// IntegrationMode is the project's mode from its config, or why it has
// none: an unreadable config is an error too.
func (p *Project) IntegrationMode() (string, error) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return "", err
	}
	return cfg.Coordination.Mode()
}

func init() {
	findingRules = append(findingRules, rule("", `^coordination integration: `, CodeCoordIntegration))
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeTrunkOneMember, 1, ActionPerson, "a trunk project (integration: trunk, D876) is for one person per repository: its first milestone claim records the claimant's NATS user as the project's owner (milestone.<project>._trunk-owner, create-only), and a claim by any other user is refused — for several people the project moves to integration: queue (queue/poll, queue/watch or queue/runner), or the owner hands it over with `ape milestone owner --to <user> --by <who>` (a person's act; refused unless the caller is the owner and holds no live milestone lease in the project). A lost owner's key is removed by an operator with the cluster's admin credential. A refusal of a mistake, not a security boundary: any member's credential may write the project's milestone keys"},
		CodeDoc{CodeIntegrationModePending, 1, ActionPerson, "`ape project integration-mode --check` (the framework's migration's check, D876): the coordination: block in _apex/config.yaml names no integration — `ape project integration-mode` writes it in one commit (queue where coordination.github is set, else trunk). Also its refusal when ape cannot write the key itself (coordination: written as a flow mapping): a person adds it by hand"},
		CodeDoc{CodeCoordIntegration, 1, ActionPerson, "the coordination: block in _apex/config.yaml does not make one of the integration modes (D876): integration missing (a block from before 1.5 — `ape project integration-mode` writes it: queue where coordination.github is set, else trunk) or neither trunk nor queue; queue without coordination.github; github:, events: or runner_label under trunk; events not poll, watch or runner; runner_label without events: runner; or events: runner without a runner_label of the runner's own (one word, not self-hosted or another of GitHub's default labels: linux, windows, macos, x64, x86, arm, arm64, compared case-insensitively) — a person fixes the block"},
	)
}

// CodeIntegrationModePending: `ape project integration-mode --check` found
// a coordination: block with no integration (the framework's migration's
// check: exit 1, not applied).
const CodeIntegrationModePending = "project.integration-mode-pending"

// IntegrationModeSubject is the commit `ape project integration-mode`
// makes.
const IntegrationModeSubject = "apex: coordination integration mode"

// IntegrationModeResult is what `ape project integration-mode` did, or
// with --check would do.
type IntegrationModeResult struct {
	// Integration is the value written (or to write): queue or trunk; ""
	// when nothing is left to write (no block, or the key is there).
	Integration string `json:"integration,omitempty"`
	// Commit is the one commit, IntegrationModeSubject; "" when nothing
	// was written.
	Commit string `json:"commit,omitempty"`
}

// integrationModePlan is what is left: the value a block without
// integration gets (queue where coordination.github is set, else trunk),
// or "" when there is no block or it has the key.
func (p *Project) integrationModePlan() (string, error) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return "", err
	}
	c := cfg.Coordination
	if c == nil || strings.TrimSpace(c.Integration) != "" {
		return "", nil
	}
	if c.GitHub != nil {
		return IntegrationQueue, nil
	}
	return IntegrationTrunk, nil
}

// IntegrationModeCheck is `ape project integration-mode --check`: nil when
// nothing is left to write, else CodeIntegrationModePending (exit 1). It
// writes nothing and needs no git.
func (p *Project) IntegrationModeCheck() (*IntegrationModeResult, error) {
	want, err := p.integrationModePlan()
	res := &IntegrationModeResult{Integration: want}
	if err != nil || want == "" {
		return res, err
	}
	return res, &FailError{Code: CodeIntegrationModePending, Action: ActionPerson, Msg: "the coordination: block names no integration: `" + IntegrationModeCommand +
		"` writes integration: " + want + " in one commit"}
}

// WriteIntegrationMode writes coordination.integration into a block that has
// none (D876): queue where coordination.github is set, else trunk, as the
// block's first key, every other byte of the file kept, in ONE commit,
// IntegrationModeSubject. Idempotent: with nothing left it writes
// nothing. Refused outside a git repository, on a change branch, and while
// the config has uncommitted changes (nothing written).
func (p *Project) WriteIntegrationMode(ctx context.Context) (*IntegrationModeResult, error) {
	want, err := p.integrationModePlan()
	res := &IntegrationModeResult{Integration: want}
	if err != nil || want == "" {
		return res, err
	}
	if !p.isWorkTree(ctx) {
		return res, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — the integration mode is one commit"}
	}
	if err := p.refuseOnChangeBranch(ctx, "the integration mode"); err != nil {
		return res, err
	}
	rel := p.Rel(ConfigPath(p.Root))
	if dirty, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", untrackedNormal, "--", rel); err != nil {
		return res, &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	} else if strings.TrimSpace(dirty) != "" {
		return res, &FailError{Code: "tree.dirty", Msg: "the integration mode is one commit of " + rel + ", which has uncommitted changes — commit or discard them first, then run it again"}
	}
	before, err := os.ReadFile(ConfigPath(p.Root))
	if err != nil {
		return res, err
	}
	out, err := insertIntegration(before, want)
	if err != nil {
		return res, &FailError{Code: CodeIntegrationModePending, Action: ActionPerson, Msg: "coordination.integration cannot be written by ape: " + err.Error() +
			" — a person adds `integration: " + want + "` to the coordination: block by hand; nothing was written"}
	}
	if err := writeFileAtomic(ConfigPath(p.Root), string(out)); err != nil {
		return res, err
	}
	sha, err := p.commitRecord(ctx, IntegrationModeSubject, "coordination.integration: "+want+" (D876: queue where coordination.github is set, else trunk).", rel)
	if err != nil {
		_ = writeFileAtomic(ConfigPath(p.Root), string(before))
		_, _ = git(ctx, p.Root, "reset", "-q", "--", rel)
		if fe, ok := errors.AsType[*FailError](err); ok {
			return res, fe
		}
		return res, &FailError{Code: CodeCommitFailed, Msg: "the integration mode failed: " + err.Error() + " — nothing was committed and the config is as it was; fix the cause and run it again"}
	}
	res.Commit = sha
	return res, nil
}

// insertIntegration is the edit of the config's text: a line
// `integration: <value>` before the coordination: block's first key, at
// its indentation. Every other byte is kept, and the result is checked by
// parsing it: the block must read as before plus the key, the rest of the
// document as before.
func insertIntegration(data []byte, value string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the config is not a mapping")
	}
	var block *yaml.Node
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "coordination" {
			block = top.Content[i+1]
		}
	}
	switch {
	case block == nil || block.Kind != yaml.MappingNode:
		return nil, errors.New("coordination: is not a mapping")
	case block.Style&yaml.FlowStyle != 0:
		return nil, errors.New("coordination: is a flow mapping ({…}), which only a person edits")
	case len(block.Content) == 0:
		return nil, errors.New("coordination: is empty")
	}
	first := block.Content[0]
	lines := strings.SplitAfter(string(data), "\n")
	n := first.Line - 1
	if n < 0 || n >= len(lines) || first.Column < 1 {
		return nil, errors.New("coordination:'s first key is not where the parser put it")
	}
	eol := "\n"
	if strings.HasSuffix(lines[n], "\r\n") {
		eol = "\r\n"
	}
	line := strings.Repeat(" ", first.Column-1) + "integration: " + value + eol
	out := []byte(strings.Join(slices.Insert(lines, n, line), ""))
	var b, a map[string]any
	if err := yaml.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(out, &a); err != nil {
		return nil, errors.New("the edited config does not parse: " + err.Error())
	}
	bc, _ := b["coordination"].(map[string]any)
	want := map[string]any{"integration": value}
	maps.Copy(want, bc)
	b["coordination"] = want
	if !reflect.DeepEqual(a, b) {
		return nil, errors.New("the edited config does not read as the block plus integration")
	}
	return out, nil
}
