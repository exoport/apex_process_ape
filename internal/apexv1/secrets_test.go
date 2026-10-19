package apexv1

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeToken is GitHub-token shaped, assembled so no scanner flags this
// file.
var fakeToken = "gh" + "p_" + "aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"

func tokenFingerprint() string {
	sum := sha256.Sum256([]byte(fakeToken))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// secretRepo is a git repo with one commit holding files.
func secretRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	needSh(t)
	gitEnv(t)
	root := t.TempDir()
	run(t, root, "init", "-q", "-b", "main")
	for rel, content := range files {
		writeAt(t, root, rel, content)
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "--allow-empty", "-m", "fixture")
	return root
}

func writeAt(tb testing.TB, root, rel, content string) {
	tb.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(tb, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(tb, os.WriteFile(path, []byte(content), 0o644))
}

func secretFail(t *testing.T, err error) *FailError {
	t.Helper()
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, CodeCommitSecret, fe.Code, fe.Msg)
	return fe
}

// D155 Q79: the refusal names <file>:<line> and the rule with the secret
// redacted, prints the exact allow entry, and classifies every finding.
func TestScanCommit_RefusesAnAddedSecret(t *testing.T) {
	root := secretRepo(t, map[string]string{"app.sh": "#!/bin/sh\necho hi\n"})
	writeAt(t, root, "app.sh", "#!/bin/sh\necho hi\nexport GITHUB_TOKEN="+fakeToken+"\n")
	run(t, root, "add", "-A")

	fe := secretFail(t, ScanCommit(ctx(), root, "apex: x", "app.sh"))
	require.Contains(t, fe.Msg, "nothing was committed: apex: x adds a secret: app.sh:3 rule github-pat: ghp_")
	require.NotContains(t, fe.Msg, fakeToken, "redacted")
	require.NotContains(t, fe.Msg, fakeToken[:12], "about four characters shown")
	require.Contains(t, fe.Msg, `secrets: {allow: [{fingerprint: "`+tokenFingerprint()+`", reason: "…"}]}`)
	// E-R6b-2: the placeholder fix first, the allow entry second.
	require.Contains(t, fe.Msg, " — replace each with a placeholder (`<token>`, `$TOKEN`) — an example in docs never needs a real value — or move a real secret to hot/ and read it from there; if one is not a secret, a person adds its entry")
	require.Less(t, strings.Index(fe.Msg, "placeholder"), strings.Index(fe.Msg, "secrets: {allow:"))
	require.Contains(t, fe.Findings[0].Message, " — replace it with a placeholder (`<token>`, `$TOKEN`)")
	require.Len(t, fe.Findings, 1)
	require.Equal(t, "app.sh", fe.Findings[0].File)
	require.Equal(t, 3, fe.Findings[0].Line)
	require.NotContains(t, findingMessages(fe.Findings), fakeToken)
	c := ClassifyFindings(fe.Findings)[0]
	require.Equal(t, CodeCommitSecret, c.Code)
	require.Equal(t, ActionPerson, ActionFor(c.Code))
	require.Contains(t, c.Fix, tokenFingerprint())
	d := Diagnostics(fe)
	require.Equal(t, ActionPerson, d[0].Action)
	require.Equal(t, CodeCommitSecret, d[1].Code)
}

// Line numbers come back from the hunks: added lines among unchanged
// ones, in two runs, keep their place in the new file.
func TestScanCommit_LineNumbersMapBack(t *testing.T) {
	var old []string
	for i := 1; i <= 40; i++ {
		old = append(old, fmt.Sprintf("line %d", i))
	}
	root := secretRepo(t, map[string]string{"a.txt": strings.Join(old, "\n") + "\n"})
	lines := append([]string{}, old[:9]...)
	lines = append(lines, "token = "+fakeToken)
	lines = append(lines, old[9:30]...)
	lines = append(lines, "x", "y", "again "+fakeToken)
	lines = append(lines, old[30:]...)
	writeAt(t, root, "a.txt", strings.Join(lines, "\n")+"\n")
	run(t, root, "add", "-A")
	fe := secretFail(t, ScanCommit(ctx(), root, "apex: x"))
	require.Len(t, fe.Findings, 2)
	require.Equal(t, 10, fe.Findings[0].Line)
	require.Equal(t, 34, fe.Findings[1].Line)
	require.Contains(t, fe.Msg, "adds 2 secrets")
	require.Equal(t, 1, strings.Count(fe.Msg, "{fingerprint:"), "one allow entry per value")
}

