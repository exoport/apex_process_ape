package apexv1

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	}
	for _, d := range []string{"development/specs", "development/changes", "development/archive", "development/decisions", "development/.apex"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	run(t, root, "init", "-q", "-b", "main")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "--allow-empty", "-m", "fixture")
	p, err := Open(root)
	require.NoError(t, err)
	return p
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
		"AGENTS.md":                          "# AGENTS.md\n",
		"CLAUDE.md":                          "@AGENTS.md\n",
		"greet.sh":                           "#!/bin/sh\necho Hello $1\n",
		"development/.apex/.gitignore":       "logs/\nstop-gate/\n",
	}
}

// changeDoc renders a change.md with the given delta and tasks bodies.
func changeDoc(id, touches, delta, tasks string) string {
	return "---\nid: " + id + "\nmilestone: M1\ntouches: [" + touches + "]\n---\n# " + titleFromSlug(id) +
		"\n## Why\nBecause the greeter must change.\n\n## Out of scope\n- farewells\n## Delta\n" + delta +
		"\n## Tasks\n" + tasks + "\n## Notes\n"
}

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
func findingMessages(fs []Finding) string {
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
	if runtime.GOOS == "windows" {
		t.Skip("check commands and their fixtures are POSIX shell")
	}
	for _, bin := range []string{"sh", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
}
