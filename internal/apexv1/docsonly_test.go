package apexv1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// docsOnlyCheck is a docs-only change's T1 check that names no path, so
// D170's command rule leaves the skip alone.
const docsOnlyCheck = "grep -qi wave R*.md"

// docsOnlyWave is a started change, docs-wave, in a project whose config
// (cfg, committed before the v1 epoch) sets a regression command; the
// change's work (README.md and docs/wave.md, then work) is committed on
// its branch, not yet verified. check is T1's check.
func docsOnlyWave(t *testing.T, cfg, check string, work func(p *Project)) *Project {
	t.Helper()
	files := baseFiles()
	files["README.md"] = "# Greeter\n"
	files["suite.sh"] = "#!/bin/sh\nexit 0\n"
	files["_apex/config.yaml"] = cfg
	p := fixture(t, files)
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	addChange(t, p, "docs-wave", changeDoc("docs-wave", "greeting", "none — docs only",
		"- T1 README mentions the wave — check: "+check+"\n"))
	commitAll(t, p, "plan: docs-wave")
	_, err := p.Start(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "README.md", "# Greeter\n\nIt can wave.\n")
	write(t, p, "docs/wave.md", "Waving.\n")
	if work != nil {
		work(p)
	}
	commitAll(t, p, "docs: wave")
	return p
}

func regressionRow(v *VerifyResult) *CheckResult {
	for i := range v.Results {
		if v.Results[i].Kind == KindRegression {
			return &v.Results[i]
		}
	}
	return nil
}

// Q87: a change that touched only docs and the development folder skips
// the regression command, and only it: T1 and the scenarios still run. The skip is in the
// verify JSON and in state.json, its paths the docs it touched.
func TestVerify_DocsOnlySkipsTheRegression(t *testing.T) {
	p := docsOnlyWave(t, "regression: ./suite.sh\n", docsOnlyCheck, nil)
	v, err := p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Nil(t, regressionRow(v), "the regression command did not run")
	require.Len(t, v.Results, 3)
	require.Equal(t, "T1", v.Results[0].Task, "the task check still runs")
	require.Equal(t, KindScenario, v.Results[1].Kind, "and so do the scenario checks")
	require.Equal(t, RegressionSkippedDocsOnly, v.Regression.Skipped)
	require.Equal(t, []string{"README.md", "docs/wave.md"}, v.Regression.Paths)
	require.Equal(t, 2, v.Regression.PathsTotal)
	require.Equal(t, "./suite.sh", v.Regression.Cmd)
	require.False(t, v.Regression.Failed)
	require.Empty(t, v.Regression.NotSkipped)
	require.NotNil(t, v.Regression.BaselineExit)

	data, err := json.Marshal(v)
	require.NoError(t, err)
	require.Contains(t, string(data), `"skipped":"docs-only","paths":["README.md","docs/wave.md"],"paths_total":2`)
	require.Contains(t, string(data), `"failed":false`, "the fields readers know stay")

	st := state(t, p, "docs-wave")
	require.Equal(t, StatusVerified, st.Status)
	require.NotNil(t, st.Verify.Regression)
	require.Equal(t, RegressionSkippedDocsOnly, st.Verify.Regression.Skipped)
	require.Equal(t, []string{"README.md", "docs/wave.md"}, st.Verify.Regression.Paths)

	// docs.changelog and docs.user_docs are docs too (DocsDefaultScope).
	p = docsOnlyWave(t, "regression: ./suite.sh\ndocs: {changelog: HISTORY.md, user_docs: guide/}\n", docsOnlyCheck, func(p *Project) {
		write(t, p, "HISTORY.md", "- waves\n")
		write(t, p, "guide/wave.md", "Wave.\n")
	})
	v, err = p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, RegressionSkippedDocsOnly, v.Regression.Skipped)
	require.Equal(t, []string{"HISTORY.md", "README.md", "docs/wave.md", "guide/wave.md"}, v.Regression.Paths)
}