// A bingo pin's .sum (go.sum's format: h1: hashes) is skipped as go.sum
// is, so the lift's and `ape update --pin`'s .bingo commits are not
// refused for it; other paths under .bingo are scanned.
func TestScanCommit_BingoSumIsSkipped(t *testing.T) {
	root := secretRepo(t, map[string]string{"x": "x\n"})
	// Two lines of a real bingo .sum (public module hashes) that the
	// generic-api-key rule flags under any other name.
	sum := "cloud.google.com/go/auth v0.20.0 h1:kXTssoVb4azsVDoUiF8KvxAqrsQcQtB53DcSgta74CA=\n" +
		"cloud.google.com/go/auth/oauth2adapt v0.2.8 h1:keo8NaayQZ6wimpNSmW5OPc283g65QNIiLpZnkHRbnc=\n"
	writeAt(t, root, ".bingo/goreleaser.sum", sum)
	writeAt(t, root, "go.sum", sum)
	run(t, root, "add", "-A")
	require.NoError(t, ScanCommit(ctx(), root, "apex: pin"))
	writeAt(t, root, "deps.txt", sum)
	run(t, root, "add", "-A")
	require.Len(t, secretFail(t, ScanCommit(ctx(), root, "apex: pin")).Findings, 2, "the same lines anywhere else are flagged")
	run(t, root, "rm", "-q", "-f", "deps.txt")
	writeAt(t, root, ".bingo/notes.txt", "T="+fakeToken+"\n")
	run(t, root, "add", "-A")
	fe := secretFail(t, ScanCommit(ctx(), root, "apex: pin"))
	require.Len(t, fe.Findings, 1)
	require.Equal(t, ".bingo/notes.txt", fe.Findings[0].File, "other paths under .bingo are scanned")
}

// Q80: the inline betterleaks:allow and gitleaks:allow markers do not
// suppress (a model could write them), and .betterleaksignore is not read.
func TestScanCommit_InlineMarkersAndIgnoreFileDoNotSuppress(t *testing.T) {
	root := secretRepo(t, map[string]string{
		".betterleaksignore": strings.TrimPrefix(tokenFingerprint(), "sha256:") + "\n",
		".gitleaksignore":    strings.TrimPrefix(tokenFingerprint(), "sha256:") + "\n",
	})
	writeAt(t, root, "a.go", "const t = \""+fakeToken+"\" // betterleaks:allow\nconst u = \""+fakeToken+"\" // gitleaks:allow\n")
	run(t, root, "add", "-A")
	fe := secretFail(t, ScanCommit(ctx(), root, "apex: x"))
	require.Len(t, fe.Findings, 2)
}

// Q80: an allow entry in the committed config lets that value through;
// one only in the working tree (uncommitted, unguarded) does not.
func TestScanCommit_AllowListFromTheCommittedConfig(t *testing.T) {
	allow := "secrets:\n  allow:\n    - {fingerprint: \"" + tokenFingerprint() + "\", reason: a fake token in a test}\n"
	root := secretRepo(t, map[string]string{"x": "x\n"})
	writeAt(t, root, configYAML, allow)
	writeAt(t, root, "a.sh", "T="+fakeToken+"\n")
	run(t, root, "add", "--", "a.sh")
	fe := secretFail(t, ScanCommit(ctx(), root, "apex: x", "a.sh"))
	require.Len(t, fe.Findings, 1)
	require.Equal(t, "a.sh", fe.Findings[0].File, "an allow entry only in the working tree does not count")

	run(t, root, "add", "--", configYAML)
	run(t, root, "commit", "-q", "-m", "person: allow the fake token", "--", configYAML)
	require.NoError(t, ScanCommit(ctx(), root, "apex: x", "a.sh"))
}

