package apexv1

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// docsChange is the milestone's docs change (D130) as apex-change writes
// it under D152: docs: true, a README task and the docs-accuracy check.
func docsChange(id string, docs bool) string {
	doc := changeDoc(id, "greeting", "none — docs only",
		"- T1 README mentions the wave — check: grep -q wave README.md\n- T2 the docs are accurate — check: human-review\n")
	if docs {
		doc = strings.Replace(doc, "milestone: M1\n", "milestone: M1\ndocs: true\n", 1)
	}
	return doc
}

// docsWave is a reviewed docs change, docs-wave, in a project whose
// config (cfg, committed before the v1 epoch) is baseline; work runs on
// its branch before the review, the tree clean after it.
func docsWave(t *testing.T, cfg string, docs bool, work func(p *Project)) *Project {
	t.Helper()
	files := baseFiles()
	files["README.md"] = "# Greeter\n"
	files["scripts/lint.sh"] = "#!/bin/sh\n"
	if cfg != "" {
		files["_apex/config.yaml"] = cfg
	}
	p := fixture(t, files)
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	addChange(t, p, "docs-wave", docsChange("docs-wave", docs))
	commitAll(t, p, "plan: docs-wave")
	_, err := p.Start(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "README.md", "# Greeter\n\nIt can wave.\n")
	write(t, p, "docs/wave.md", "Waving.\n")
	if work != nil {
		work(p)
	}
	commitAll(t, p, "docs: wave")
	_, err = p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/docs-wave/review.md", "fine\n")
	commitAll(t, p, "review: docs-wave")
	_, err = p.Review(ctx(), "docs-wave", "pass", "development/changes/docs-wave/review.md")
	require.NoError(t, err)
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
	return p
}

func requireFail(t *testing.T, err error, code string) *FailError {
	t.Helper()
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, code, fe.Code, fe.Msg)
	return fe
}

// D152: the docs-accuracy check of a docs change is the delegate's by
// default — no human_review block — taken by apex-accept with the default
// args and its models entry; ape status lists it as delegable and next
// names the delegate; a pass is recorded as any delegate's.
func TestDocsDelegate_DefaultOnADocsOnlyChange(t *testing.T) {
	p := docsWave(t, "models:\n  apex-accept: {model: sonnet, effort: high}\n", true, nil)
	plan, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)
	require.Equal(t, "apex-accept", plan.Skill)
	require.Equal(t, "--task T2 --change docs-wave", plan.Args)
	require.Equal(t, SkillModel{Model: "sonnet", Effort: "high"}, plan.Model)
	require.Equal(t, DelegableDocsDefault, plan.Reason)

	r := p.Status()
	require.Equal(t, []DelegableTask{{Task: "T2", Reason: "docs-default"}}, r.Changes[0].Delegable)
	require.Contains(t, r.Next, "delegate T2 of docs-wave to apex-accept: `ape change accept docs-wave T2 --delegate --wait 9m`")
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(data), `"delegable":[{"task":"T2","reason":"docs-default"}]`)

	// Only a human-review task is the docs check.
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T1")
	require.ErrorContains(t, err, "proven by `grep -q wave README.md`")

	// The records are the delegate path's (D78), unchanged.
	dr := &DelegateRun{
		Change: "docs-wave", Task: "T2", Skill: plan.Skill, Args: plan.Args, Folder: plan.Folder, DetachedID: "run-d1",
		StartedAt: Stamp(), HeadSHA: HeadSHA(ctx(), p.Root), Branch: CurrentBranch(ctx(), p.Root), ChangeTip: plan.ChangeTip,
	}
	require.NoError(t, p.SaveDelegateRun(dr))
	t.Setenv(EnvDelegate, "docs-wave/T2")
	t.Setenv(envDetachedRun, "run-d1")
	_, err = p.RecordDelegateVerdict(ctx(), "docs-wave", "T2", "pass", "README and docs/ match the code", nil)
	require.NoError(t, err)
	leaveSession(t)
	out, err := p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	require.NotNil(t, out.Accepted)
	require.Equal(t, "delegate:apex-accept", out.Accepted.By)
	require.Equal(t, "run-d1", out.Accepted.Run)
	require.Equal(t, "Accepted-By: delegate:apex-accept T2 run run-d1\nChange: docs-wave", run(t, p.Root, "log", "-1", "--format=%b"))
	require.Empty(t, p.Status().Changes[0].Delegable)

	// A fail is a person's, as D78 says: not delegable, not retried.
	p = docsWave(t, "", true, nil)
	plan, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)
	dr = &DelegateRun{
		Change: "docs-wave", Task: "T2", Skill: plan.Skill, Args: plan.Args, Folder: plan.Folder, DetachedID: "run-d2",
		StartedAt: Stamp(), HeadSHA: HeadSHA(ctx(), p.Root), Branch: CurrentBranch(ctx(), p.Root), ChangeTip: plan.ChangeTip,
	}
	require.NoError(t, p.SaveDelegateRun(dr))
	_, err = p.SettleDelegateRun(ctx(), dr)
	require.NoError(t, err)
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	requireFail(t, err, "delegate.already-failed")
	r = p.Status()
	require.Empty(t, r.Changes[0].Delegable)
	require.Contains(t, r.Next, "a person reviews docs-wave")
}

