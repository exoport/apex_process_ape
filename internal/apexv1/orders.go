package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
)

// Standing orders (v1.4.2, D659 A1–A4): a person's delegation to their
// conductor, as text in a committed file per person,
// _apex/standing-orders/<person>.md. Its frontmatter is what ape checks —
// who, which acts, which conditions — and its body what the conductor
// reads. ape keeps the orders' state in the same frontmatter, on the base
// branch: `accepted` (by, at and the sha256 of what was accepted) and
// `revoked`. Every delegated act reads the file at the base branch's tip,
// so a revocation counts at once wherever the project's HEAD is, and any
// edit after the acceptance — the person's own included — voids the
// orders until a person accepts them again (`ape orders accept`, from a
// terminal).

// EventOrders: a person accepted or revoked their standing orders
// (v1.4.2, D661+): action "accepted <person>" or "revoked <person>". The
// notifier runs on it as on a delegated act.
const EventOrders = "orders"

// OrdersDirRel is the standing orders' folder, project-relative.
const OrdersDirRel = "_apex/standing-orders"

// The orders codes.
const (
	CodeOrdersMissing      = "orders.missing"
	CodeOrdersInvalid      = "orders.invalid"
	CodeOrdersUnaccepted   = "orders.unaccepted"
	CodeOrdersRevoked      = "orders.revoked"
	CodeOrdersNonDelegable = "orders.non-delegable"
	CodeOrdersNotNamed     = "orders.not-named"
	CodeOrdersCondition    = "orders.condition"
	CodeOrdersNoReason     = "orders.no-reason"
	CodeOrdersEvidence     = "orders.evidence-missing"
	// CodeConductorRunning and CodeConductorNotRunning are the
	// orchestrator terminal's (ape conductor start|send|read|watch).
	CodeConductorRunning    = "conductor.running"
	CodeConductorNotRunning = "conductor.not-running"
)

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeOrdersMissing, 1, ActionPerson, "`ape conductor act` (v1.4.2): the person named has no standing orders in this project (_apex/standing-orders/<person>.md on the base branch), so their conductor acts on nothing here — it relays to the person instead"},
		CodeDoc{CodeOrdersInvalid, 1, ActionPerson, "a standing orders file's frontmatter does not parse, names an act ape does not know, or its person does not match its file name: a person fixes it and accepts it again (`ape orders accept`)"},
		CodeDoc{CodeOrdersUnaccepted, 1, ActionPerson, "the standing orders changed since a person last accepted them, or were never accepted: no delegated act runs under them until the person runs `ape orders accept --by <person>` from a terminal"},
		CodeDoc{CodeOrdersRevoked, 1, ActionPerson, "the person revoked their standing orders (`ape orders revoke`): no delegated act runs under them until they accept them again"},
		CodeDoc{CodeOrdersNonDelegable, 1, ActionPerson, "the act is never delegated (accept the config, accept red, allow a pending human-review task, push, tag, release, the orders themselves): only the person does it, from a terminal or in their own words"},
		CodeDoc{CodeOrdersNotNamed, 1, ActionPerson, "the act is delegated only when the person's orders name it under acts: (approve a milestone, accept a human-review task, accept a decision), and these orders do not"},
		CodeDoc{CodeOrdersCondition, 1, ActionPerson, "a condition the person's orders set for this act does not hold (a tweak's type, a milestone's change count, its last gate, an answer only for a change blocked on a question): the conductor relays it to the person"},
		CodeDoc{CodeOrdersNoReason, 2, ActionStop, "`ape conductor act` needs --reason: what the act rests on, quoting the evidence the conductor read (D660)"},
		CodeDoc{CodeConductorRunning, 1, ActionStop, "`ape conductor start`: the project's orchestrator terminal already runs (one per project): `ape conductor stop` first"},
		CodeDoc{CodeConductorNotRunning, 1, ActionStop, "`ape conductor send|read|watch`: no orchestrator terminal runs (or ran) in this project: `ape conductor start`"},
		CodeDoc{"usage.message", 2, ActionStop, "`ape conductor send`: the message is empty or over 600 characters; for more, write a file in the project and name its path"},
		CodeDoc{"usage.idle-alert", 2, ActionStop, "the orchestrator terminal's idle alert (--idle-alert, conductor.idle_alert) is not a positive Go duration (e.g. 30m)"},
		CodeDoc{"usage.kind", 2, ActionStop, "`ape conductor note` takes exactly one of --hold, --question, --resolved <n>"},
		CodeDoc{"usage.act", 2, ActionStop, "`ape conductor act` names no act ape knows (answer, tweak-approve, milestone-approve, accept-task, decision-accept; the never-delegated acts are refused by name)"},
		CodeDoc{"usage.resolves", 2, ActionStop, "`ape conductor note --resolved <n>` names no open hold or question of the person's decision log (`ape conductor log --open`)"},
		CodeDoc{"usage.person", 2, ActionStop, "`ape orders show` names no --person and git config user.email is unset: name whose orders"},
		CodeDoc{CodeOrdersEvidence, 1, ActionStop, "an --evidence path of `ape conductor act` does not exist in the project, or a milestone approve does not name the milestone's latest gate run (D660): the conductor reads the evidence first"},
	)
}

