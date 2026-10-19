package apexv1

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D205 Q121: nothing ape writes into a project's files or git history says
// a bare "v0" or "v1" — it says apex-v0 / apex-v1, or a release number.
// Machine values (JSON keys, codes, the apex-v0-final tag) keep their
// names; terminal-only text is out of scope.

var bareV01Re = regexp.MustCompile(`\bv[01]\b`)

// bareV01 lists the lines of s that say v0 or v1 alone: not after
// "apex-", not a release number (v0.30.0, v1.0.0-rc.8).
func bareV01(s string) []string {
	var out []string
	for i, l := range strings.Split(s, "\n") {
		for _, m := range bareV01Re.FindAllStringIndex(l, -1) {
			if strings.HasSuffix(l[:m[0]], "apex-") {
				continue
			}
			if rest := l[m[1]:]; len(rest) > 1 && rest[0] == '.' && rest[1] >= '0' && rest[1] <= '9' {
				continue
			}
			out = append(out, strings.TrimSpace(l)+" (line "+strconv.Itoa(i+1)+")")
			break
		}
	}
	return out
}

// requireNoBareV01 fails on any file under root (relative paths in only,
// all when nil) or the message of any of the last n commits (ape's) that
// says v0 or v1 alone.
func requireNoBareV01(t *testing.T, root string, only map[string]bool, n int) {
	t.Helper()
	var bad []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if only != nil && !only[rel] {
			return nil
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, l := range bareV01(string(data)) {
			bad = append(bad, rel+": "+l)
		}
		return nil
	}))
	if data, err := os.ReadFile(filepath.Join(root, ".git", "hooks", "pre-commit")); err == nil {
		for _, l := range bareV01(string(data)) {
			bad = append(bad, ".git/hooks/pre-commit: "+l)
		}
	}
	for _, l := range bareV01(run(t, root, "log", "--format=%B", "-"+strconv.Itoa(n))) {
		bad = append(bad, "git log: "+l)
	}
	require.Empty(t, bad, "Q121: a bare v0/v1 in what ape wrote — say apex-v0/apex-v1, or a release number")
}

func TestBareV01(t *testing.T) {
	require.Empty(t, bareV01("apex-v0 and apex-v1, v0.30.0, v1.0.0-rc.8, apex-v0-final, v10, sv1, v1x"))
	require.Len(t, bareV01("APEX v0\nthe v1 skeleton\nv1."), 3)
}

// Q121: init's tree and commit, the standard ADR included.
func TestWording_InitWritesNoBareV01(t *testing.T) {
	p := initProject(t)
	requireNoBareV01(t, p.Root, nil, 1)
	require.Empty(t, bareV01(EmbeddedStandardADR()))
}

// Q121: what the lift writes — its commit message, the disposition and
// its report, the converted ADRs, AGENTS.md, the config, the skeleton —
// with the gaps a record missing everything gets.
func TestWording_LiftWritesNoBareV01(t *testing.T) {
	root := liftFixture(t, false)
	p := &Project{Root: root}
	write(t, p, "development/governance/adrs/adr-0009_bare.md", "---\nid: ADR-0009\nstatus: odd\n---\n\nNo sections.\n")
	tagLift(t, root)
	res, err := Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	only := map[string]bool{
		res.Disposition: true, res.Report: true, "AGENTS.md": true, configYAML: true,
		"development/project.md": true, "development/roadmap.md": true, "_apex/state/.gitignore": true, "_apex/state/decisions.json": true,
	}
	for i := range res.ADRs {
		only[res.ADRs[i].Path] = true
	}
	only[res.StandardADR.Path] = true
	requireNoBareV01(t, root, only, 1)
	var gaps []string
	for i := range res.ADRs {
		gaps = append(gaps, res.ADRs[i].Gaps...)
	}
	require.NotEmpty(t, gaps)
	require.Empty(t, bareV01(strings.Join(gaps, "\n")))
}

// Q121: ape decision standard's commits.
func TestWording_StandardCommitsSayNoBareV01(t *testing.T) {
	p := rc7LiftedProject(t, ADRProposed)
	_, err := p.DecisionStandard(ctx())
	require.NoError(t, err)
	installPayload(t, p)
	_, err = p.DecisionStandard(ctx())
	require.NoError(t, err)
	require.Empty(t, bareV01(run(t, p.Root, "log", "--format=%B", "-2")))
}
