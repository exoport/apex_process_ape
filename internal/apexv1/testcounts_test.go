package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The counts each stack's summary gives, read from real outputs captured
// for V14-PLAN-APE P1 (Go 1.27, cargo, Node 24 node --test, pytest, jest 29,
// vitest 3, Flutter 3.47 on the VM and Chrome).
func TestTestCounts_RealOutputs(t *testing.T) {
	for file, want := range map[string]struct {
		passed, skipped, failed int
		parser, verdict         string
	}{
		"go-skip-v.txt":           {0, 2, 0, "go test -v", ReasonAllSkipped},
		"go-pass-v.txt":           {2, 0, 0, "go test -v", ""},
		"go-fail-v.txt":           {1, 0, 1, "go test -v", ""},
		"go-skip-json.txt":        {0, 2, 0, "go test -json", ReasonAllSkipped},
		"cargo-pass.txt":          {1, 1, 0, "cargo test", ""},
		"cargo-fail.txt":          {0, 1, 1, "cargo test", ""},
		"cargo-zero.txt":          {0, 0, 0, "cargo test", ""},
		"node-skip.txt":           {0, 2, 0, "node --test", ReasonAllSkipped},
		"node-pass.txt":           {2, 0, 0, "node --test", ""},
		"node-fail.txt":           {1, 0, 1, "node --test", ""},
		"pytest-pass.txt":         {2, 0, 0, "pytest", ""},
		"pytest-skip.txt":         {0, 2, 0, "pytest", ReasonAllSkipped},
		"pytest-fail.txt":         {1, 0, 1, "pytest", ""},
		"jest-skip.txt":           {0, 2, 0, "jest", ReasonAllSkipped},
		"jest-fail.txt":           {1, 0, 1, "jest", ""},
		"vitest-mixed.txt":        {0, 2, 1, "vitest", ""},
		"vitest-allskip.txt":      {0, 3, 0, "vitest", ReasonAllSkipped},
		"flutter-vm-pass.txt":     {2, 0, 0, "flutter test", ""},
		"flutter-vm-skip.txt":     {0, 2, 0, "flutter test", ReasonAllSkipped},
		"flutter-vm-fail.txt":     {1, 0, 1, "flutter test", ""},
		"flutter-chrome-pass.txt": {2, 0, 0, "flutter test", ""},
		"flutter-chrome-skip.txt": {0, 2, 0, "flutter test", ReasonAllSkipped},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "testcounts", file))
		require.NoError(t, err)
		c, ok := testCounts(string(b))
		require.True(t, ok, file)
		require.Equal(t, TestCounts{Passed: want.passed, Skipped: want.skipped, Failed: want.failed, Parser: want.parser}, c, file)
		v, _ := skipVerdict("make test", c)
		require.Equal(t, want.verdict, v, file)
	}
	// Plain go test prints no counts: `ok p1 0.002s` hides two skips.
	b, err := os.ReadFile(filepath.Join("testdata", "testcounts", "go-skipplain.txt"))
	require.NoError(t, err)
	_, ok := testCounts(string(b))
	require.False(t, ok)
}

func TestSkipVerdict_NamedSkip(t *testing.T) {
	some := TestCounts{Passed: 3, Skipped: 1, Parser: "go test -v"}
	for cmd, want := range map[string]string{
		"go test -run '^TestStore$' ./store":   ReasonNamedSkip,
		"go test -run=TestA ./...":             ReasonNamedSkip,
		"pytest tests/test_db.py::test_insert": ReasonNamedSkip,
		"flutter test --plain-name 'greets'":   ReasonNamedSkip,
		"go test ./...":                        "",
		"make test":                            "",
	} {
		v, _ := skipVerdict(cmd, some)
		require.Equal(t, want, v, cmd)
	}
}

// D563 Q8: -v is appended to GOFLAGS only for one `go test` invocation.
func TestGoFlagsV(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	require.Equal(t, []string{"GOFLAGS=-mod=mod -v"}, goFlagsV("go test -run '^TestX$' ./store"))
	require.Equal(t, []string{"GOFLAGS=-mod=mod -v"}, goFlagsV("CGO_ENABLED=0 go test ./..."))
	for _, cmd := range []string{
		"go test -v ./...", "go test -json ./...", "go vet ./...", "go build ./...", "go run ./cmd/x",
		"go test ./... | tee out.txt", "go test ./a && go test ./b", "go test ./...; echo done", "make test",
		"GOFLAGS=-count=1 go test ./...", "go test $(go list ./...)", "./fake-go-test.sh",
	} {
		require.Nil(t, goFlagsV(cmd), cmd)
	}
	t.Setenv("GOFLAGS", "-v")
	require.Nil(t, goFlagsV("go test ./..."), "-v already reaches it")
	t.Setenv("GOFLAGS", "")
	require.Equal(t, []string{"GOFLAGS=-v"}, goFlagsV("go test ./..."))
}

// End to end: a judged check whose every test skipped is unrun
// (all-skipped), never green; a gate's own command is not judged.
func TestRunCheck_AllSkippedIsUnrun(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	out := `printf '=== RUN   TestDB\n    db_test.go:9: harness unavailable\n--- SKIP: TestDB (0.00s)\nPASS\nok  \tp\t0.01s\n'`
	r := runCheck(ctx(), dir, out, "", "", Timeouts{}.Limits(KindTask, 0), DefaultVacuousOutput)
	require.Equal(t, ExitUnrun, r.Exit)
	require.Equal(t, ReasonUnrun, r.Reason)
	require.Equal(t, ReasonAllSkipped, r.Skip)
	require.Equal(t, &TestCounts{Skipped: 1, Parser: "go test -v"}, r.Tests)
	g := runCheck(ctx(), dir, out, "", "", Timeouts{}.Limits(KindGate, 0), nil)
	require.Equal(t, 0, g.Exit, "a gate's own command is not judged")
	require.NotNil(t, g.Tests, "its counts are still recorded")
}

func TestPassDropNotices(t *testing.T) {
	prev := &Verify{Results: []CheckResult{
		{Task: "T1", Exit: 0, Tests: &TestCounts{Passed: 40, Parser: "go test -v"}},
		{Task: "T2", Exit: 0, Tests: &TestCounts{Passed: 3}},
		{Task: "T3", Exit: 1, Tests: &TestCounts{Passed: 40}},
	}}
	now := []CheckResult{
		{Task: "T1", Exit: 0, Tests: &TestCounts{Passed: 12, Skipped: 28, Parser: "go test -v"}},
		{Task: "T2", Exit: 0, Tests: &TestCounts{Passed: 0, Skipped: 0}},
		{Task: "T3", Exit: 0, Tests: &TestCounts{Passed: 1}},
	}
	ns := passDropNotices(prev, now)
	require.Len(t, ns, 1, "T2 had too few to judge, T3 was red then")
	require.Equal(t, CodePassDrop, ns[0].Code)
	require.Contains(t, ns[0].Message, "T1 is green but passed 12 test(s), against 40 at the last verify")
	require.Empty(t, passDropNotices(nil, now))
}