// The acts a conductor may be delegated, in the orders' own words, and the
// person act each one is.
const (
	OrderAnswer           = "answer"
	OrderTweakApprove     = "tweak-approve"
	OrderMilestoneApprove = "milestone-approve"
	OrderAcceptTask       = "accept-task"
	OrderDecisionAccept   = "decision-accept"
	OrderAcceptConfig     = "accept-config"
	OrderAcceptRed        = "accept-red"
	OrderAllowPending     = "allow-pending-human"
	OrderPush             = "push"
	OrderTag              = "tag"
	OrderRelease          = "release"
	OrderOrders           = "orders"
)

// OrderTier is how far an act can be delegated (D659 A2).
type OrderTier int

const (
	// TierDelegable acts run under any accepted orders.
	TierDelegable OrderTier = 1
	// TierNamed acts run only when the orders name them under acts:.
	TierNamed OrderTier = 2
	// TierNever acts are never delegated.
	TierNever OrderTier = 3
)

// orderActs maps each act to its tier and person act ("" for the acts ape
// never runs for anyone: push, tag, release, the orders).
var orderActs = map[string]struct {
	Tier OrderTier
	Act  string
}{
	OrderAnswer:           {TierDelegable, ActAnswer},
	OrderTweakApprove:     {TierDelegable, ActTweakApprove},
	OrderMilestoneApprove: {TierNamed, ActMilestoneApprove},
	OrderAcceptTask:       {TierNamed, ActAcceptTask},
	OrderDecisionAccept:   {TierNamed, ActDecisionAccept},
	OrderAcceptConfig:     {TierNever, ActAcceptConfig},
	OrderAcceptRed:        {TierNever, ActAcceptRed},
	OrderAllowPending:     {TierNever, ActAllowPendingHuman},
	OrderPush:             {TierNever, ""},
	OrderTag:              {TierNever, ""},
	OrderRelease:          {TierNever, ""},
	OrderOrders:           {TierNever, ""},
}

// OrderActs are the act names, sorted.
func OrderActs() []string {
	out := make([]string, 0, len(orderActs))
	for a := range orderActs {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// OrderTierOf is an act's tier, 0 for an unknown act.
func OrderTierOf(act string) OrderTier { return orderActs[act].Tier }

// OrderConditions are the conditions ape checks (D659 A4, D661 Q3).
//

type OrderConditions struct {
	TweakApprove *struct {
		Types []string `json:"types" yaml:"types"`
	} `json:"tweak-approve,omitempty" yaml:"tweak-approve,omitempty"`
	MilestoneApprove *struct {
		MaxChanges *int `json:"max_changes,omitempty" yaml:"max_changes"`
		// Gate is "red" or "green": the milestone's last recorded gate run.
		Gate string `json:"gate,omitempty" yaml:"gate"`
	} `json:"milestone-approve,omitempty" yaml:"milestone-approve,omitempty"`
	Answer *struct {
		// QuestionOnly: answer only a change blocked on a question, never
		// one blocked on a limit.
		QuestionOnly bool `json:"question_only" yaml:"question_only"`
	} `json:"answer,omitempty" yaml:"answer,omitempty"`
}

// OrdersMark is ape's record of an acceptance or a revocation.
type OrdersMark struct {
	By     string `json:"by"               yaml:"by"`
	At     string `json:"at"               yaml:"at"`
	SHA256 string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
}

// Orders are one person's standing orders as read from the base branch.
type Orders struct {
	Person     string          `json:"person"             yaml:"person"`
	Acts       []string        `json:"acts"               yaml:"acts"`
	Conditions OrderConditions `json:"conditions"         yaml:"conditions"`
	Accepted   *OrdersMark     `json:"accepted,omitempty" yaml:"accepted,omitempty"`
	Revoked    *OrdersMark     `json:"revoked,omitempty"  yaml:"revoked,omitempty"`

	// Read from the file, not its frontmatter.
	Slug   string `json:"slug"   yaml:"-"`
	Path   string `json:"path"   yaml:"-"`
	Branch string `json:"branch" yaml:"-"`
	Body   string `json:"body"   yaml:"-"`
	// SHA256 is the digest of what a person accepts: person, acts,
	// conditions and the body (ape's own marks left out).
	SHA256 string `json:"sha256" yaml:"-"`
	// State: accepted, unaccepted (never accepted, or changed since) or
	// revoked.
	State string `json:"state" yaml:"-"`
}

// The orders' states.
const (
	OrdersAccepted   = "accepted"
	OrdersUnaccepted = "unaccepted"
	OrdersRevoked    = "revoked"
)

var personSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// PersonSlug is a person's file name in the orders folder: their git
// user.email (D661 Q1), lowercased, every run of other characters a dash.
// A name already in slug form is returned as it is.
func PersonSlug(person string) string {
	return strings.Trim(personSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(person)), "-"), "-")
}

