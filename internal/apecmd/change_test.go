package apecmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/change"

	"github.com/stretchr/testify/require"
)

// changeProject is a project `ape change` would accept: a git
// repository, an ignored ape subtree, a declared evidence_folder, and a
// commit so HEAD exists.
func changeProject(t *testing.T, ignoreLine string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := projectFor(t, allExtensionsConfig+"evidence_folder: development/governance/evidence\n")
	gitInit(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte(ignoreLine+"\n"), 0o644))
	gitCommitAll(t, root, "base")
	return root
}

// The ignore rule a project actually writes is the one doctor's own fix
// command hands it — `_output/ape/`, a directory-only pattern. Asked
// about the bare path, git answers "not ignored" until the directory
// exists, so a project that took that advice and whose FIRST ape run is
// `ape change` would be refused for having done exactly the right thing.
//
// A contents rule (`_output/ape/*`) is worse: git ignores what the
// folder holds and not the folder, so the bare path answers "not
// ignored" for ever. Both are what the trailing-slash query fixes.
func TestChangePreflight_AcceptsEveryIgnoreRuleShape(t *testing.T) {
	for _, rule := range []string{"_output/ape/", "/_output/ape/", "_output/ape", "_output/", "_output/ape/*", "_output/ape/**"} {
		t.Run(rule, func(t *testing.T) {
			root := changeProject(t, rule)
			cfg := tryResolveProjectConfig(root)
			require.NotNil(t, cfg)

			// Resolving the config ISSUES a timestamp, which persists
			// {output_folder}/ape/timestamp.state and so creates the very
			// directory this is about. Removed again here, because the
			// case under test is the project whose first ape run is this
			// one — where the bare-path query answers "not ignored".
			require.NoError(t, os.RemoveAll(filepath.Join(root, "_output")))
			require.NoDirExists(t, filepath.Join(root, "_output", "ape"))
			require.NoError(t, changePreflight(context.Background(), cfg), "rule %q should satisfy the gate", rule)
		})
	}
}

func TestChangePreflight_RefusesTheStatesItCannotCommitFrom(t *testing.T) {
	t.Run("the ape subtree is not ignored", func(t *testing.T) {
		root := changeProject(t, "something-else")
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "not ignored by git")
	})

	t.Run("a dirty tree", func(t *testing.T) {
		root := changeProject(t, "_output/ape/")
		require.NoError(t, os.WriteFile(filepath.Join(root, "dirt.txt"), []byte("x\n"), 0o644))
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "not clean")
		require.ErrorContains(t, err, "dirt.txt", "the report names what is in the way")
	})

	t.Run("a detached HEAD", func(t *testing.T) {
		root := changeProject(t, "_output/ape/")
		cmd := exec.CommandContext(context.Background(), "git", "checkout", "--detach", "HEAD")
		cmd.Dir = root
		require.NoError(t, cmd.Run())
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "detached")
	})

	t.Run("no evidence_folder", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig)
		gitInit(t, root)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("_output/ape/\n"), 0o644))
		gitCommitAll(t, root, "base")
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "evidence_folder is not set")
	})

	t.Run("not a git repository", func(t *testing.T) {
		root := projectFor(t, allExtensionsConfig+"evidence_folder: development/governance/evidence\n")
		err := changePreflight(context.Background(), tryResolveProjectConfig(root))
		require.ErrorContains(t, err, "not a git repository")
	})
}

