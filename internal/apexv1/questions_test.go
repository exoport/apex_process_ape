package apexv1

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/runlog"
)

// D663 2a: any session asks; the open question is a person action and an
// event; only a person answers (E2 by "answer Q<n>: …"), once.
func TestQuestions(t *testing.T) {
	p := e2Project(t)
	inApeSession(t)
	q, err := p.AskQuestion(ctx(), Question{Text: "EUR or the customer's currency?", Milestone: "M1", Options: []string{"EUR", "customer's"}}, nil)
	require.NoError(t, err, "an orchestrator may ask")
	require.Equal(t, "Q1", q.ID)
	evs := p.EventsSince(0)
	require.Equal(t, EventQuestion, evs[len(evs)-1].Event)
	require.Equal(t, "answer Q1: <answer>", evs[len(evs)-1].Action)

	r := p.Status()
	acts := p.PersonActionsOf(ctx(), r)
	var found bool
	for _, a := range acts {
		found = found || (a.Kind == ActQuestionAnswer && a.Ref == "Q1")
	}
	require.True(t, found, "an open question is a person action")
	require.Len(t, r.Questions, 1, "ape status lists it (D666+ F3)")
	require.Equal(t, "Q1", r.Questions[0].ID)

	_, err = p.AnswerQuestion(ctx(), "Q1", "EUR", "x")
	require.Equal(t, CodePersonOnly, Diagnostics(err)[0].Code, "a model session does not answer")

	// The person's own words in their Claude Code session grant it.
	asClaudeCode(t)
	c := PersonCall{Act: ActQuestionAnswer, Target: "Q1", Text: "EUR", ArgvSHA256: ArgvSHA256([]string{"question", "answer", "Q1", "--text=EUR"}), Line: "ape question answer Q1 --text EUR", Literal: true}
	payload := e2Turn(t, p, "s1", "p1", "answer Q1: EUR", c.Line)
	require.False(t, PersonActGate(ctx(), payload, []PersonCall{c}).Deny, "the typed phrase grants it")
	asPerson(t)
	got, err := p.AnswerQuestion(ctx(), "Q1", "EUR", "Ann Person")
	require.NoError(t, err)
	require.Equal(t, "EUR", got.Answer.Text)
	_, err = p.AnswerQuestion(ctx(), "Q1", "GBP", "Ann Person")
	require.Equal(t, CodeQuestionAnswered, Diagnostics(err)[0].Code)
	_, err = p.AnswerQuestion(ctx(), "Q9", "x", "Ann Person")
	require.Equal(t, CodeQuestionNotFound, Diagnostics(err)[0].Code)
	require.Empty(t, p.OpenQuestions())
	require.Empty(t, p.Status().Questions, "answered: no longer listed")
}

// D663: the conductor answers a question under orders naming it; the
// escalate: list sends an act in its categories to the person; the new
// acts' checks (prepare-release's changelog; push-branch's refusals).
func TestConductorAct_D663(t *testing.T) {
	p := e2Project(t)
	addChange(t, p, "fix-hug", strings.Replace(tweakDoc("fix-hug"), "type: feat", "type: fix", 1))
	tasks := "- T1 add wave — check: ./wave.sh\n- T5 Set `regression: go test ./...` in `_apex/config.yaml` — check: grep -qx 'regression: go test ./\\\\.\\\\.\\\\.' _apex/config.yaml\n"
	addChange(t, p, "fix-config", strings.Replace(strings.Replace(changeDoc("fix-config", "greeting", addedWave, tasks), "milestone: M1", "milestone: none", 1), "type: feat", "type: fix", 1))
	orders := "---\nperson: dana@example.com\nacts: [question-answer, tweak-approve, prepare-release, push-branch, open-pr]\nescalate: [changes-config]\n---\n"
	write(t, p, OrdersRel("dana@example.com"), orders)
	write(t, p, "CHANGELOG.md", "# CHANGELOG\n\n## v1.2.0 (unreleased)\n")
	commitAll(t, p, "orders, changes, changelog")
	_, err := p.AcceptOrders(ctx(), "dana@example.com", "dana@example.com")
	require.NoError(t, err)
	q, err := p.AskQuestion(ctx(), Question{Text: "which greeting?", Change: "fix-hug"}, nil)
	require.NoError(t, err)
	asClaudeCode(t)

	res, err := p.RunDelegated(ctx(), act(OrderQuestionAnswer, q.ID, func(d *DelegatedAct) { d.Text = "hello" }), nil)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(p.question(q.ID).Answer.By, "conductor under orders dana-example-com@"), res.By)

	_, err = p.RunDelegated(ctx(), act(OrderTweakApprove, "fix-config"), nil)
	require.Equal(t, CodeOrdersEscalate, codeOf(t, err), "a change editing the config goes to the person")
	require.ErrorContains(t, err, "escalate: changes-config")

	_, err = p.RunDelegated(ctx(), act(OrderPrepareRelease, "v9.9.9"), nil)
	require.Equal(t, CodeOrdersRefused, codeOf(t, err), "a version the changelog does not name")
	res, err = p.RunDelegated(ctx(), act(OrderPrepareRelease, "v1.2.0"), nil)
	require.NoError(t, err)
	require.Equal(t, []string{
		"git push --no-follow-tags origin refs/heads/main:refs/heads/main",
		`git tag -a v1.2.0 -m "v1.2.0"`, "git push origin refs/tags/v1.2.0",
	}, res.Next, "the pushes and the tag stay the person's: the base branch first, alone (D666+ F4)")

	for _, b := range []string{"main", "master", "refs/tags/v1.2.0", "no-such-branch"} {
		_, err = p.RunDelegated(ctx(), act(OrderPushBranch, b), nil)
		require.Equal(t, CodeOrdersRefused, codeOf(t, err), b)
	}
	// A work branch goes to a bare remote, and no tag goes with it.
	remote := t.TempDir()
	require.NoError(t, exec.Command("git", "init", "-q", "--bare", remote).Run())
	run(t, p.Root, "remote", "add", "origin", remote)
	run(t, p.Root, "tag", "-a", "v0.0.1", "-m", "an unpushed tag")
	run(t, p.Root, "branch", "work/greeting")
	res, err = p.RunDelegated(ctx(), act(OrderPushBranch, "work/greeting"), nil)
	require.NoError(t, err)
	require.Contains(t, res.Next[0], "--no-follow-tags")
	out, err := exec.Command("git", "-C", remote, "for-each-ref", "--format=%(refname)").Output()
	require.NoError(t, err)
	require.Equal(t, "refs/heads/work/greeting", strings.TrimSpace(string(out)), "the branch and nothing else")

	t.Setenv("PATH", filepath.Dir(mustLook(t, "git")))
	_, err = p.RunDelegated(ctx(), act(OrderOpenPR, "work/greeting", func(d *DelegatedAct) { d.Text = "greeting" }), nil)
	require.Equal(t, CodeOrdersRefused, codeOf(t, err), "no gh on PATH")
	require.ErrorContains(t, err, remote)
}