// R23: since the v1 epoch, secrets.allow is read from the guard's
// baseline — the newest acceptance, else the epoch — not from HEAD: an
// entry committed but not accepted lets nothing through, and the refusal
// says what is missing; once a person accepts it, it does.
func TestScanCommit_AllowListFromTheAcceptedConfig(t *testing.T) {
	files := baseFiles()
	files[configYAML] = "project_name: greeter\n"
	p := fixture(t, files)
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, configYAML, "project_name: greeter\nsecrets:\n  allow:\n    - {fingerprint: \""+tokenFingerprint()+"\", reason: a fake token in a test}\n")
	commitAll(t, p, "chore: allow the fake token")
	write(t, p, "a.sh", "T="+fakeToken+"\n")
	run(t, p.Root, "add", "--", "a.sh")

	fe := secretFail(t, ScanCommit(ctx(), p.Root, "apex: x", "a.sh"))
	require.Len(t, fe.Findings, 1, "a committed, unaccepted allow entry does not count")
	require.Contains(t, fe.Findings[0].Message, " — its allow entry is committed but not accepted: a person runs `ape project accept-config --by <who>`")
	require.Contains(t, fe.Msg, "the allow entry for "+tokenFingerprint()+" is committed but not accepted: a person runs `ape project accept-config --by <who>`")
	require.NotContains(t, fe.Msg, "secrets: {allow: [", "no entry to add: it is there")
	require.Equal(t, CodeCommitSecret, ClassifyFindings(fe.Findings)[0].Code)

	run(t, p.Root, "restore", "--staged", "--", "a.sh")
	_, err := p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	run(t, p.Root, "add", "--", "a.sh")
	require.NoError(t, ScanCommit(ctx(), p.Root, "apex: x", "a.sh"), "accepted: it counts")

	// A later, unaccepted commit that drops the entry from HEAD does not
	// take it away either: the baseline decides.
	write(t, p, configYAML, "project_name: greeter\n")
	run(t, p.Root, "commit", "-q", "-m", "chore: drop it", "--", configYAML)
	require.NoError(t, ScanCommit(ctx(), p.Root, "apex: x", "a.sh"))
}

// Q78: a secret only in a deleted line, or in unchanged context, does not
// block; a moved file adds no lines.
func TestScanCommit_DeletedLinesContextAndMovesAreNotScanned(t *testing.T) {
	root := secretRepo(t, map[string]string{
		"old.sh":  "a\nT=" + fakeToken + "\nb\n",
		"keep.sh": "a\nT=" + fakeToken + "\nb\n",
		"move.sh": "#!/bin/sh\nT=" + fakeToken + "\necho one\necho two\necho three\n",
	})
	writeAt(t, root, "old.sh", "a\nb\n")
	writeAt(t, root, "keep.sh", "a\nT="+fakeToken+"\nb\nc\n")
	run(t, root, "mv", "move.sh", "moved.sh")
	run(t, root, "add", "-A")
	require.NoError(t, ScanCommit(ctx(), root, "apex: x"))
	run(t, root, "rm", "-q", "-f", "keep.sh")
	require.NoError(t, ScanCommit(ctx(), root, "apex: x"), "a deleted file adds nothing")
}

// Q78: a person's unrelated staged file is not scanned — only the
// commit's pathspec — and ape's record commit leaves it staged, uncommitted.
func TestCommitRecord_PersonsStagedFileIsNotScanned(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "mine.env", "TOKEN="+fakeToken+"\n")
	run(t, p.Root, "add", "--", "mine.env")
	write(t, p, "_apex/state/milestones.json", "{}\n")
	sha, err := p.commitRecord(ctx(), "apex: record", "", "_apex/state/milestones.json")
	require.NoError(t, err)
	require.NotEmpty(t, sha)
	require.Equal(t, "_apex/state/milestones.json", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	require.Equal(t, "A  mine.env", run(t, p.Root, "status", "--porcelain", "--", "mine.env"))

	// The same file in the pathspec is scanned, and nothing is committed.
	// It is not ape's own edit: it stays staged, and the refusal says how
	// to unstage it (R28).
	head := HeadSHA(ctx(), p.Root)
	_, err = p.commitRecord(ctx(), "apex: record", "", "mine.env")
	fe := secretFail(t, err)
	require.Len(t, fe.Findings, 1)
	require.Equal(t, "mine.env", fe.Findings[0].File)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.Contains(t, fe.Msg, "mine.env stays staged: `git restore --staged -- mine.env` unstages it; nothing was committed")
	require.Equal(t, "A  mine.env", run(t, p.Root, "status", "--porcelain", "--", "mine.env"))
}