// OrdersRel is a person's orders file, project-relative.
func OrdersRel(person string) string { return path.Join(OrdersDirRel, PersonSlug(person)+".md") }

// ParseOrders reads an orders file's content.
func ParseOrders(data []byte) (*Orders, error) {
	fm, body, err := frontmatter.Split(data)
	if err != nil || len(fm) == 0 {
		return nil, &FailError{Code: CodeOrdersInvalid, Msg: "the orders have no frontmatter (--- person:, acts:, conditions: ---)"}
	}
	var o Orders
	if err := yaml.Unmarshal(fm, &o); err != nil {
		return nil, &FailError{Code: CodeOrdersInvalid, Msg: "the orders' frontmatter does not parse: " + err.Error()}
	}
	o.Body = string(body)
	if strings.TrimSpace(o.Person) == "" {
		return nil, &FailError{Code: CodeOrdersInvalid, Msg: "the orders name no person: (their git user.email)"}
	}
	for _, a := range o.Acts {
		if OrderTierOf(a) == 0 {
			return nil, &FailError{Code: CodeOrdersInvalid, Msg: fmt.Sprintf("the orders name an act ape does not know: %q (acts: %s)", a, strings.Join(OrderActs(), ", "))}
		}
	}
	sum, err := o.digest()
	if err != nil {
		return nil, err
	}
	o.SHA256 = sum
	switch {
	case o.Revoked != nil:
		o.State = OrdersRevoked
	case o.Accepted != nil && o.Accepted.SHA256 == o.SHA256:
		o.State = OrdersAccepted
	default:
		o.State = OrdersUnaccepted
	}
	return &o, nil
}

// digest is the sha256 of what a person accepts.
func (o *Orders) digest() (string, error) {
	j, err := json.Marshal(struct {
		Person     string          `json:"person"`
		Acts       []string        `json:"acts"`
		Conditions OrderConditions `json:"conditions"`
		Body       string          `json:"body"`
	}{o.Person, o.Acts, o.Conditions, o.Body})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(j)
	return hex.EncodeToString(h[:]), nil
}

// Names reports whether the orders name act under acts:.
func (o *Orders) Names(act string) bool { return slices.Contains(o.Acts, act) }

// LoadOrders reads person's orders from the base branch's tip.
func (p *Project) LoadOrders(ctx context.Context, person string) (*Orders, error) {
	base, err := p.baseBranch(ctx)
	if err != nil {
		return nil, err
	}
	rel := OrdersRel(person)
	data, ok := p.readOnBranch(ctx, base, rel)
	if !ok {
		return nil, &FailError{Code: CodeOrdersMissing, Msg: fmt.Sprintf("%s has no standing orders here (%s on %s)", person, rel, base)}
	}
	o, err := ParseOrders(data)
	if err != nil {
		return nil, err
	}
	o.Slug, o.Path, o.Branch = PersonSlug(person), rel, base
	if PersonSlug(o.Person) != o.Slug {
		return nil, &FailError{Code: CodeOrdersInvalid, Msg: fmt.Sprintf("%s names person %q, whose file would be %s", rel, o.Person, OrdersRel(o.Person))}
	}
	return o, nil
}

