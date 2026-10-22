package apexv1

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// fixture builds a v1 project tree in a temp git repo: files maps a
// project-relative path to content. The repo has one commit holding
// everything given, so tests start from a clean tree.
func fixture(t *testing.T, files map[string]string) *Project {
	t.Helper()
	needSh(t)
	root := t.TempDir()
	gitEnv(t)
	asPerson(t)
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	}
	for _, d := range []string{"development/specs", "development/changes", "development/archive", "development/decisions", "_apex/state"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	p, err := Open(root)
	require.NoError(t, err)
	// A milestone the fixture's roadmap says is approved was approved by a
	// person through ape (H2), unless the fixture brings its own records.
	if _, ok := files["_apex/state/milestones.json"]; !ok {
		recordApprovals(t, p)
	}
	run(t, root, "init", "-q", "-b", "main")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "--allow-empty", "-m", "fixture")
	return p
}

// recordApprovals writes milestones.json approval records, by "eval", for
// every milestone the roadmap says is approved, gate as it is now. It
// commits nothing.
func recordApprovals(t *testing.T, p *Project) {
	t.Helper()
	rm := p.roadmap()
	if rm == nil {
		return
	}
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	n := 0
	for i := range rm.Milestones {
		if m := &rm.Milestones[i]; m.Status == MilestoneApproved {
			if recs[m.ID] == nil {
				recs[m.ID] = &MilestoneRecord{}
			}
			recs[m.ID].Approval = p.newApproval(m, "eval")
			n++
		}
	}
	if n > 0 {
		require.NoError(t, p.saveMilestoneRecords(recs))
	}
}

// asPerson runs the test as a person at a terminal: no model session
// (requirePerson), whatever session runs the tests.
func asPerson(t *testing.T) {
	t.Helper()
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
}

// inApeSession marks the test as running inside a session ape launched
// (APE_SESSION), after a fixture that ran as a person.
func inApeSession(t *testing.T) {
	t.Helper()
	t.Setenv(envApeSession, "task/test")
}