// R28: a refused `ape next add` undoes ape's own edit to the roadmap — the
// tree exactly as before, the refusal says so — and the next harmless
// `next add` commits (it was refused too while the edit stayed staged).
func TestAddNext_RefusedEditIsUndone(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "notes.txt", "a person's own work\n")
	before := run(t, p.Root, "status", "--porcelain")
	head := HeadSHA(ctx(), p.Root)

	_, err := p.AddNext(ctx(), "rotate GITHUB_TOKEN="+fakeToken, "")
	fe := secretFail(t, err)
	require.Contains(t, fe.Msg, "ape's edit to development/roadmap.md was undone; nothing was committed")
	require.Equal(t, before, run(t, p.Root, "status", "--porcelain"))
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.NotContains(t, read(t, p, "development/roadmap.md"), fakeToken)

	res, err := p.AddNext(ctx(), "rotate the GitHub token", "")
	require.NoError(t, err)
	require.Equal(t, res.Commit, HeadSHA(ctx(), p.Root))
	require.Equal(t, before, run(t, p.Root, "status", "--porcelain"))
}

// R28: a roadmap that held a person's uncommitted edits keeps its working
// tree (ape's line in it, beside theirs); only ape's staging is taken
// back, the person's staged hunk kept, and the refusal names the command
// that clears the rest.
func TestAddNext_RefusedEditOnADirtyFileOnlyUnstages(t *testing.T) {
	p := fixture(t, baseFiles())
	rm := "development/roadmap.md"
	write(t, p, rm, read(t, p, rm)+"\nstaged by a person\n")
	run(t, p.Root, "add", "--", rm)
	write(t, p, rm, read(t, p, rm)+"unstaged by a person\n")
	staged := run(t, p.Root, "diff", "--cached")

	_, err := p.AddNext(ctx(), "rotate GITHUB_TOKEN="+fakeToken, "")
	fe := secretFail(t, err)
	require.Contains(t, fe.Msg, "ape's edit to "+rm+" was unstaged and left in the working tree, which held uncommitted edits before it: take it out, or `git restore --worktree -- "+rm+"` drops every unstaged edit to "+rm)
	require.Equal(t, staged, run(t, p.Root, "diff", "--cached"), "the index is as the person left it")
	doc := read(t, p, rm)
	require.Contains(t, doc, "unstaged by a person")
	require.Contains(t, doc, fakeToken, "the working tree is left alone")
	run(t, p.Root, "restore", "--worktree", "--", rm)
	require.NotContains(t, read(t, p, rm), fakeToken)
	require.Contains(t, read(t, p, rm), "staged by a person")
}

// R28: a file ape created for the refused record (an ADR skeleton) is
// removed with its staging.
func TestNewDecision_RefusedSkeletonIsRemoved(t *testing.T) {
	p := fixture(t, baseFiles())
	before := run(t, p.Root, "status", "--porcelain")
	_, err := p.NewDecision(ctx(), "rotate-token", "Rotate "+fakeToken, "")
	fe := secretFail(t, err)
	require.Contains(t, fe.Msg, "ape's edit to development/decisions/0001-rotate-token.md was undone")
	require.NoFileExists(t, filepath.Join(p.Root, "development", "decisions", "0001-rotate-token.md"))
	require.Equal(t, before, run(t, p.Root, "status", "--porcelain"))
	_, err = p.NewDecision(ctx(), "rotate-token", "Rotate the token", "")
	require.NoError(t, err)
}

// A record commit — state.json through commitPaths — is scanned, and
// clean.
func TestCommitPaths_StateJSONIsScannedAndClean(t *testing.T) {
	p := startedWave(t)
	st := state(t, p, "add-wave")
	note := "a note with no secret"
	st.BlockedReason = &note
	require.NoError(t, SaveState(p.StateFile("add-wave"), st))
	_, err := p.commitPaths(ctx(), "apex: add-wave note", "add-wave", p.Rel(p.StateFile("add-wave")))
	require.NoError(t, err)
	require.Equal(t, "apex: add-wave note", subjects(t, p)[0])

	// The same commit carrying a secret is refused, and HEAD stays.
	head := HeadSHA(ctx(), p.Root)
	leak := "token " + fakeToken
	st.BlockedReason = &leak
	require.NoError(t, SaveState(p.StateFile("add-wave"), st))
	err = p.saveAndCommitSubject(ctx(), "add-wave", st, "apex: add-wave blocked")
	fe := secretFail(t, err)
	require.Contains(t, fe.Msg, "development/changes/add-wave/state.json:")
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
}