func mustLook(t *testing.T, bin string) string {
	t.Helper()
	path, err := exec.LookPath(bin)
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "gh")); err == nil {
		t.Skip("gh sits beside git on this machine; the no-gh case cannot be made by PATH")
	}
	return path
}

// D663 2c tokens-over: a milestone past the orders' token budget goes to
// the person, read from its runs' manifests (D1 tokens_total).
func TestConductorAct_EscalateTokensOver(t *testing.T) {
	p := e2Project(t)
	write(t, p, OrdersRel("dana@example.com"), "---\nperson: dana@example.com\nacts: [milestone-approve]\nescalate: [{tokens-over: 1000}]\n---\n")
	commitAll(t, p, "orders")
	_, err := p.AcceptOrders(ctx(), "dana@example.com", "dana@example.com")
	require.NoError(t, err)
	dir := filepath.Join(runlog.TasksRoot(p.Root), "apex-orchestrator", "20261031-100000-aaaaaa")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: 20261031-100000-aaaaaa\nstarted_at: 2026-10-31T10:00:00Z\nstatus: completed\n"+
		"totals:\n  tokens_input: 400\n  tokens_output: 100\n  tokens_cache_read: 900\n  tokens_cache_creation: 0\n"), 0o644))
	asClaudeCode(t)
	_, err = p.RunDelegated(ctx(), act(OrderMilestoneApprove, "M1"), nil)
	require.Equal(t, CodeOrdersEscalate, codeOf(t, err))
	require.ErrorContains(t, err, "M1 has used 1400 tokens since its approval, over 1000")
}

// D666+ F2: prepare-release reads the configured changelog, and Keep a
// Changelog's "## [1.2.0]" names a version as "## v1.2.0" and "## 1.2.0" do.
func TestPrepareRelease_Changelog(t *testing.T) {
	for doc, want := range map[string]bool{
		"## v1.2.0 (2026-10-31)\n":                  true,
		"## 1.2.0\n":                                true,
		"## [1.2.0] - 2026-10-31\n":                 true,
		"## [v1.2.0]\n":                             true,
		"## [1.2.0](https://example.com/compare)\n": true,
		"## [1.2.0]\r\n":                            true,
		"## v1.2.01\n":                              false,
		"## [1.2.0-rc.1]\n":                         false,
		"### v1.2.0\n":                              false,
		"see ## v1.2.0\n":                           false,
	} {
		require.Equal(t, want, changelogNames("# Changelog\n\n"+doc, "v1.2.0"), doc)
	}

	files := baseFiles()
	files["_apex/config.yaml"] = "docs:\n  changelog: docs/CHANGES.md\n"
	files["docs/CHANGES.md"] = "# Changes\n\n## [1.2.0] - 2026-10-31\n"
	files["CHANGELOG.md"] = "# CHANGELOG\n\n## v9.9.9\n"
	p := fixture(t, files)
	checks, _, err := p.recheck(ctx(), DelegatedAct{Act: OrderPrepareRelease, Target: "v1.2.0"}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"docs/CHANGES.md at HEAD names v1.2.0"}, checks)
	_, _, err = p.recheck(ctx(), DelegatedAct{Act: OrderPrepareRelease, Target: "v9.9.9"}, nil)
	require.Equal(t, CodeOrdersRefused, codeOf(t, err), "the configured changelog, not CHANGELOG.md")
	require.ErrorContains(t, err, "docs/CHANGES.md at HEAD names no section for v9.9.9")
}
