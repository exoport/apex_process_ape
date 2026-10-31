package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const danaOrders = `---
person: Dana@Example.com
# what my conductor may do
acts: [answer, tweak-approve, milestone-approve]
conditions:
  tweak-approve: {types: [fix, docs]}
  milestone-approve: {max_changes: 6}
---
Answer questions the way the brief leans; escalate anything about pricing.
`

func ordersProject(t *testing.T, orders string) *Project {
	t.Helper()
	files := baseFiles()
	if orders != "" {
		files[OrdersRel("dana@example.com")] = orders
	}
	return fixture(t, files)
}

func TestPersonSlug(t *testing.T) {
	require.Equal(t, "dana-example-com", PersonSlug("Dana@Example.com"))
	require.Equal(t, "dana-example-com", PersonSlug("dana-example-com"))
	require.Equal(t, "_apex/standing-orders/dana-example-com.md", OrdersRel("dana@example.com"))
}

func TestParseOrders(t *testing.T) {
	o, err := ParseOrders([]byte(danaOrders))
	require.NoError(t, err)
	require.Equal(t, OrdersUnaccepted, o.State, "never accepted")
	require.True(t, o.Names(OrderMilestoneApprove))
	require.Equal(t, []string{"fix", "docs"}, o.Conditions.TweakApprove.Types)
	require.Equal(t, 6, *o.Conditions.MilestoneApprove.MaxChanges)
	require.Contains(t, o.Body, "escalate anything about pricing")

	for name, bad := range map[string]string{
		"no frontmatter": "just text\n",
		"no person":      "---\nacts: [answer]\n---\n",
		"unknown act":    "---\nperson: d@x\nacts: [deploy]\n---\n",
	} {
		_, err := ParseOrders([]byte(bad))
		require.ErrorContains(t, err, "", name)
		require.Equal(t, CodeOrdersInvalid, Diagnostics(err)[0].Code, name)
	}
}

func TestOrderTiers(t *testing.T) {
	require.Equal(t, TierDelegable, OrderTierOf(OrderAnswer))
	require.Equal(t, TierDelegable, OrderTierOf(OrderTweakApprove))
	require.Equal(t, TierNamed, OrderTierOf(OrderMilestoneApprove))
	require.Equal(t, TierNamed, OrderTierOf(OrderAcceptTask))
	require.Equal(t, TierNamed, OrderTierOf(OrderDecisionAccept))
	for _, a := range []string{OrderAcceptConfig, OrderAcceptRed, OrderAllowPending, OrderPush, OrderTag, OrderRelease, OrderOrders} {
		require.Equal(t, TierNever, OrderTierOf(a), a)
	}
	require.Zero(t, OrderTierOf("deploy"))
}

// A1/A3: accepted only by a person, at a terminal, on the base branch; any
// edit after that voids the orders; a revocation counts at once, from a
// change branch too; the person's comments survive ape's marks.
func TestOrdersAcceptEditRevoke(t *testing.T) {
	p := ordersProject(t, danaOrders)
	o, err := p.LoadOrders(ctx(), "dana@example.com")
	require.NoError(t, err)
	require.Equal(t, OrdersUnaccepted, o.State)

	_, err = p.AcceptOrders(ctx(), "dana@example.com", "someone@else.org")
	require.Equal(t, CodeByMismatch, Diagnostics(err)[0].Code, "only the orders' person accepts them")

	inApeSession(t)
	_, err = p.AcceptOrders(ctx(), "dana@example.com", "dana@example.com")
	require.Equal(t, CodePersonOnly, Diagnostics(err)[0].Code, "never from a model session")
	asClaudeCode(t)
	_, err = p.AcceptOrders(ctx(), "dana@example.com", "dana@example.com")
	require.Equal(t, CodePersonOnly, Diagnostics(err)[0].Code, "no grant from the person's words covers it either")
	asPerson(t)

	o, err = p.AcceptOrders(ctx(), "dana@example.com", "dana@example.com")
	require.NoError(t, err)
	require.Equal(t, OrdersAccepted, o.State)
	o, err = p.LoadOrders(ctx(), "dana@example.com")
	require.NoError(t, err)
	require.Equal(t, OrdersAccepted, o.State, "read back from the base branch")
	data, err := os.ReadFile(filepath.Join(p.Root, OrdersRel("dana@example.com")))
	require.NoError(t, err)
	require.Contains(t, string(data), "# what my conductor may do", "the person's comment survives")
	require.Contains(t, string(data), "accepted:")
	require.Equal(t, "apex: orders accepted by dana-example-com", run(t, p.Root, "log", "-1", "--format=%s"))
	evs := p.EventsSince(0)
	require.NotEmpty(t, evs)
	require.Equal(t, EventOrders, evs[len(evs)-1].Event, "the person is notified of an acceptance (D661+)")
	require.Equal(t, "accepted dana-example-com", evs[len(evs)-1].Action)

	// An edit after the acceptance, committed by anyone, voids them.
	widened := strings.Replace(string(data), "acts: [answer, tweak-approve, milestone-approve]", "acts: [answer, tweak-approve, milestone-approve, accept-task]", 1)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, OrdersRel("dana@example.com")), []byte(widened), 0o644))
	run(t, p.Root, "commit", "-qam", "widen my orders")
	o, err = p.LoadOrders(ctx(), "dana@example.com")
	require.NoError(t, err)
	require.Equal(t, OrdersUnaccepted, o.State, "changed since accepted")
	o, err = p.AcceptOrders(ctx(), "dana@example.com", "Dana@Example.com")
	require.NoError(t, err)
	require.Equal(t, OrdersAccepted, o.State)

	// Revoked from a change branch: it lands on the base branch, and counts.
	run(t, p.Root, "switch", "-q", "-c", ChangeBranch("add-thing"))
	require.NoError(t, os.MkdirAll(filepath.Dir(p.StateFile("add-thing")), 0o755))
	require.NoError(t, SaveState(p.StateFile("add-thing"), &State{ID: "add-thing", Status: StatusInProgress, BaseBranch: "main"}))
	o, err = p.RevokeOrders(ctx(), "dana@example.com", "dana@example.com")
	require.NoError(t, err)
	require.Equal(t, OrdersRevoked, o.State)
	o, err = p.LoadOrders(ctx(), "dana@example.com")
	require.NoError(t, err)
	require.Equal(t, OrdersRevoked, o.State, "read from main, whatever HEAD is")
	require.Equal(t, ChangeBranch("add-thing"), CurrentBranch(ctx(), p.Root), "HEAD did not move")
}