func TestResolveChangeRequest(t *testing.T) {
	t.Run("the positional form is a human at a shell", func(t *testing.T) {
		got, err := resolveChangeRequest("the reference is stale", "", strings.NewReader(""), "")
		require.NoError(t, err)
		require.Equal(t, "the reference is stale", got)
	})

	t.Run("stdin is spelled -", func(t *testing.T) {
		got, err := resolveChangeRequest("", "-", strings.NewReader("from stdin\n"), "")
		require.NoError(t, err)
		require.Equal(t, "from stdin", got, "exactly one trailing newline is stripped")
	})

	t.Run("a file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "req.txt")
		require.NoError(t, os.WriteFile(path, []byte("from a file\n"), 0o644))
		got, err := resolveChangeRequest("", path, strings.NewReader(""), "")
		require.NoError(t, err)
		require.Equal(t, "from a file", got)
	})

	t.Run("--fixes alone has no request", func(t *testing.T) {
		got, err := resolveChangeRequest("", "", strings.NewReader(""), "20260919-a1b2c3")
		require.NoError(t, err)
		require.Empty(t, got, "no request means no Request: trailer")
	})

	t.Run("two sources are refused", func(t *testing.T) {
		_, err := resolveChangeRequest("typed", "/tmp/x", strings.NewReader(""), "")
		require.ErrorContains(t, err, "mutually exclusive")
	})

	t.Run("nothing at all is refused", func(t *testing.T) {
		_, err := resolveChangeRequest("", "", strings.NewReader(""), "")
		require.ErrorContains(t, err, "nothing to do")
	})
}

