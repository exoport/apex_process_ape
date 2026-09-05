package migration

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestShellRunner_RealShell is the one test that runs a real command.
//
// Every other test drives a fake, which is what keeps them portable and
// deterministic — but a fake cannot prove the thing that actually matters
// here: that a `check:` line carrying a pipe and embedded quotes reaches
// a shell intact rather than being split on whitespace, and that a
// missing binary comes back as "could not run" rather than as a verdict.
func TestShellRunner_RealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the framework's migration lines are POSIX shell")
	}
	dir := t.TempDir()
	r := ShellRunner{}

	code, err := r.Run(t.Context(), dir, "true")
	require.NoError(t, err)
	require.Equal(t, 0, code)

	code, err = r.Run(t.Context(), dir, "exit 3")
	require.NoError(t, err, "a command that ran and said no is a verdict, not a failure")
	require.Equal(t, 3, code)

	// A pipe with embedded single quotes, the shape the framework's own
	// entries use. An argv split would run `printf` with the rest as
	// arguments and silently succeed.
	code, err = r.Run(t.Context(), dir,
		`printf '{"n":2}' | grep -q '"n":2'`)
	require.NoError(t, err)
	require.Equal(t, 0, code, "the line reached a shell intact")

	// cmd.Dir is honoured, so a check can look at the project it is about.
	require.NoError(t, writeFile(filepath.Join(dir, "marker"), "x"))
	code, err = r.Run(t.Context(), dir, "test -f marker")
	require.NoError(t, err)
	require.Equal(t, 0, code)
}

// TestShellRunner_MissingBinaryReachesCannotTell is the defect this file
// caught, and the reason it runs a real shell rather than a fake.
//
// A shell reports a missing binary as exit 127, NOT as a failure to start
// the process. So a project without `jq`, running the framework's own
// `… | jq -e '…'` check, comes back non-zero — and a mapping that read
// every non-zero code as "unsatisfied" would make the entry pending and
// the runner would apply it. That is precisely the re-application the
// cannot-tell state exists to prevent, arriving through the one door
// nobody watches.
//
// The end-to-end assertion, through the real shell and the real state
// machine: a check that pipes into a binary that is not installed leaves
// the entry CANNOT-TELL and NOT runnable.
func TestShellRunner_MissingBinaryReachesCannotTell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the framework's migration lines are POSIX shell")
	}
	code, err := ShellRunner{}.Run(t.Context(), t.TempDir(), "definitely-not-a-real-binary-92831")
	require.NoError(t, err)
	require.Equal(t, 127, code, "a shell reports a missing command as a code, not a spawn error")

	entries := []Entry{{
		ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable,
		Command: "echo would-have-run",
		Check:   `echo '{}' | definitely-not-a-real-binary-92831 -e '.x'`,
	}}
	p := BuildPlan(t.Context(), "/d", t.TempDir(), entries, nil, ShellRunner{}, true)
	require.Equal(t, StateCannotTell, p.Rows[0].State)
	require.Equal(t, CheckCannotRun, p.Rows[0].Check)
	require.False(t, p.Rows[0].Runnable)
}

// TestShellRunner_ExitOneIsTheAnsweredNoVerdict — the other half: `jq -e`
// and `grep -q` exit 1 for "no", which IS a verdict and does make the
// entry pending.
func TestShellRunner_ExitOneIsTheAnsweredNoVerdict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the framework's migration lines are POSIX shell")
	}
	entries := []Entry{{
		ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable,
		Command: "true",
		Check:   `printf '{"n":1}' | grep -q '"n":2'`,
	}}
	p := BuildPlan(t.Context(), "/d", t.TempDir(), entries, nil, ShellRunner{}, true)
	require.Equal(t, CheckUnsatisfied, p.Rows[0].Check)
	require.Equal(t, StatePending, p.Rows[0].State)
	require.True(t, p.Rows[0].Runnable)
}

func TestNoCheckRunner_ExecutesNothing(t *testing.T) {
	code, err := NoCheckRunner().Run(t.Context(), t.TempDir(), "touch should-not-exist")
	require.Error(t, err)
	require.Equal(t, -1, code)
}

// TestApply_WritesToTheWriter keeps the run narrated: an operator reading
// `ape framework update` output must see the command before it runs.
func TestApply_WritesToTheWriter(t *testing.T) {
	entries := []Entry{{ID: "a", Version: "0.16.0", Seq: 1, Kind: KindDerivable, Command: "cmd-a"}}
	r := newRunner()
	p := BuildPlan(t.Context(), "/d", "/p", entries, nil, r, true)

	var buf writerRecorder
	Apply(t.Context(), &buf, "/p", p, r, fixedStamp)
	require.Contains(t, buf.String(), "cmd-a")
}

type writerRecorder struct{ b []byte }

func (w *writerRecorder) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *writerRecorder) String() string              { return string(w.b) }

var _ io.Writer = (*writerRecorder)(nil)

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
