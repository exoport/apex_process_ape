package apexv1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
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
		CodeDoc{CodeOrdersNotNamed, 1, ActionPerson, "the act is delegated only when the person's orders name it under acts: (every act, D664+), and these orders do not"},
		CodeDoc{CodeOrdersCondition, 1, ActionPerson, "a condition the person's orders set for this act does not hold (a tweak's type, a milestone's change count, its last gate, an answer only for a change blocked on a question): the conductor relays it to the person"},
		CodeDoc{CodeOrdersNoReason, 2, ActionStop, "`ape conductor act` needs --reason: what the act rests on, quoting the evidence the conductor read (D660)"},
		CodeDoc{CodeConductorRunning, 1, ActionStop, "`ape conductor start`: the project's orchestrator terminal already runs (one per project): `ape conductor stop` first"},
		CodeDoc{CodeConductorNotRunning, 1, ActionStop, "`ape conductor send|read|watch`: no orchestrator terminal runs (or ran) in this project: `ape conductor start`"},
		CodeDoc{"usage.message", 2, ActionStop, "`ape conductor send`: the message is empty or over 600 characters; for more, write a file in the project and name its path"},
		CodeDoc{"usage.idle-alert", 2, ActionStop, "the orchestrator terminal's idle alert (--idle-alert, conductor.idle_alert) is not a positive Go duration (e.g. 30m)"},
		CodeDoc{"usage.from", 2, ActionStop, "`ape orders accept --from <draft>`: the draft cannot be read"},
		CodeDoc{"usage.kind", 2, ActionStop, "`ape conductor note` takes exactly one of --hold, --question, --resolved <n>"},
		CodeDoc{"usage.act", 2, ActionStop, "`ape conductor act` names no act ape knows (" + strings.Join(DelegableActs(), ", ") + "; the never-delegated acts are refused by name)"},
		CodeDoc{"usage.resolves", 2, ActionStop, "`ape conductor note --resolved <n>` names no open hold or question of the person's decision log (`ape conductor log --open`)"},
		CodeDoc{"usage.person", 2, ActionStop, "`ape orders show` names no --person and git config user.email is unset: name whose orders"},
		CodeDoc{CodeOrdersEvidence, 1, ActionStop, "an --evidence path of `ape conductor act` does not exist in the project, or a milestone approve does not name the milestone's latest gate run (D660): the conductor reads the evidence first"},
	)
}