// gitEnv isolates git from the machine's config and gives it an identity.
func gitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p *Project, rel, content string) {
	t.Helper()
	path := filepath.Join(p.Root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
}

func read(t *testing.T, p *Project, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(data)
}

func commitAll(t *testing.T, p *Project, msg string) {
	t.Helper()
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", msg)
}

func subjects(t *testing.T, p *Project) []string {
	t.Helper()
	return strings.Split(run(t, p.Root, "log", "--format=%s"), "\n")
}

const projectMD = `# Greeter
## Vision
Greets people.
## Users
Humans.
## Scope
Greeting.
## Out of scope
Farewells.
## Constraints
POSIX sh only.
`

const roadmapApproved = `# Roadmap

## M1 — Walking skeleton
Status: approved (by eval 2026-10-15T12:00:00Z)
Gate: ./gate.sh
Budget: 30
Goal: A greeter that greets.
Out of scope:
- farewells
`

const greetingSpec = `# Greeting
Purpose: Say things to people.

### Requirement: Say hello
The system SHALL print Hello.
#### Scenario: plain run
- GIVEN nothing WHEN greet runs THEN it prints Hello
- check: ./greet.sh | grep -q Hello

### Requirement: Say name
The system SHALL greet by name.
#### Scenario: named
- GIVEN a name WHEN greet runs THEN it prints the name
- check: ./greet.sh Ann | grep -q Ann
`

// baseFiles is a project with an approved M1 and one greeting spec.
func baseFiles() map[string]string {
	return map[string]string{
		"development/project.md":             projectMD,
		"development/roadmap.md":             roadmapApproved,
		"development/specs/greeting/spec.md": greetingSpec,
		"AGENTS.md":                          "# AGENTS.md\n\n" + projectContextLine("development") + "\n",
		"CLAUDE.md":                          "@AGENTS.md\n",
		"greet.sh":                           "#!/bin/sh\necho Hello $1\n",
		"_apex/state/.gitignore":             runlog.V1StateGitignore,
	}
}

// changeDoc renders a change.md with the given delta and tasks bodies,
// typed feat: the default archive subject is "{type}: {title}" (D193).
func changeDoc(id, touches, delta, tasks string) string {
	return "---\nid: " + id + "\nmilestone: M1\ntype: feat\ntouches: [" + touches + "]\n---\n# " + titleFromSlug(id) +
		"\n## Why\nBecause the greeter must change.\n\n## Out of scope\n- farewells\n## Delta\n" + delta +
		"\n## Tasks\n" + tasks + "\n## Notes\n"
}

// untyped is a change.md of changeDoc's without its type: line.
func untyped(doc string) string { return strings.Replace(doc, "type: feat\n", "", 1) }

// addChange writes a change folder with a draft state.
func addChange(t *testing.T, p *Project, id, doc string) {
	t.Helper()
	write(t, p, "development/changes/"+id+"/change.md", doc)
	require.NoError(t, SaveState(p.StateFile(id), &State{ID: id, Status: StatusDraft}))
}

func ctx() context.Context { return context.Background() }

// findingMessages renders findings for substring assertions. Every
// validator test goes through it, so it is also where item 13's promise is
// kept: no finding any test produces may lack a published code.
// fixContractCodes are the finding codes v1.1 W5–W8 added or extended:
// each of their messages has a fix (V11RC1-F1). The value says whether the
// tests that produce the code need the shell fixtures (needSh, toolPath),
// which a platform without them — Windows — skips: TestMain requires such a
// code only where they run (D409).
var fixContractCodes = map[string]bool{
	"tools.unknown": true, CodeToolsTooBig: true, "tools.not-installed": false, CodePluginsUserScope: false,
	CodeFenceMissing: true, CodeFenceAltered: true, CodePackageLeak: true,
	"brief.bad-name": true, "brief.uncommitted": true, "brief.edited": true,
	"roadmap.delivery-link": true, "roadmap.delivery-unlinked": true,
}

var (
	fixContractMu   sync.Mutex
	fixContractSeen = map[string]bool{}
)

// fixContractUnseen lists the fixContractCodes no test has checked, of
// those whose producing tests run on this platform.
func fixContractUnseen() []string {
	fixContractMu.Lock()
	defer fixContractMu.Unlock()
	shOK, _ := shFixtures()
	var out []string
	for c, needsSh := range fixContractCodes {
		if needsSh && !shOK {
			continue
		}
		if !fixContractSeen[c] {
			out = append(out, c)
		}
	}
	return out
}

// checkFixContract panics when a finding breaks the diagnostic contract
// (V11RC1-F1): in --json, message is the text before the first " — " and
// fix the text after it — classified, and as Diagnostics gives it to a
// failure's JSON — and a W5–W8 code's finding has a fix. Every helper that
// classifies findings runs it, so each test that produces a code checks it.
func checkFixContract(fs []Finding) {
	cls := ClassifyFindings(fs)
	fixContractMu.Lock()
	for _, f := range cls {
		fixContractSeen[f.Code] = true
	}
	fixContractMu.Unlock()
	ds := Diagnostics(&FailError{Code: "change.check", Msg: "check failed", Findings: fs})[1:]
	for i, f := range cls {
		if strings.Contains(f.Message, " — ") {
			panic("finding message holds its fix (V11RC1-F1): " + f.Code + ": " + f.Message)
		}
		if fixContractCodes[f.Code] && f.Fix == "" {
			panic("a W5–W8 finding has no fix (V11RC1-F1): " + f.Code + ": " + f.Message)
		}
		if f.Text() != fs[i].Text() {
			panic("Text() does not give the finding back: " + f.Text() + " != " + fs[i].Text())
		}
		if d := ds[i]; d.Message != f.Message || d.Fix != f.Fix {
			panic("Diagnostics splits a finding unlike ClassifyFindings: " + d.Message + " | " + d.Fix)
		}
	}
}

func findingMessages(fs []Finding) string {
	checkFixContract(fs)
	for _, f := range ClassifyFindings(fs) {
		if f.Code == UnclassifiedFinding {
			panic("finding has no diagnostic code (add a rule to findingRules): " + f.File + ": " + f.Message)
		}
	}
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.String())
		b.WriteString("\n")
	}
	return b.String()
}

// needSh skips a test that runs check commands where there is no POSIX
// shell (checks run with `sh -c`) or no git.
func needSh(t *testing.T) {
	t.Helper()
	if ok, why := shFixtures(); !ok {
		t.Skip(why)
	}
}

// shFixtures reports whether the shell fixtures run here, and why not.
// APEXV1_TEST_NO_SH=1 makes a run skip them as Windows does, which is how
// TestMain's coverage guard is checked for a platform without them (D409).
func shFixtures() (ok bool, why string) {
	if runtime.GOOS == "windows" {
		return false, "check commands and their fixtures are POSIX shell"
	}
	if os.Getenv("APEXV1_TEST_NO_SH") != "" {
		return false, "APEXV1_TEST_NO_SH: the shell fixtures are skipped, as on Windows"
	}
	for _, bin := range []string{"sh", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return false, bin + " not on PATH"
		}
	}
	return true, ""
}