// The request is typed into the REPL as keystrokes. Each of these is
// ordinary text that would not survive the typing, so each is refused at
// the door rather than diagnosed from a stuck pane an hour later.
func TestValidateTypedLine_RefusesWhatCannotBeTyped(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a line break mid-text", "first\nsecond\n", "line break"},
		{"a second trailing newline", "text\n\n", "line break"},
		{"a tab", "a\tb", "control character"},
		{"an escape", "a\x1bb", "control character"},
		{"a trailing backslash", `end with \`, "backslash"},
		{"empty", "\n", "is empty"},
		{"whitespace only", "   \n", "is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateTypedLine([]byte(tc.in), "the request")
			require.ErrorContains(t, err, tc.want)
		})
	}

	t.Run("a CRLF file is not a malformed request", func(t *testing.T) {
		got, err := validateTypedLine([]byte("written on windows\r\n"), "the request")
		require.NoError(t, err)
		require.Equal(t, "written on windows", got)
	})

	t.Run("an @path at the end survives", func(t *testing.T) {
		got, err := validateTypedLine([]byte("read @docs/reference/cli.md\n"), "the request")
		require.NoError(t, err)
		require.Equal(t, "read @docs/reference/cli.md", got)
	})
}

// A contract written outside the change directory is an untracked file
// in the working tree that no goal claims, and the reconciliation would
// then refuse the whole run over ape's own artifact.
func TestResolveContractOut(t *testing.T) {
	dir := t.TempDir()
	changeDir := filepath.Join(dir, "_output", "ape", "changes", "20260919-120000-abc1234")
	require.NoError(t, os.MkdirAll(changeDir, 0o755))

	got, err := resolveContractOut(changeDir, "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(changeDir, "contract.yaml"), got)

	got, err = resolveContractOut(changeDir, filepath.Join(changeDir, "pinned.yaml"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(changeDir, "pinned.yaml"), got)

	_, err = resolveContractOut(changeDir, filepath.Join(dir, "elsewhere.yaml"))
	require.ErrorContains(t, err, "must sit inside the change directory")
}

// The monotonic stamp is second-granularity, so two changes in the same
// second would otherwise write into one directory and read each other's
// contract.
func TestComputeChangeID_DistinctWithinOneSecond(t *testing.T) {
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	first := computeChangeID(at, "one request", "/p")
	second := computeChangeID(at.Add(time.Millisecond), "another request", "/p")

	require.True(t, strings.HasPrefix(first, "20260919-120000-"))
	require.NotEqual(t, first, second)
}

// exitCodeOf reads the code a command's error carries.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	require.ErrorAs(t, err, &ee, "a change's verdict travels as an exit code")
	return ee.code
}

// settleFixture is a project mid-run: the dispatch has returned, the
// skill has edited a file and written its gate output, and the contract
// is on disk. Everything after that is ape's.
func settleFixture(t *testing.T, contract string) (*changeRun, changeOptions) {
	t.Helper()
	return settleFixtureEdits(t, contract, true)
}

// settleFixtureEdits builds the same fixture with or without the
// skill's edits. A run that escalated or refused edited NOTHING — the
// framework's contract says so in as many words — so a fixture that
// left edits behind for those would be testing a contract the skill
// cannot write.
func settleFixtureEdits(t *testing.T, contract string, edits bool) (*changeRun, changeOptions) {
	t.Helper()
	root := changeProject(t, "_output/ape/")
	// The file the change will edit exists and is TRACKED, so a refusal
	// has a real patch to save rather than only untracked copies.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs", "reference"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "reference", "cli.md"),
		[]byte("old\n"), 0o644))
	gitCommitAll(t, root, "the reference")

	o := changeOptions{request: "the reference is out of date", cwdFlag: root}
	r, err := changeStart(context.Background(), o)
	require.NoError(t, err)

	if !edits {
		if contract != "" {
			require.NoError(t, os.WriteFile(r.contractPath, []byte(contract), 0o644))
		}
		return r, o
	}

	// What the skill did, in the order it would have done it.
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "reference", "cli.md"),
		[]byte("regenerated\n"), 0o644))
	evidenceDir := filepath.Join(root, "development", "governance", "evidence", "20260919-a")
	require.NoError(t, os.MkdirAll(evidenceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evidenceDir, "gate.txt"), []byte("PASS\n"), 0o644))

	if contract != "" {
		require.NoError(t, os.WriteFile(r.contractPath, []byte(contract), 0o644))
	}
	return r, o
}

const landedContract = `contract_version: '1'
maintenance_status: landed
goals_total: 1
goals_landed: 1
route: 'none'
blocking_condition: 'none'
goals:
  - goal: 'the reference is stale'
    status: landed
    paths:
      - 'docs/reference/cli.md'
    subject: 'docs(cli): regenerate the reference'
    gates: 'make docs-cli-check'
    evidence: 'development/governance/evidence/20260919-a'
    triage: 'none'
    governance: 'none'
    deferred: []
    findings_patched: 0
    findings_deferred: 0
`

// The whole path, end to end: contract in, two commits out, clean tree,
// exit 0.
func TestChangeSettle_ALandedRunCommitsAndLeavesNothing(t *testing.T) {
	r, o := settleFixture(t, landedContract)

	err := r.settle(context.Background(), o, taskRun{})
	require.NoError(t, err)

	subjects := changeLogSubjects(t, r.cfg.Root)
	require.Equal(t, []string{
		"docs(cli): regenerate the reference",
		"evidence: change " + r.id + " goal 1",
		"the reference",
		"base",
	}, subjects)

	left, lerr := change.Changed(context.Background(), r.cfg.Root)
	require.NoError(t, lerr)
	require.Empty(t, left, "a landed run leaves the tree clean")

	// The durable half: change.yaml survives the envelope.
	rec, rerr := os.ReadFile(filepath.Join(r.dir, "change.yaml"))
	require.NoError(t, rerr)
	require.Contains(t, string(rec), "outcome: landed")
	require.Contains(t, string(rec), "request: the reference is out of date")
}

// An edit no goal claims refuses the whole run: nothing is committed,
// the tree is untouched, and the work is saved as residue.
func TestChangeSettle_AnUnclaimedEditRefusesAndSavesTheWork(t *testing.T) {
	r, o := settleFixture(t, landedContract)
	require.NoError(t, os.WriteFile(filepath.Join(r.cfg.Root, "sneaky.txt"), []byte("x\n"), 0o644))

	err := r.settle(context.Background(), o, taskRun{})
	require.Equal(t, ExitCommitContract, exitCodeOf(t, err))
	require.Equal(t, []string{"the reference", "base"}, changeLogSubjects(t, r.cfg.Root),
		"nothing was committed")

	saved, rerr := os.ReadFile(filepath.Join(r.dir, "untracked", "sneaky.txt"))
	require.NoError(t, rerr)
	require.Equal(t, "x\n", string(saved), "the work is saved before anyone is asked to clear the tree")
	require.FileExists(t, filepath.Join(r.dir, "residue.patch"))
}

// The three outcome codes, and the two that win over them.
func TestChangeSettle_OutcomeExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contract string
		edits    bool
		want     int
	}{
		{"escalated", strings.Replace(strings.Replace(landedContract,
			"maintenance_status: landed", "maintenance_status: escalated", 1),
			"    status: landed", "    status: not-started", 1), false, exitChangeEscalated},
		{"refused", strings.Replace(strings.Replace(landedContract,
			"maintenance_status: landed", "maintenance_status: refused", 1),
			"    status: landed", "    status: not-started", 1), false, exitChangeRefused},
		{"halted", strings.Replace(strings.Replace(landedContract,
			"maintenance_status: landed", "maintenance_status: halted", 1),
			"    status: landed", "    status: halted", 1), true, exitChangeHalted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// goals_landed follows the goal's own status.
			contract := strings.Replace(tc.contract, "goals_landed: 1", "goals_landed: 0", 1)
			r, o := settleFixtureEdits(t, contract, tc.edits)

			err := r.settle(context.Background(), o, taskRun{})
			require.Equal(t, tc.want, exitCodeOf(t, err))
		})
	}

	t.Run("a missing contract is exit 1, not an outcome", func(t *testing.T) {
		r, o := settleFixture(t, "")
		err := r.settle(context.Background(), o, taskRun{})
		require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
		require.Equal(t, []string{"the reference", "base"}, changeLogSubjects(t, r.cfg.Root),
			"ape never infers landed from a tree it cannot account for")
	})

	t.Run("the dispatch's own failure wins over any outcome", func(t *testing.T) {
		r, o := settleFixture(t, landedContract)
		err := r.settle(context.Background(), o, taskRun{
			Envelope: taskEnvelope{ExitCode: ExitREPLNotReady},
		})
		require.Equal(t, ExitREPLNotReady, exitCodeOf(t, err))
		require.Equal(t, []string{"the reference", "base"}, changeLogSubjects(t, r.cfg.Root))
	})
}

func changeLogSubjects(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "log", "--format=%s")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// --prompt-file is how a printed escalation command carries the
// operator's own words to the next command: argv cannot, and the
// destination is the same REPL, typed into as keystrokes.
func TestResolvePromptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.txt")
	require.NoError(t, os.WriteFile(path, []byte("fix the reference\n"), 0o644))

	got, err := resolvePromptFile(path, false, false, strings.NewReader(""))
	require.NoError(t, err)
	require.Equal(t, "fix the reference", got)

	got, err = resolvePromptFile("-", false, false, strings.NewReader("from stdin\n"))
	require.NoError(t, err)
	require.Equal(t, "from stdin", got)

	_, err = resolvePromptFile(path, true, false, strings.NewReader(""))
	require.ErrorContains(t, err, "mutually exclusive")

	_, err = resolvePromptFile(path, false, true, strings.NewReader(""))
	require.ErrorContains(t, err, "--handoff")

	// The same shape check a request takes, for the same reason.
	multiline := filepath.Join(dir, "two-lines.txt")
	require.NoError(t, os.WriteFile(multiline, []byte("one\ntwo\n"), 0o644))
	_, err = resolvePromptFile(multiline, false, false, strings.NewReader(""))
	require.ErrorContains(t, err, "line break")

	// Nothing asked for, nothing read.
	got, err = resolvePromptFile("", false, false, strings.NewReader("ignored"))
	require.NoError(t, err)
	require.Empty(t, got)
}

// `escalated` and `refused` are the skill asserting it edited nothing.
// With edits in the tree the contract is false, and the refusal says
// which contradiction it found — an operator must not read exit 6 there
// as an ownership failure.
func TestChangeSettle_AnEscalatedContractWithEditsNamesTheContradiction(t *testing.T) {
	escalated := strings.Replace(strings.Replace(
		strings.Replace(landedContract, "maintenance_status: landed", "maintenance_status: escalated", 1),
		"    status: landed", "    status: not-started", 1),
		"goals_landed: 1", "goals_landed: 0", 1)
	r, o := settleFixtureEdits(t, escalated, true)

	err := r.settle(context.Background(), o, taskRun{})
	require.Equal(t, ExitCommitContract, exitCodeOf(t, err))
	require.ErrorContains(t, err, "the contract reports escalated, which edits nothing")
	require.Equal(t, []string{"the reference", "base"}, changeLogSubjects(t, r.cfg.Root))
}
