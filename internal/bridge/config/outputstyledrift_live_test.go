package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// builtinStyleProbe matches Claude Code's own built-in style table, whose
// entries carry `source:"built-in"` beside their canonical `name`. The
// standard style is absent from it by construction: Claude Code keys that
// one as the literal `default` with a null entry, which is why ape's own
// DefaultOutputStyle is excluded from the comparison below.
var builtinStyleProbe = regexp.MustCompile(`name:"([A-Za-z][A-Za-z0-9 _-]{0,40})",source:"built-in"`)

// TestLive_OutputStyleBuiltins is the drift gate for builtinOutputStyles.
//
// ape folds a built-in's case before writing `outputStyle`, which means
// the table is a claim about a vendor surface that moves on its own
// schedule — `Concise` and `Proactive` only appeared in 2.1.237. A stale
// table does not fail loudly. It fails by HALVES: lowercase keeps working
// for the styles ape knows and silently stops working for a newer one,
// after users have been taught by the working cases that case does not
// matter. Nothing errors, and the session runs Default.
//
// Same standing as check-prices for prices.yaml: hand-curated data about
// something ape does not control, gated against the installed binary
// rather than trusted. Opt-in (APE_CLAUDE_LIVE=1) because it reads the
// local Claude Code install, and never part of `make test` or CI.
//
// Run it with `make check-output-styles`, or the whole sweep with
// `make check-harness`.
func TestLive_OutputStyleBuiltins(t *testing.T) {
	if os.Getenv("APE_CLAUDE_LIVE") != "1" {
		t.Skip("set APE_CLAUDE_LIVE=1 (needs the local Claude Code install) to run the output-style drift gate")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("claude not on PATH: %v — the gate cannot judge the table without the binary it tracks", err)
	}
	// The launcher is a symlink into versions/<v>; the styles live in the
	// resolved binary.
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("resolve %s: %v", bin, err)
	}
	blob, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("read %s: %v", resolved, err)
	}

	found := map[string]bool{}
	for _, m := range builtinStyleProbe.FindAllSubmatch(blob, -1) {
		found[string(m[1])] = true
	}
	// Zero matches is a FAILURE, never a skip. It means the probe no
	// longer recognises the table's shape, so the drift it exists to
	// catch would go unreported while this gate printed green — the
	// "a check that cannot look is not a pass" rule this repo keeps
	// re-learning.
	if len(found) == 0 {
		t.Fatalf("no built-in output styles found in %s — the probe pattern no longer matches Claude Code's "+
			"style table, so this gate is verifying nothing. Re-derive %q against the current binary.",
			resolved, builtinStyleProbe.String())
	}

	known := map[string]bool{}
	for _, name := range BuiltinOutputStyles() {
		known[name] = true
	}

	var missing, extra []string
	for name := range found {
		if !known[name] {
			missing = append(missing, name)
		}
	}
	for name := range known {
		// DefaultOutputStyle is ape's pin for the standard style and has
		// no `source:"built-in"` entry to match; excluded by design.
		if name != DefaultOutputStyle && !found[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("Claude Code ships built-in output style(s) ape does not know: %v.\n"+
			"  ape will NOT fold their case, so a lowercase declaration of one enrols nothing and reports\n"+
			"  nothing while the styles ape does know keep working. Add them to builtinOutputStyles.", missing)
	}
	if len(extra) > 0 {
		t.Errorf("ape's table names built-in style(s) this Claude Code does not ship: %v.\n"+
			"  ape would fold a declaration onto a name that no longer resolves, which Claude Code ignores\n"+
			"  silently. Remove them from builtinOutputStyles, or confirm they are still valid.", extra)
	}
	if len(missing) == 0 && len(extra) == 0 {
		t.Logf("built-in output styles agree with %s: %v", filepath.Base(resolved), BuiltinOutputStyles())
	}
}