// The first commit of a repository is scanned against the empty tree.
func TestScanCommit_FirstCommit(t *testing.T) {
	needSh(t)
	gitEnv(t)
	root := t.TempDir()
	run(t, root, "init", "-q", "-b", "main")
	writeAt(t, root, "a.sh", "T="+fakeToken+"\n")
	run(t, root, "add", "-A")
	fe := secretFail(t, ScanCommit(ctx(), root, "apex: project init", "a.sh"))
	require.Len(t, fe.Findings, 1)
	require.Equal(t, "a.sh", fe.Findings[0].File)
	writeAt(t, root, "a.sh", "T=1\n")
	run(t, root, "add", "-A")
	require.NoError(t, ScanCommit(ctx(), root, "apex: project init", "a.sh"))
}

// A pathspec too long for a command line (the lift's) is applied to the
// whole diff instead, with the same result.
func TestScanCommit_LongPathspec(t *testing.T) {
	root := secretRepo(t, map[string]string{"x": "x\n"})
	writeAt(t, root, "dir/in.sh", "T="+fakeToken+"\n")
	writeAt(t, root, "out.sh", "T="+fakeToken+"\n")
	run(t, root, "add", "-A")
	var paths []string
	for i := 0; len(strings.Join(paths, " ")) <= maxPathspecArgs; i++ {
		paths = append(paths, fmt.Sprintf("missing/file-%04d.md", i))
	}
	require.NoError(t, ScanCommit(ctx(), root, "apex: lift", paths...))
	fe := secretFail(t, ScanCommit(ctx(), root, "apex: lift", append(paths, "dir/")...))
	require.Len(t, fe.Findings, 1)
	require.Equal(t, "dir/in.sh", fe.Findings[0].File)
	require.True(t, underAny("sub/a/b", "sub/", []string{"./a/"}))
	require.False(t, underAny("sub/ab", "sub/", []string{"a"}))
	require.True(t, underAny("sub/x", "sub/", []string{"."}))
}

// The diff parser: quoted paths, a deleted file, a pure deletion and a
// binary file add nothing; an added line starting with "++" is content.
func TestParseAddedLines(t *testing.T) {
	diff := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,0 +2,2 @@\n+one\n++++ two\n" +
		"diff --git a/gone b/gone\ndeleted file mode 100644\n--- a/gone\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n" +
		"diff --git a/y b/y\n--- a/y\n+++ b/y\n@@ -3 +2,0 @@\n-del\n" +
		"diff --git a/bin b/bin\nBinary files a/bin and b/bin differ\n" +
		"diff --git \"a/sp\\tace\" \"b/sp\\tace\"\n--- \"a/sp\\tace\"\n+++ \"b/sp\\tace\"\n@@ -0,0 +1 @@\n+q\n\\ No newline at end of file\n"
	require.Equal(t, []addedHunk{
		{path: "x", start: 2, text: "one\n+++ two\n"},
		{path: "sp\tace", start: 1, text: "q\n"},
	}, parseAddedLines(diff))
}

// Q80: secrets.allow is validated as it parses, and secrets is guarded.
func TestSecretsConfig_Parse(t *testing.T) {
	var c Config
	for in, want := range map[string]string{
		"secrets:\n  allow:\n    - {fingerprint: \"" + tokenFingerprint()[7:] + "\", reason: x}\n":      "is not sha256: and 64 hex digits",
		"secrets:\n  allow:\n    - {fingerprint: \"" + tokenFingerprint() + "\"}\n":                     "secrets.allow[0].reason: say why",
		"secrets:\n  allow:\n    - {fingerprint: \"" + tokenFingerprint() + "\", reason: x, path: a}\n": "secrets.allow[0].path: not a key ape knows",
		"secrets:\n  ignore: []\n": "secrets.ignore: not a key ape knows",
		"secrets:\n  allow: x\n":   "secrets.allow: is not a list",
		"secrets:\n  allow:\n    - {fingerprint: \"" + tokenFingerprint() + "\", reason: x}\n    - {fingerprint: \"" + tokenFingerprint() + "\", reason: y}\n": "is listed twice",
	} {
		var c Config
		require.ErrorContains(t, yaml.Unmarshal([]byte(in), &c), want, in)
	}
	require.NoError(t, yaml.Unmarshal([]byte("secrets:\n  allow:\n    - {fingerprint: \""+strings.ToUpper("sha256:"+tokenFingerprint()[7:])+"\", reason: \" a fake \"}\n"), &c))
	require.Equal(t, []SecretAllow{{Fingerprint: tokenFingerprint(), Reason: "a fake"}}, c.Secrets.Allow)
	require.Contains(t, guardedConfigKeys, "secrets")
}