// A change that touched code runs the regression command, as before:
// nothing marks it.
func TestVerify_CodeRunsTheRegression(t *testing.T) {
	p := docsOnlyWave(t, "regression: ./suite.sh\n", docsOnlyCheck, func(p *Project) {
		write(t, p, "greet.sh", "#!/bin/sh\necho Hello $1, waving\n")
	})
	v, err := p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.NotNil(t, regressionRow(v))
	require.Empty(t, v.Regression.Skipped)
	require.Empty(t, v.Regression.Paths)
	require.Empty(t, v.Regression.NotSkipped, "not docs-only: nothing to explain")
	require.Nil(t, state(t, p, "docs-wave").Verify.Regression)
	data, err := json.Marshal(v)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"skipped"`)
	require.NotContains(t, string(data), `"not_skipped"`)
}

// A touched path under docs.tested — docs the project executes — runs it.
func TestVerify_DocsTestedRunsTheRegression(t *testing.T) {
	p := docsOnlyWave(t, "regression: ./suite.sh\ndocs: {tested: [docs/]}\n", docsOnlyCheck, nil)
	v, err := p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, regressionRow(v))
	require.Empty(t, v.Regression.Skipped)
	require.Equal(t, "it touches docs/wave.md, under docs.tested (docs the project executes)", v.Regression.NotSkipped)
}

// The docs block bounds the skip, so it is guarded as for the docs
// delegate: a docs: edit no person accepted runs the regression command —
// it refuses nothing — until a person accepts it.
func TestVerify_UnacceptedDocsConfigRunsTheRegression(t *testing.T) {
	p := docsOnlyWave(t, "regression: ./suite.sh\n", docsOnlyCheck, nil)
	run(t, p.Root, "switch", "-q", "main")
	write(t, p, "_apex/config.yaml", "regression: ./suite.sh\ndocs: {user_docs: docs/}\n")
	commitAll(t, p, "chore: the user docs")
	run(t, p.Root, "switch", "-q", ChangeBranch("docs-wave"))
	v, err := p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err, "verify catches up and runs")
	require.NotNil(t, v.CatchUp)
	require.NotNil(t, regressionRow(v))
	require.Empty(t, v.Regression.Skipped)
	require.Contains(t, v.Regression.NotSkipped, "docs block was changed by a commit no person accepted")
	require.Contains(t, v.Regression.NotSkipped, "so the docs-only skip does not apply")

	run(t, p.Root, "switch", "-q", "main")
	_, err = p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", ChangeBranch("docs-wave"))
	v, err = p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, RegressionSkippedDocsOnly, v.Regression.Skipped, "accepted, the skip applies")
}

// D170: a regression or check command that names a touched docs path, or a
// folder holding one, as a word runs the regression command; one that
// names none (go test ./...) leaves the skip alone.
func TestVerify_CommandNamingADocRunsTheRegression(t *testing.T) {
	p := docsOnlyWave(t, "regression: test -f README.md\n", docsOnlyCheck, nil)
	v, err := p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, regressionRow(v))
	require.Equal(t, "the regression command names README.md", v.Regression.NotSkipped)

	p = docsOnlyWave(t, "regression: go test ./...\n", docsOnlyCheck, nil)
	v, err = p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, regressionRow(v))
	require.Equal(t, RegressionSkippedDocsOnly, v.Regression.Skipped)

	// The change's own check naming the doc it edits does not end the skip:
	// it runs anyway.
	p = docsOnlyWave(t, "regression: ./suite.sh\n", "grep -q wave README.md", nil)
	v, err = p.Verify(ctx(), "docs-wave", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, regressionRow(v))
	require.Equal(t, RegressionSkippedDocsOnly, v.Regression.Skipped)
}

func TestNamedPath(t *testing.T) {
	paths := []string{"README.md", "docs/guide/wave.md"}
	for cmd, want := range map[string]string{
		"test -f README.md":                 "README.md",
		"test -f ./README.md":               "./README.md",
		"markdownlint docs/":                "docs/",
		"./scripts/check-links.sh docs":     "docs",
		"markdownlint 'docs/**/*.md'":       "docs/**/*.md",
		"cd docs/guide && make":             "docs/guide",
		"go test ./...":                     "",
		"make test":                         "",
		"grep -q wave README.md.bak":        "",
		"cat src/README.md":                 "",
		"test -f \"$ROOT/README.md\"":       "",
		"go test ./documentation/...":       "",
		"sh -c 'grep -q x docs/guide/a.md'": "docs/guide/a.md",
	} {
		require.Equal(t, want, namedPath(cmd, paths), cmd)
	}
	require.Empty(t, namedPath("cat README.md", nil))
}

// docs.tested is a list of relative paths inside the project, each once;
// [] is none, and status --json carries it with the block.
func TestDocsConfig_Tested(t *testing.T) {
	var c struct {
		Docs DocsConfig `yaml:"docs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte("docs: {changelog: CHANGELOG.md, tested: [docs/examples/, README.md]}\n"), &c))
	require.Equal(t, []string{"docs/examples/", "README.md"}, c.Docs.Tested)
	require.Equal(t, "CHANGELOG.md", c.Docs.Changelog)
	data, err := json.Marshal(c.Docs)
	require.NoError(t, err)
	require.JSONEq(t, `{"changelog":"CHANGELOG.md","tested":["docs/examples/","README.md"]}`, string(data))

	c.Docs = DocsConfig{}
	require.NoError(t, yaml.Unmarshal([]byte("docs: {tested: []}\n"), &c))
	require.Empty(t, c.Docs.Tested)

	for in, want := range map[string]string{
		"docs: {tested: [/abs/docs]}\n":             `docs.tested: "/abs/docs" is not a relative path inside the project`,
		"docs: {tested: [../other]}\n":              `docs.tested: "../other" is not a relative path inside the project`,
		"docs: {tested: [docs/, ./docs]}\n":         `docs.tested: "./docs" is listed twice`,
		"docs: {tested: [\"\"]}\n":                  "docs.tested: an empty path",
		"docs: {tested: docs/}\n":                   "docs: ",
		"docs: {testd: [docs/]}\n":                  "docs.testd: not a key",
		"docs: {user_docs: ../x, tested: [docs]}\n": `docs.user_docs: "../x" is not a path inside the project`,
	} {
		c.Docs = DocsConfig{}
		err := yaml.Unmarshal([]byte(in), &c)
		require.Error(t, err, in)
		require.Contains(t, err.Error(), want, in)
	}
}