// D152 Q69 (b): a docs change that touched code is not a delegate's by
// default — main.go named — nor is one that moved code into docs/.
func TestDocsDelegate_RefusedOutsideTheDocs(t *testing.T) {
	for name, tc := range map[string]struct {
		work func(p *Project)
		want string
	}{
		"code":           {work: func(p *Project) { write(t, p, "main.go", "package main\n") }, want: "touches main.go, outside the docs (README.md, docs/: release.docs, docs.changelog and docs.user_docs) and the development folder"},
		"code into docs": {work: func(p *Project) { run(t, p.Root, "mv", "scripts/lint.sh", "docs/lint.sh") }, want: "touches scripts/lint.sh, outside"},
		"config":         {work: func(p *Project) { write(t, p, "_apex/config.yaml", "notify: echo\n") }, want: "touches _apex/config.yaml, outside"},
	} {
		t.Run(name, func(t *testing.T) {
			p := docsWave(t, "", true, tc.work)
			_, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
			fe := requireFail(t, err, "delegate.docs-scope")
			require.Equal(t, ActionPerson, ActionFor(fe.Code))
			require.Contains(t, fe.Msg, tc.want)
			r := p.Status()
			require.Equal(t, []string{"T2"}, r.Changes[0].AwaitingHuman)
			require.Empty(t, r.Changes[0].Delegable)
			require.Contains(t, r.Next, "a person reviews docs-wave")
		})
	}
	// release.docs names what counts as docs: docs/ listed, docs/wave.md is in.
	p := docsWave(t, "release: {docs: [README.md, docs/]}\n", true, nil)
	_, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)
	p = docsWave(t, "release: {docs: [docs/]}\n", true, nil)
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.ErrorContains(t, requireFail(t, err, "delegate.docs-scope"), "touches README.md, outside the docs (docs/: release.docs")
}

// D158 R6b-1: a docs change must update the changelog (the project's
// rule, review's finding), so the docs default counts the configured
// docs.changelog as docs, and docs.user_docs too.
func TestDocsDelegate_ChangelogAndUserDocsAreDocs(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  string
		work func(p *Project)
	}{
		"docs.changelog CHANGELOG.md": {cfg: "docs: {changelog: CHANGELOG.md}\n", work: func(p *Project) { write(t, p, "CHANGELOG.md", "- waving\n") }},
		"docs.changelog":              {cfg: "docs: {changelog: ./HISTORY.md}\n", work: func(p *Project) { write(t, p, "HISTORY.md", "- waving\n") }},
		"docs.user_docs": {cfg: "docs: {changelog: CHANGELOG.md, user_docs: manual/}\n", work: func(p *Project) {
			write(t, p, "manual/wave.md", "Waving.\n")
			write(t, p, "CHANGELOG.md", "- waving\n")
		}},
		"release.docs and the changelog": {cfg: "release: {docs: [README.md, docs/]}\ndocs: {changelog: CHANGELOG.md}\n", work: func(p *Project) { write(t, p, "CHANGELOG.md", "- waving\n") }},
	} {
		t.Run(name, func(t *testing.T) {
			p := docsWave(t, tc.cfg, true, tc.work)
			plan, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
			require.NoError(t, err)
			require.Equal(t, DelegableDocsDefault, plan.Reason)
			require.Equal(t, []DelegableTask{{Task: "T2", Reason: "docs-default"}}, p.Status().Changes[0].Delegable)
		})
	}
	// Only a configured changelog counts: with no docs block, CHANGELOG.md
	// is outside (eval P22 d).
	p0 := docsWave(t, "", true, func(p *Project) { write(t, p, "CHANGELOG.md", "- waving\n") })
	_, err0 := p0.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.ErrorContains(t, requireFail(t, err0, "delegate.docs-scope"), "touches CHANGELOG.md, outside the docs (README.md, docs/:")
	require.Empty(t, p0.Status().Changes[0].Delegable)
	// docs.changelog names the changelog: CHANGELOG.md is then no docs.
	p := docsWave(t, "docs: {changelog: HISTORY.md}\n", true, func(p *Project) { write(t, p, "CHANGELOG.md", "- waving\n") })
	_, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.ErrorContains(t, requireFail(t, err, "delegate.docs-scope"), "touches CHANGELOG.md, outside the docs (README.md, docs/, HISTORY.md:")
	require.Empty(t, p.Status().Changes[0].Delegable)
}