// Q80: a commit that is not ape's and adds an allow entry is
// config.changed, as for every guarded block.
func TestSecretsAllow_IsGuarded(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\n"
	p := fixture(t, files)
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "_apex/config.yaml", "project_name: greeter\nsecrets:\n  allow:\n    - {fingerprint: \""+tokenFingerprint()+"\", reason: fake}\n")
	commitAll(t, p, "allow it")
	require.Contains(t, findingMessages(p.Check()), "guarded config keys (secrets) changed")
}

// A commit built in a temporary index (a base-branch record written from a
// change's branch) is scanned too: the tree against the tip it goes on.
func TestCommitDetached_IsScanned(t *testing.T) {
	p := startedWave(t)
	tip := run(t, p.Root, "rev-parse", "main")
	_, err := p.commitDetached(ctx(), "main", tip, map[string][]byte{"notes.md": []byte("T=" + fakeToken + "\n")}, []string{"notes.md"}, "apex: notes")
	fe := secretFail(t, err)
	require.Len(t, fe.Findings, 1)
	require.Equal(t, "notes.md", fe.Findings[0].File)
	require.Equal(t, tip, run(t, p.Root, "rev-parse", "main"), "main did not move")
	sha, err := p.commitDetached(ctx(), "main", tip, map[string][]byte{"notes.md": []byte("fine\n")}, []string{"notes.md"}, "apex: notes")
	require.NoError(t, err)
	require.Equal(t, sha, run(t, p.Root, "rev-parse", "main"))
}

// secretWave is a reviewed, accepted add-wave whose code, committed on the
// change's branch by the session (not scanned: not ape's commit), holds a
// GitHub-token-shaped string.
func secretWave(t *testing.T, files map[string]string) *Project {
	t.Helper()
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\n# GITHUB_TOKEN="+fakeToken+"\necho wave\n")
	commitAll(t, p, "feat: wave")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	return p
}

// D155: a change's secret is caught at the archive squash at the latest:
// commit.secret, refused before the squash commit — main unchanged, the
// change's branch checked out again, its folder in place, the tree clean.
func TestArchive_RefusesASecretBeforeTheSquash(t *testing.T) {
	p := secretWave(t, baseFiles())
	mainBefore := run(t, p.Root, "rev-parse", "main")
	_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	fe := secretFail(t, err)
	require.Contains(t, fe.Msg, "wave.sh:2 rule github-pat:")
	require.Contains(t, fe.Msg, tokenFingerprint())
	require.Contains(t, fe.Msg, "nothing was committed: main is unchanged and change/add-wave is checked out again")
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "main"))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.DirExists(t, p.ChangeDir("add-wave"))
	require.NoDirExists(t, filepath.Join(p.Root, "development", "archive", time.Now().Format("2006-01-02")+"-add-wave"))
	require.Nil(t, p.Status().ArchiveJournal, "no half-archived state")
	clean(t, p, "a secret-refused archive")
}

// The same change archives once a person's committed allow entry names
// the value.
func TestArchive_SecretAllowedByFingerprint(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "secrets:\n  allow:\n    - fingerprint: \"" + tokenFingerprint() + "\"\n      reason: a fake token the test plants\n"
	p := secretWave(t, files)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, res.Commit, run(t, p.Root, "rev-parse", "main"))
	require.Contains(t, read(t, p, "wave.sh"), fakeToken)
}

