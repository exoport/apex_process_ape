package apecmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// The framework ships a FORK of aboard's curated recipe library: the same
// files with every `aboard <verb>` invocation rewritten to `ape aboard
// <verb>`, because in an APEX project the board is driven through this
// binary. The fork sat frozen at aboard v0.2.1 while upstream moved to
// v0.3.0, and nothing on either side reported it — not ape's gates, not
// the framework's thirteen validators. It surfaced in conversation.
//
// This is the check that would have caught it, and it lives HERE because
// this is the only repo that holds both trees: the module cache has
// upstream at exactly the version go.mod pins, and APEX_FRAMEWORK_REPO
// points at the fork. The framework's validators cannot see the module
// cache and never will.
//
// It fails at the right moment — when someone bumps the aboard dependency
// — rather than months later when a human notices a recipe describing a
// renderer that moved.
//
// A framework-authored recipe (project-status-dashboard.md) carries no
// `upstream:` block and is skipped by construction: this checks the fork,
// not the library.
func TestContract_LiveAboardRecipeForkMatchesUpstream(t *testing.T) {
	root := frameworkSubtreeRoot(t)
	dir := filepath.Join(root, filepath.FromSlash(framework.SubtreeAboardRecipes))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no aboard recipes in this checkout (%v)", err)
	}

	pinned := pinnedAboardVersion(t)
	cache := aboardModuleRecipes(t, pinned)

	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, readErr)

		stamp, ok := parseUpstreamStamp(string(body))
		if !ok {
			continue // framework-authored; not part of the fork
		}
		checked++

		t.Run(e.Name(), func(t *testing.T) {
			// The stamp must name the version THIS binary ships. A bump
			// that leaves the stamp behind is exactly the drift the fork
			// suffered, and it is caught here before the content compare
			// so the failure names the cause rather than a diff.
			require.Equal(t, pinned, stamp.version,
				"recipe is stamped against aboard %s but ape pins %s — re-port the fork or correct the stamp",
				stamp.version, pinned)

			upstreamBody, found := cache[filepath.Base(stamp.path)]
			require.True(t, found,
				"stamp names %s, which is not in aboard %s's recipes/", stamp.path, pinned)

			got := reverseApePrefix(stripUpstreamStamp(string(body)))
			if got == upstreamBody {
				return
			}
			// EXACT comparison, deliberately. This tolerated markdown
			// table repadding for one revision, because the ported file
			// had been run through prettier and a byte-exact gate would
			// have failed on semantically correct content. That tolerance
			// is gone, and the reason it is gone is the point: a fork only
			// equal after normalizing is one someone has run a formatter
			// over, and the next edit through that formatter is where real
			// drift hides. The framework now exempts the forked files in
			// `.prettierignore`, so byte-identity holds at the SOURCE and
			// this check does not have to be lenient to stay useful.
			//
			// Reported as the first differing lines rather than two whole
			// files: require.Equal on multi-kilobyte markdown prints both
			// in full, and a failure nobody can read is a gate nobody acts
			// on.
			diff, allTableRows := firstDifference(upstreamBody, got)
			hint := ""
			if allTableRows {
				hint = "\n  EVERY differing line is a markdown table row, which is what a formatter" +
					"\n  does to a forked file — check the framework's .prettierignore before" +
					"\n  re-porting, or the next port will repad it again."
			}
			t.Fatalf("fork has diverged from aboard %s beyond the documented `ape ` prefix:\n%s%s",
				pinned, diff, hint)
		})
	}
	// A SKIP, not a failure. The stamps arrived with the framework release
	// that introduced them, and an older checkout legitimately predates
	// them — ape's own `make test` must not fail because the sibling
	// checkout on this machine is older than the convention.
	//
	// It names the directory so the skip is auditable: "a skip is not a
	// pass" is only true if the reader can tell WHICH tree went unchecked.
	// This is also how a framework that silently DROPPED its stamps would
	// read, which is why the line says unchecked rather than fine.
	if checked == 0 {
		t.Skipf("no recipe under %s carries an `upstream:` stamp — "+
			"the fork is UNCHECKED here (a framework predating the stamps, or one that lost them)", dir)
	}
}