// D152 Q70: human_review: {docs: person} turns the docs default off, and a
// block with only docs: delegates no other task.
func TestDocsDelegate_OffWithDocsPerson(t *testing.T) {
	p := docsWave(t, "human_review: {docs: person}\n", true, nil)
	_, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	fe := requireFail(t, err, "delegate.off")
	require.Equal(t, ActionPerson, ActionFor(fe.Code))
	require.Contains(t, fe.Msg, "human_review.docs: person")
	r := p.Status()
	require.Empty(t, r.Changes[0].Delegable)
	require.Contains(t, r.Next, "a person reviews docs-wave")

	// docs: delegate is the default, written out.
	p = docsWave(t, "human_review: {docs: delegate}\n", true, nil)
	plan, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)
	require.Equal(t, DelegableDocsDefault, plan.Reason)

	for _, cfg := range []string{"human_review: {docs: person}\n", "human_review: {docs: delegate}\n"} {
		p = delegatedWave(t, cfg)
		_, err = p.PrepareDelegate(ctx(), "add-wave", "T2")
		require.Contains(t, requireFail(t, err, "delegate.off").Msg, "sets only docs:, which delegates no other task", cfg)
		require.Empty(t, p.Status().Changes[0].Delegable, cfg)
		require.NotContains(t, p.Status().Next, "--delegate", cfg)
	}
}

// Without docs: true nothing changes (D78): delegate.off with no
// human_review block, however docs-only the diff; the configured delegate
// takes every human-review task, a docs change's included, whatever it
// touched and whatever human_review.docs says (Q69).
func TestDocsDelegate_UnchangedWithoutTheMarker(t *testing.T) {
	p := docsWave(t, "", false, nil)
	_, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	fe := requireFail(t, err, "delegate.off")
	require.Contains(t, fe.Msg, "no human_review: block")
	require.Contains(t, fe.Msg, "D152")
	require.Empty(t, p.Status().Changes[0].Delegable)

	p = delegatedWave(t, "human_review: {}\n")
	require.Equal(t, []DelegableTask{{Task: "T2", Reason: "configured"}}, p.Status().Changes[0].Delegable)
	plan, err := p.PrepareDelegate(ctx(), "add-wave", "T2")
	require.NoError(t, err)
	require.Equal(t, DelegableConfigured, plan.Reason)

	p = docsWave(t, "human_review: {delegate: apex-accept, docs: person}\n", true, func(p *Project) { write(t, p, "main.go", "package main\n") })
	plan, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)
	require.Equal(t, DelegableConfigured, plan.Reason)
	require.Equal(t, []DelegableTask{{Task: "T2", Reason: "configured"}}, p.Status().Changes[0].Delegable)
}

// human_review stays guarded (config.changed): adding docs: person is a
// guarded change, and a model's removal of it — or an uncommitted edit of
// release.docs, which bounds the default — stops the docs default.
func TestDocsDelegate_ConfigGuard(t *testing.T) {
	p := fixture(t, baseFiles())
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "_apex/config.yaml", "human_review: {docs: person}\n")
	commitAll(t, p, "chore: docs are a person's")
	require.Contains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys (human_review)")

	p = docsWave(t, "human_review: {docs: person}\n", true, nil)
	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "_apex/config.yaml", "")
	commitAll(t, p, "chore: let the delegate take the docs")
	_, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.Contains(t, requireFail(t, err, "delegate.unaccepted-config").Msg, "human_review block was changed by a commit no person accepted")
	_, err = p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)

	write(t, p, "_apex/config.yaml", "release: {docs: [README.md, greet.sh]}\n")
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.Contains(t, requireFail(t, err, "delegate.unaccepted-config").Msg, "release block differs from the one committed at HEAD")

	// D158: the docs block bounds the default too — docs.user_docs: src/
	// would hand code to the delegate — so the docs default guards it;
	// project-wide it stays unguarded (no config.changed).
	write(t, p, "_apex/config.yaml", "docs: {user_docs: src/}\n")
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.Contains(t, requireFail(t, err, "delegate.unaccepted-config").Msg, "docs block differs from the one committed at HEAD")
	commitAll(t, p, "chore: the user docs")
	require.NotContains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys")
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.Contains(t, requireFail(t, err, "delegate.unaccepted-config").Msg, "docs block was changed by a commit no person accepted")
	_, err = p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.NoError(t, err)
}