func TestLoadOrders_MissingAndMisnamed(t *testing.T) {
	p := ordersProject(t, "")
	_, err := p.LoadOrders(ctx(), "dana@example.com")
	require.Equal(t, CodeOrdersMissing, Diagnostics(err)[0].Code)

	p = ordersProject(t, strings.Replace(danaOrders, "person: Dana@Example.com", "person: eve@example.com", 1))
	_, err = p.LoadOrders(ctx(), "dana@example.com")
	require.Equal(t, CodeOrdersInvalid, Diagnostics(err)[0].Code, "a file under one person's name with another's orders")
}

// A1: a model edit of the orders is refused before it lands (the digest
// voids any that gets past, by a script).
func TestPreTool_StandingOrdersArePersons(t *testing.T) {
	p := ordersProject(t, danaOrders)
	for _, path := range []string{OrdersRel("dana@example.com"), "_apex/standing-orders/new.md", filepath.Join(p.Root, OrdersRel("dana@example.com"))} {
		d := PreTool(editPayload(t, p.Root, "Write", path))
		require.True(t, d.Deny, path)
		require.Contains(t, d.Reason, "ape orders accept")
	}
	require.False(t, PreTool(editPayload(t, p.Root, "Write", "_apex/standing.md")).Deny)
}

// D662+: the header parse is strict — every refusal names the file line,
// and nothing is ignored.
func TestParseOrders_Strict(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"unknown key":          {"---\nperson: d@x\nacts: [answer]\nmax_acts: 3\n---\n", "orders line 4: \"max_acts\" is not a key"},
		"unknown act":          {"---\nperson: d@x\nacts:\n  - answer\n  - deploy\n---\n", "orders line 5: \"deploy\" is not an act"},
		"never delegated":      {"---\nperson: d@x\nacts: [answer, accept-red]\n---\n", "orders line 3: accept-red is never delegated"},
		"twice":                {"---\nperson: d@x\nacts: [answer, answer]\n---\n", "orders line 3: answer is listed twice"},
		"tier 1 not named":     {"---\nperson: d@x\nacts: [answer]\nconditions:\n  tweak-approve: {types: [fix]}\n---\n", "orders line 5: tweak-approve has conditions but is not in acts:"},
		"condition not named":  {"---\nperson: d@x\nacts: [answer]\nconditions:\n  milestone-approve: {max_changes: 3}\n---\n", "orders line 5: milestone-approve has conditions but is not in acts:"},
		"condition never":      {"---\nperson: d@x\nacts: [answer]\nconditions:\n  push: {when: never}\n---\n", "orders line 5: push is never delegated"},
		"unknown field":        {"---\nperson: d@x\nacts: [answer]\nconditions:\n  answer: {question_only: true, polite: true}\n---\n", "orders line 5: answer:"},
		"malformed max":        {"---\nperson: d@x\nacts: [milestone-approve]\nconditions:\n  milestone-approve: {max_changes: six}\n---\n", "orders line 5: milestone-approve:"},
		"bad gate":             {"---\nperson: d@x\nacts: [milestone-approve]\nconditions:\n  milestone-approve: {gate: amber}\n---\n", `gate: is red or green, not "amber"`},
		"empty types":          {"---\nperson: d@x\nacts: [tweak-approve]\nconditions:\n  tweak-approve: {types: []}\n---\n", "types: names at least one"},
		"no condition for act": {"---\nperson: d@x\nacts: [decision-accept]\nconditions:\n  decision-accept: {only: ADR}\n---\n", "ape checks no condition for this act"},
	} {
		_, err := ParseOrders([]byte(tc.doc))
		require.Error(t, err, name)
		require.Equal(t, CodeOrdersInvalid, Diagnostics(err)[0].Code, name)
		require.ErrorContains(t, err, tc.want, name)
	}
	_, err := ParseOrders([]byte(danaOrders))
	require.NoError(t, err, "the well-formed example parses")
}

// D662+: a tweak type the project does not have is refused at load, with
// its line.
func TestLoadOrders_TweakTypesAreTheProjects(t *testing.T) {
	p := ordersProject(t, strings.Replace(danaOrders, "types: [fix, docs]", "types: [fix, hotfix]", 1))
	_, err := p.LoadOrders(ctx(), "dana@example.com")
	require.Equal(t, CodeOrdersInvalid, Diagnostics(err)[0].Code)
	require.ErrorContains(t, err, `orders line 6: tweak-approve: "hotfix" is not one of the project's change types`)
}