// AcceptOrders is the person accepting their orders as they are on the
// base branch: ape writes `accepted` (by, at, the digest) and drops a
// revocation, in one commit there. A person's act, from a terminal only:
// no grant from a model session ever covers it (D659 A2: the orders
// themselves are never delegated).
func (p *Project) AcceptOrders(ctx context.Context, person, by string) (*Orders, error) {
	return p.markOrders(ctx, person, by, "ape orders accept", func(o *Orders, m OrdersMark) {
		m.SHA256 = o.SHA256
		o.Accepted, o.Revoked = &m, nil
	}, "accepted")
}

// RevokeOrders is the person revoking their orders: ape writes `revoked`
// on the base branch. It counts at once: every delegated act reads the
// base branch's tip.
func (p *Project) RevokeOrders(ctx context.Context, person, by string) (*Orders, error) {
	return p.markOrders(ctx, person, by, "ape orders revoke", func(o *Orders, m OrdersMark) {
		o.Revoked = &m
	}, "revoked")
}

func (p *Project) markOrders(ctx context.Context, person, by, cmd string, mark func(*Orders, OrdersMark), verb string) (*Orders, error) {
	if err := RequirePersonFlag(cmd); err != nil {
		return nil, err
	}
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names the person whose orders these are"}
	}
	o, err := p.LoadOrders(ctx, person)
	if err != nil {
		return nil, err
	}
	if PersonSlug(by) != o.Slug {
		return nil, &UsageError{Code: CodeByMismatch, Msg: fmt.Sprintf("--by %q is not the orders' person (%s): only a person accepts or revokes their own orders", by, o.Person)}
	}
	var out *Orders
	_, err = p.editOnBranch(ctx, o.Branch, "apex: orders "+verb+" by "+o.Slug, []string{o.Path}, func(cur map[string][]byte) (map[string][]byte, error) {
		data := cur[o.Path]
		if data == nil {
			return nil, &FailError{Code: CodeOrdersMissing, Msg: o.Path + " is gone from " + o.Branch}
		}
		now, err := ParseOrders(data)
		if err != nil {
			return nil, err
		}
		mark(now, OrdersMark{By: by, At: Stamp()})
		text, err := withMarks(data, now.Accepted, now.Revoked)
		if err != nil {
			return nil, err
		}
		if r, err := ParseOrders(text); err == nil {
			now.State = r.State
		}
		now.Slug, now.Path, now.Branch = o.Slug, o.Path, o.Branch
		out = now
		return map[string][]byte{o.Path: text}, nil
	})
	if err != nil {
		return nil, err
	}
	// Notified at once (D661+): an acceptance a script forged on the SEC-1
	// route still reaches the person through their notify: channel.
	p.Emit(ctx, Event{Event: EventOrders, Action: verb + " " + o.Slug, Reason: "by " + by, Orders: o.Slug}, nil)
	return out, nil
}

// withMarks rewrites ape's two keys, accepted and revoked, in an orders
// file's frontmatter, keeping the person's own keys, order and comments,
// and the body byte for byte.
func withMarks(data []byte, accepted, revoked *OrdersMark) ([]byte, error) {
	fm, body, err := frontmatter.Split(data)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(fm, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, &FailError{Code: CodeOrdersInvalid, Msg: "the orders' frontmatter is not a mapping"}
	}
	m := doc.Content[0]
	for _, kv := range []struct {
		key  string
		mark *OrdersMark
	}{{"accepted", accepted}, {"revoked", revoked}} {
		at := -1
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == kv.key {
				at = i
			}
		}
		if kv.mark == nil {
			if at >= 0 {
				m.Content = append(m.Content[:at], m.Content[at+2:]...)
			}
			continue
		}
		var v yaml.Node
		if err := v.Encode(kv.mark); err != nil {
			return nil, err
		}
		if at >= 0 {
			m.Content[at+1] = &v
		} else {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv.key}, &v)
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(out) + "---\n" + string(body)), nil
}

// GitUserEmail is `git config user.email` in the project, "" when unset:
// whose orders a command means when it names none (D661 Q1).
func GitUserEmail(ctx context.Context, root string) string {
	email, _ := git(ctx, root, "config", "user.email")
	return strings.TrimSpace(email)
}