// Q90 (eval Q21): the guard runs before the docs: person check, so a
// model's own unaccepted human_review: {docs: person} — committed, or an
// uncommitted edit — is delegate.unaccepted-config, as the help says, not
// delegate.off; once a person accepts it, it is delegate.off.
func TestDocsDelegate_UnacceptedDocsPersonIsUnacceptedConfig(t *testing.T) {
	p := docsWave(t, "", true, nil)
	write(t, p, "_apex/config.yaml", "human_review: {docs: person}\n")
	_, err := p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.Contains(t, requireFail(t, err, "delegate.unaccepted-config").Msg, "human_review block differs from the one committed at HEAD")

	run(t, p.Root, "switch", "-q", "main")
	commitAll(t, p, "chore: the docs are a person's")
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	fe := requireFail(t, err, "delegate.unaccepted-config")
	require.Contains(t, fe.Msg, "human_review block was changed by a commit no person accepted")
	require.Equal(t, ActionPerson, ActionFor(fe.Code))

	_, err = p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	_, err = p.PrepareDelegate(ctx(), "docs-wave", "T2")
	require.Contains(t, requireFail(t, err, "delegate.off").Msg, "human_review.docs: person")
}

// change check reads docs: as a YAML boolean only (D152): any other value
// is a change.frontmatter finding, and docs: is no unknown key.
func TestCheckChange_DocsKey(t *testing.T) {
	for value, want := range map[string]string{
		"true": "", "false": "",
		"yes":      "frontmatter docs: is \"yes\", not true or false",
		"\"true\"": "frontmatter docs: is \"true\", not true or false",
		"[a]":      "frontmatter docs: is a list or mapping, not true or false",
	} {
		p := fixture(t, baseFiles())
		doc := strings.Replace(docsChange("docs-wave", false), "milestone: M1\n", "milestone: M1\ndocs: "+value+"\n", 1)
		addChange(t, p, "docs-wave", doc)
		fs := p.CheckChange("docs-wave")
		msgs := findingMessages(fs)
		require.NotContains(t, msgs, "frontmatter key", value)
		if want == "" {
			require.Empty(t, fs, value)
			require.Equal(t, value == "true", ParseChange("", doc).Meta.Docs)
			continue
		}
		require.Contains(t, msgs, want)
		require.Contains(t, msgs, "drop the key on any other change")
		require.False(t, ParseChange("", doc).Meta.Docs, value)
		for _, f := range ClassifyFindings(fs) {
			require.Equal(t, "change.frontmatter", f.Code)
		}
	}
}

// human_review.docs is delegate or person, and the block's keys are
// checked: a typo would otherwise make a docs-only block a delegate for
// every task.
func TestHumanReviewConfig_Docs(t *testing.T) {
	for cfg, want := range map[string]string{
		"human_review: {docs: maybe}\n":  `human_review.docs: "maybe" is not delegate or person`,
		"human_review: {doc: person}\n":  "human_review.doc: not a key ape knows",
		"human_review: {docs: person}\n": "",
		"human_review:\n":                "",
	} {
		root := t.TempDir()
		write(t, &Project{Root: root}, "_apex/config.yaml", cfg)
		c, err := LoadConfig(root)
		if want != "" {
			require.ErrorContains(t, err, want, cfg)
			continue
		}
		require.NoError(t, err, cfg)
		require.False(t, c.HumanReview.Delegates(), cfg)
	}
	for _, cfg := range []string{"human_review: {}\n", "human_review: {model: haiku}\n", "human_review: {docs: person, args: \"--task {task}\"}\n"} {
		root := t.TempDir()
		write(t, &Project{Root: root}, "_apex/config.yaml", cfg)
		c, err := LoadConfig(root)
		require.NoError(t, err, cfg)
		require.True(t, c.HumanReview.Delegates(), cfg)
	}
}