// The acts a conductor may be delegated, in the orders' own words, and the
// person act each one is.
const (
	OrderAnswer           = "answer"
	OrderQuestionAnswer   = "question-answer"
	OrderPrepareRelease   = "prepare-release"
	OrderPushBranch       = "push-branch"
	OrderOpenPR           = "open-pr"
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

// Every delegable act runs only when the orders name it under acts:
// (D664+ Q-D663-1). The tiers say how far an act may reach — tier 1 the
// everyday acts, tier 2 the weightier ones — and which are never delegated.
const (
	// TierDelegable: everyday acts (answer, tweak-approve), when named.
	TierDelegable OrderTier = 1
	// TierNamed: weightier acts, when named.
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
	OrderQuestionAnswer:   {TierDelegable, ActQuestionAnswer},
	OrderPrepareRelease:   {TierNamed, ""},
	OrderPushBranch:       {TierNamed, ""},
	OrderOpenPR:           {TierNamed, ""},
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

// actOrder is every act in the order ape lists them — the accept table,
// `ape conductor act --help`, usage.act — tier by tier: one list, so a new
// act cannot be enforced and missing from what the person approves
// (FINDING-V142-1). TestActOrder holds it to orderActs.
var actOrder = []string{
	OrderAnswer, OrderQuestionAnswer, OrderTweakApprove,
	OrderMilestoneApprove, OrderAcceptTask, OrderDecisionAccept, OrderPrepareRelease, OrderPushBranch, OrderOpenPR,
	OrderAcceptConfig, OrderAcceptRed, OrderAllowPending, OrderPush, OrderTag, OrderRelease, OrderOrders,
}

// DelegableActs are the acts orders may name, in actOrder's order.
func DelegableActs() []string {
	return slices.DeleteFunc(slices.Clone(actOrder), func(a string) bool { return orderActs[a].Tier == TierNever })
}

// NeverActs are the acts never delegated, in actOrder's order.
func NeverActs() []string {
	return slices.DeleteFunc(slices.Clone(actOrder), func(a string) bool { return orderActs[a].Tier != TierNever })
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
	TweakApprove     *TweakCondition     `json:"tweak-approve,omitempty"     yaml:"tweak-approve,omitempty"`
	MilestoneApprove *MilestoneCondition `json:"milestone-approve,omitempty" yaml:"milestone-approve,omitempty"`
	Answer           *AnswerCondition    `json:"answer,omitempty"            yaml:"answer,omitempty"`
}

// TweakCondition: approve only tweaks of these change types.
type TweakCondition struct {
	Types []string `json:"types" yaml:"types"`
}

// MilestoneCondition: approve only a milestone of at most MaxChanges
// changes, and/or whose last gate run is Gate (red or green).
//

type MilestoneCondition struct {
	MaxChanges *int   `json:"max_changes,omitempty" yaml:"max_changes"`
	Gate       string `json:"gate,omitempty"        yaml:"gate"`
}

// AnswerCondition: answer only a change blocked on a question, never one
// blocked on a limit.
//

type AnswerCondition struct {
	QuestionOnly bool `json:"question_only" yaml:"question_only"`
}

// OrderEscalate is the escalate: list.
//

type OrderEscalate struct {
	TouchesGate   bool `json:"touches-gate,omitempty"`
	ChangesConfig bool `json:"changes-config,omitempty"`
	// TokensOver: a milestone past this many tokens since its approval.
	TokensOver int `json:"tokens-over,omitempty"`
}

// The escalate: categories.
const (
	EscalateTouchesGate   = "touches-gate"
	EscalateChangesConfig = "changes-config"
	EscalateTokensOver    = "tokens-over"
)

// OrdersMark is ape's record of an acceptance or a revocation.
type OrdersMark struct {
	By     string `json:"by"               yaml:"by"`
	At     string `json:"at"               yaml:"at"`
	SHA256 string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
}

// Orders are one person's standing orders as read from the base branch.
type Orders struct {
	Person     string          `json:"person"     yaml:"person"`
	Acts       []string        `json:"acts"       yaml:"acts"`
	Conditions OrderConditions `json:"conditions" yaml:"conditions"`
	// Escalate are the categories that always go to the person, even for
	// an act the orders name (D663 2c).
	Escalate OrderEscalate `json:"escalate"           yaml:"-"`
	Accepted *OrdersMark   `json:"accepted,omitempty" yaml:"accepted,omitempty"`
	Revoked  *OrdersMark   `json:"revoked,omitempty"  yaml:"revoked,omitempty"`

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
	// typesLine is the file line of tweak-approve's types (strict errors).
	typesLine int
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

// ParseOrders reads an orders file's content, strictly (D662+): an
// unknown key, an unknown act, a never-delegated act, an act listed
// twice, a condition on an act the orders cannot use, or a malformed
// condition is orders.invalid naming the file's line. Nothing is ever
// ignored: an ignored condition would widen the conductor's authority.
func ParseOrders(data []byte) (*Orders, error) {
	fm, body, err := frontmatter.Split(data)
	if err != nil || len(fm) == 0 {
		return nil, ordersInvalid(1, "the orders have no frontmatter (--- person:, acts:, conditions: ---)")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(fm, &doc); err != nil {
		return nil, ordersInvalid(1, "the frontmatter does not parse: "+err.Error())
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, ordersInvalid(1, "the frontmatter is not a mapping of person:, acts:, conditions:")
	}
	o := &Orders{Body: string(body)}
	if err := o.parseHeader(doc.Content[0]); err != nil {
		return nil, err
	}
	if strings.TrimSpace(o.Person) == "" {
		return nil, ordersInvalid(1, "the orders name no person: (their git user.email)")
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
	return o, nil
}

// ordersInvalid is orders.invalid at a frontmatter line, given as the
// file's line (the frontmatter starts on line 2).
func ordersInvalid(fileLine int, msg string) error {
	return &FailError{Code: CodeOrdersInvalid, Msg: fmt.Sprintf("orders line %d: %s", fileLine, msg)}
}

// line is a frontmatter node's line in the file.
func line(n *yaml.Node) int { return n.Line + 1 }

// parseHeader reads the frontmatter's mapping, refusing anything it does
// not know.
func (o *Orders) parseHeader(m *yaml.Node) error {
	var conds *yaml.Node
	seen := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if seen[k.Value] {
			return ordersInvalid(line(k), k.Value+": is given twice")
		}
		seen[k.Value] = true
		switch k.Value {
		case "person":
			if v.Kind != yaml.ScalarNode {
				return ordersInvalid(line(v), "person: is one email")
			}
			o.Person = strings.TrimSpace(v.Value)
		case "acts":
			if err := o.parseActs(v); err != nil {
				return err
			}
		case "conditions":
			conds = v
		case "escalate":
			if err := o.parseEscalate(v); err != nil {
				return err
			}
		case "accepted", "revoked":
			var mk OrdersMark
			if err := decodeStrict(v, &mk); err != nil {
				return ordersInvalid(line(v), k.Value+": "+err.Error())
			}
			if k.Value == "accepted" {
				o.Accepted = &mk
			} else {
				o.Revoked = &mk
			}
		default:
			return ordersInvalid(line(k), fmt.Sprintf("%q is not a key of the orders (person, acts, conditions; ape writes accepted, revoked)", k.Value))
		}
	}
	if conds != nil {
		return o.parseConditions(conds)
	}
	return nil
}

func (o *Orders) parseActs(v *yaml.Node) error {
	if v.Kind != yaml.SequenceNode {
		return ordersInvalid(line(v), "acts: is a list, e.g. [answer, tweak-approve]")
	}
	for _, a := range v.Content {
		act := strings.TrimSpace(a.Value)
		switch tier := OrderTierOf(act); {
		case a.Kind != yaml.ScalarNode || tier == 0:
			return ordersInvalid(line(a), fmt.Sprintf("%q is not an act (acts: %s)", act, strings.Join(OrderActs(), ", ")))
		case tier == TierNever:
			return ordersInvalid(line(a), act+" is never delegated: only the person does it, so it cannot be in acts:")
		case slices.Contains(o.Acts, act):
			return ordersInvalid(line(a), act+" is listed twice")
		}
		o.Acts = append(o.Acts, act)
	}
	return nil
}

// parseEscalate reads escalate: — touches-gate, changes-config, or
// {tokens-over: N} — strictly.
func (o *Orders) parseEscalate(v *yaml.Node) error {
	if v.Kind != yaml.SequenceNode {
		return ordersInvalid(line(v), "escalate: is a list, e.g. [touches-gate, changes-config, {tokens-over: 40000000}]")
	}
	for _, it := range v.Content {
		switch {
		case it.Kind == yaml.ScalarNode && it.Value == EscalateTouchesGate:
			o.Escalate.TouchesGate = true
		case it.Kind == yaml.ScalarNode && it.Value == EscalateChangesConfig:
			o.Escalate.ChangesConfig = true
		case it.Kind == yaml.MappingNode && len(it.Content) == 2 && it.Content[0].Value == EscalateTokensOver:
			n, err := strconv.Atoi(it.Content[1].Value)
			if err != nil || n < 1 {
				return ordersInvalid(line(it), "tokens-over: is a token count of at least 1")
			}
			o.Escalate.TokensOver = n
		default:
			return ordersInvalid(line(it), fmt.Sprintf("%q is not a category escalate: knows (touches-gate, changes-config, {tokens-over: N})", it.Value))
		}
	}
	return nil
}

// parseConditions reads conditions:, each for an act the orders can use
// (tier 1, or named), each field known and well formed.
func (o *Orders) parseConditions(v *yaml.Node) error {
	if v.Kind != yaml.MappingNode {
		return ordersInvalid(line(v), "conditions: is a mapping of act → its conditions")
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, c := v.Content[i], v.Content[i+1]
		act := k.Value
		switch OrderTierOf(act) {
		case 0:
			return ordersInvalid(line(k), fmt.Sprintf("%q is not an act, so it can have no conditions", act))
		case TierNever:
			return ordersInvalid(line(k), act+" is never delegated, so it can have no conditions")
		case TierDelegable, TierNamed:
			if !o.Names(act) {
				return ordersInvalid(line(k), act+" has conditions but is not in acts:, so the orders cannot use it — name it, or drop its conditions")
			}
		}
		var err error
		switch act {
		case OrderTweakApprove:
			var t TweakCondition
			o.typesLine = line(k)
			if err = decodeStrict(c, &t); err == nil && len(t.Types) == 0 {
				err = errors.New("types: names at least one change type")
			}
			o.Conditions.TweakApprove = &t
		case OrderMilestoneApprove:
			var t MilestoneCondition
			err = decodeStrict(c, &t)
			switch {
			case err != nil:
			case t.MaxChanges != nil && *t.MaxChanges < 1:
				err = errors.New("max_changes: is a count of at least 1")
			case t.Gate != "" && t.Gate != "red" && t.Gate != "green":
				err = fmt.Errorf("gate: is red or green, not %q", t.Gate)
			case t.MaxChanges == nil && t.Gate == "":
				err = errors.New("names no condition (max_changes, gate)")
			}
			o.Conditions.MilestoneApprove = &t
		case OrderAnswer:
			var t struct {
				QuestionOnly *bool `yaml:"question_only"`
			}
			if err = decodeStrict(c, &t); err == nil && t.QuestionOnly == nil {
				err = errors.New("names no condition (question_only)")
			}
			if err == nil {
				o.Conditions.Answer = &AnswerCondition{QuestionOnly: *t.QuestionOnly}
			}
		default:
			err = errors.New("ape checks no condition for this act: put what you want of it in the text, for the conductor")
		}
		if err != nil {
			return ordersInvalid(line(k), act+": "+err.Error())
		}
	}
	return nil
}

// decodeStrict decodes a node, refusing a field v does not have.
func decodeStrict(n *yaml.Node, v any) error {
	out, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(out))
	dec.KnownFields(true)
	return dec.Decode(v)
}

// digest is the sha256 of what a person accepts.
func (o *Orders) digest() (string, error) {
	j, err := json.Marshal(struct {
		Person     string          `json:"person"`
		Acts       []string        `json:"acts"`
		Conditions OrderConditions `json:"conditions"`
		Escalate   OrderEscalate   `json:"escalate"`
		Body       string          `json:"body"`
	}{o.Person, o.Acts, o.Conditions, o.Escalate, o.Body})
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
	if err := p.checkTweakTypes(o); err != nil {
		return nil, err
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

// CodeOrdersDeclined: the person answered no at `ape orders accept --from`.
const CodeOrdersDeclined = "orders.declined"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeOrdersDeclined, 1, ActionPerson, "`ape orders accept --from <draft>`: the person did not confirm the parsed orders (y/N), so nothing was written"},
	)
}

// OrdersDraft is a draft read for `ape orders accept --from`: what ape will
// enforce, what it reads as text only, and how it differs from the orders
// in force.
type OrdersDraft struct {
	Orders  *Orders `json:"orders"`
	Current *Orders `json:"current,omitempty"`
	// Diff is the unified diff from the orders on the base branch (empty
	// for new orders).
	Diff string `json:"diff"`
	data []byte
}

// ReadOrdersDraft parses a draft strictly and diffs it against person's
// orders on the base branch. The draft must be person's (--by).
func (p *Project) ReadOrdersDraft(ctx context.Context, by string, data []byte) (*OrdersDraft, error) {
	o, err := ParseOrders(data)
	if err != nil {
		return nil, err
	}
	if PersonSlug(o.Person) != PersonSlug(by) {
		return nil, &UsageError{Code: CodeByMismatch, Msg: fmt.Sprintf("the draft's person is %s, not --by %s: only a person accepts their own orders", o.Person, by)}
	}
	base, err := p.baseBranch(ctx)
	if err != nil {
		return nil, err
	}
	o.Slug, o.Path, o.Branch = PersonSlug(by), OrdersRel(by), base
	if err := p.checkTweakTypes(o); err != nil {
		return nil, err
	}
	d := &OrdersDraft{Orders: o, data: data}
	cur, _ := p.readOnBranch(ctx, base, o.Path)
	if cur != nil {
		if c, err := ParseOrders(cur); err == nil {
			d.Current = c
		}
	}
	if cur != nil {
		d.Diff = unifiedDiff("current/"+o.Path, "draft/"+o.Path, string(cur), string(data))
	}
	return d, nil
}

// AcceptOrdersFrom writes a confirmed draft as person's orders, accepted,
// in one commit on the base branch. A person's act, from a terminal only.
func (p *Project) AcceptOrdersFrom(ctx context.Context, d *OrdersDraft, by string) (*Orders, error) {
	if err := RequirePersonFlag("ape orders accept --from"); err != nil {
		return nil, err
	}
	o := d.Orders
	mark := OrdersMark{By: strings.TrimSpace(by), At: Stamp(), SHA256: o.SHA256}
	text, err := withMarks(d.data, &mark, nil)
	if err != nil {
		return nil, err
	}
	if _, err := p.commitOnBranch(ctx, o.Branch, map[string][]byte{o.Path: text}, "apex: orders accepted by "+o.Slug); err != nil {
		return nil, err
	}
	o.Accepted, o.Revoked, o.State = &mark, nil, OrdersAccepted
	p.Emit(ctx, Event{Event: EventOrders, Action: "accepted " + o.Slug, Reason: "by " + by + " from a draft", Orders: o.Slug}, nil)
	return o, nil
}

// checkTweakTypes refuses a tweak type the project does not have.
func (p *Project) checkTweakTypes(o *Orders) error {
	c := o.Conditions.TweakApprove
	if c == nil {
		return nil
	}
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return err
	}
	known := cfg.Commit.TypeList()
	for _, t := range c.Types {
		if !slices.Contains(known, t) {
			return ordersInvalid(o.typesLine, fmt.Sprintf("tweak-approve: %q is not one of the project's change types (%s)", t, strings.Join(known, ", ")))
		}
	}
	return nil
}