// timingRepo is a repo with a committed state.json, and, staged, either a
// record commit's change to it (large false) or a synthetic large commit:
// 2,000 new files of 100 code-like lines (about 23 MB of added lines).
func timingRepo(tb testing.TB, large bool) string {
	tb.Helper()
	root := tb.TempDir()
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		require.NoError(tb, err, string(out))
	}
	state := `{"id": "add-wave", "status": "verified", "base_branch": "main", "base_sha": "%s", "tasks": [%s]}` + "\n"
	tasks := make([]string, 0, 30)
	for i := range 30 {
		tasks = append(tasks, fmt.Sprintf(`{"ref": "T%d", "status": "green", "at": "2026-10-18T12:00:00Z", "log": "_apex/state/logs/verify/T%d.log"}`, i, i))
	}
	git("init", "-q", "-b", "main")
	writeAt(tb, root, "development/changes/add-wave/state.json", fmt.Sprintf(state, strings.Repeat("a", 40), strings.Join(tasks, ",\n")))
	git("add", "-A")
	git("commit", "-q", "-m", "fixture")
	if !large {
		writeAt(tb, root, "development/changes/add-wave/state.json", fmt.Sprintf(state, strings.Repeat("b", 40), strings.Join(tasks, ",\n")))
	} else {
		var b strings.Builder
		for i := range 100 {
			fmt.Fprintf(&b, "\tconfig.Timeout%d := lookupTimeout(ctx, \"service-%d\", defaultTimeout) // retries=%d token_url=https://example.com/%d\n", i, i, i%7, i)
		}
		for i := range 2000 {
			writeAt(tb, root, fmt.Sprintf("src/pkg%02d/file%04d.go", i%40, i), "package pkg\n\n"+b.String())
		}
	}
	git("add", "-A")
	return root
}

// D155 Q81: how long the scan takes on a typical record commit and on a
// large one (target: a record commit under 100 ms, a lift commit under
// 10 s). Opt-in, and run alone for the cold numbers a single ape command
// sees: APE_SECRET_SCAN_TIMING=1 go test -run TestSecretScanTiming -v.
func TestSecretScanTiming(t *testing.T) {
	if os.Getenv("APE_SECRET_SCAN_TIMING") == "" {
		t.Skip("set APE_SECRET_SCAN_TIMING=1 to measure")
	}
	needSh(t)
	gitEnv(t)
	record, large := timingRepo(t, false), timingRepo(t, true)
	for _, c := range []struct {
		name, root string
		paths      []string
	}{
		{"record commit, cold", record, []string{"development/changes/add-wave/state.json"}},
		{"record commit, warm", record, []string{"development/changes/add-wave/state.json"}},
		{"large commit (2000 files, 200k added lines, 23 MB)", large, nil},
	} {
		start := time.Now()
		require.NoError(t, ScanCommit(ctx(), c.root, "apex: timing", c.paths...))
		t.Logf("%s: %s", c.name, time.Since(start).Round(time.Millisecond))
	}
}

// The lift commit is scanned: a line the lift rewrites that holds a secret
// is an added line and refuses it (commit.secret, nothing committed, the
// reset instruction); a secret on a line the lift leaves alone does not.
func TestLift_RefusesASecretItAdds(t *testing.T) {
	root := liftFixture(t, true)
	write(t, &Project{Root: root}, "kept.go", "package main\n\n// Story 1.1: kept\n// GITHUB_TOKEN="+fakeToken+"\nfunc K() {}\n")
	write(t, &Project{Root: root}, "edited.go", "package main\n\n// Story 1.1: GITHUB_TOKEN="+fakeToken+"\nfunc E() {}\n")
	commitAll(t, &Project{Root: root}, "v0: two files")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "APEX v0 final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)
	tagSHA := run(t, root, "rev-parse", "HEAD")

	_, err := Lift(ctx(), root, LiftOptions{Exclude: []string{"schema/**"}})
	fe := secretFail(t, err)
	require.Len(t, fe.Findings, 1, fe.Msg)
	require.Equal(t, "edited.go", fe.Findings[0].File)
	require.Equal(t, 3, fe.Findings[0].Line)
	require.Contains(t, fe.Msg, "the lift commit adds a secret")
	require.Contains(t, fe.Msg, "`git reset --hard` restores the tree, then lift again")
	require.Equal(t, tagSHA, run(t, root, "rev-parse", "HEAD"), "nothing was committed")
}