// firstDifference renders the first few diverging lines with their line
// numbers, plus how many lines differ in total. The second return says
// whether EVERY differing line is a markdown table row — the signature of
// a formatter having run over a forked file, which is a different problem
// from real content drift and has a different fix.
func firstDifference(want, got string) (string, bool) {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	var b strings.Builder
	shown, differing, tableRows := 0, 0, 0
	for i := 0; i < len(w) || i < len(g); i++ {
		var lw, lg string
		if i < len(w) {
			lw = w[i]
		}
		if i < len(g) {
			lg = g[i]
		}
		if lw == lg {
			continue
		}
		differing++
		if mdTableRowRe.MatchString(lw) && mdTableRowRe.MatchString(lg) {
			tableRows++
		}
		if shown < 3 {
			fmt.Fprintf(&b, "  line %d\n    upstream: %s\n    fork:     %s\n", i+1, truncLine(lw), truncLine(lg))
			shown++
		}
	}
	fmt.Fprintf(&b, "  (%d line(s) differ in total)", differing)
	return b.String(), differing > 0 && tableRows == differing
}

func truncLine(s string) string {
	const width = 120
	if len(s) > width {
		return s[:width] + "…"
	}
	return s
}

type upstreamStamp struct {
	path    string
	version string
}

var (
	upstreamBlockRe = regexp.MustCompile(`(?m)^upstream:\n(?:[ \t]+.*\n)+`)
	upstreamPathRe  = regexp.MustCompile(`(?m)^[ \t]+path:[ \t]*(.+?)[ \t]*$`)
	upstreamVerRe   = regexp.MustCompile(`(?m)^[ \t]+version:[ \t]*(.+?)[ \t]*$`)
)

func parseUpstreamStamp(body string) (upstreamStamp, bool) {
	block := upstreamBlockRe.FindString(body)
	if block == "" {
		return upstreamStamp{}, false
	}
	p := upstreamPathRe.FindStringSubmatch(block)
	v := upstreamVerRe.FindStringSubmatch(block)
	if p == nil || v == nil {
		return upstreamStamp{}, false
	}
	return upstreamStamp{path: strings.Trim(p[1], `"'`), version: strings.Trim(v[1], `"'`)}, true
}

// stripUpstreamStamp removes the fork's own `upstream:` frontmatter block,
// which upstream does not carry and which is the one addition the
// transform does not describe.
func stripUpstreamStamp(body string) string {
	return upstreamBlockRe.ReplaceAllString(body, "")
}

// reverseApePrefix undoes the fork's documented transform.
//
// Narrow on purpose: it rewrites `ape aboard ` and nothing else. The
// transform is only reversible while no recipe legitimately discusses the
// standalone binary in prose — true for all three today. If that changes,
// this has to name command positions instead of pattern-matching, and the
// content compare below will say so loudly rather than silently pass.
func reverseApePrefix(s string) string {
	return strings.ReplaceAll(s, "ape aboard ", "aboard ")
}

var mdTableRowRe = regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`)

// pinnedAboardVersion is the aboard version THIS build depends on, read
// from go.mod rather than written down — a literal here would be the same
// class of defect the check exists to catch.
func pinnedAboardVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/exoport/aboard").Output()
	require.NoError(t, err, "could not resolve the pinned aboard version")
	v := strings.TrimSpace(string(out))
	require.NotEmpty(t, v)
	return v
}

// aboardModuleRecipes reads aboard's curated library straight out of the
// module cache at the pinned version.
//
// The files ARE in the module zip — verified, not assumed — so this needs
// no vendoring, no network, and no new dependency. The cache is read-only
// and content-addressed by version, which is what makes it a trustworthy
// copy of upstream rather than a second fork.
func aboardModuleRecipes(t *testing.T, version string) map[string]string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	require.NoError(t, err)
	dir := filepath.Join(strings.TrimSpace(string(out)),
		"github.com", "exoport", "aboard@"+version, "recipes")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("aboard %s recipes are not in the module cache (%v) — run `go mod download`", version, err)
	}
	got := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, readErr)
		got[e.Name()] = string(b)
	}
	require.NotEmpty(t, got, "aboard %s shipped no recipes/", version)
	return got
}
