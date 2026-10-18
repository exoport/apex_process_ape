package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// L3, from the dry runs: Go's ./... is a package pattern, not a
// placeholder (radix and cortex got no regression: at all), and a fenced
// line's trailing comment is the shell's (atomix's regression ran
// `make smoke` alone, the && after its comment swallowed).
func TestExtractGates_GoPatternsAndInlineComments(t *testing.T) {
	doc := "# Project Context\n\n## Code Validation\n\n" +
		"```bash\n" +
		"make smoke     # boots the stack against ./... and a pg15\n" +
		"make shellcheck\n" +
		"go test -race -count=1 ./... # every package, ./... included\n" +
		"go build ./... ./examples/...\n" +
		"go test -tags integration ./internal/...   # needs the stack\n" +
		"grep -c '#' notes.txt # a quoted # is not a comment\n" +
		"echo \"${HOME} # not a comment\" ${GOFLAGS}\n" +
		"```\n\n" +
		"- `./gate.sh <board>` — per board\n" +
		"- `deploy {name}` — per target\n" +
		"- `make lint # gofmt + vet` — the lint\n" +
		"- `make … ` — anything else\n\n" +
		"## Next\n"
	g := extractGates("development/project-context.md", doc)
	require.Equal(t, []string{
		"make smoke",
		"make shellcheck",
		"go test -race -count=1 ./...",
		"go build ./... ./examples/...",
		"go test -tags integration ./internal/...",
		"grep -c '#' notes.txt",
		"echo \"${HOME} # not a comment\" ${GOFLAGS}",
		"./gate.sh <board>",
		"deploy {name}",
		"make lint",
		"make …",
	}, g.Commands)
	require.Equal(t, []string{"./gate.sh <board>", "deploy {name}", "make …"}, g.NotRunnable)
	require.Equal(t, "make smoke && make shellcheck && go test -race -count=1 ./... && go build ./... ./examples/... && "+
		"go test -tags integration ./internal/... && grep -c '#' notes.txt && echo \"${HOME} # not a comment\" ${GOFLAGS} && make lint", g.Regression)
}

// cortex's only gate was one fenced block with ./... in it.
func TestExtractGates_OneBlockWithGoPatterns(t *testing.T) {
	g := extractGates("CLAUDE.md", "## Code Validation\n\n```\nmake lint\ngo vet ./...\ngo test ./...\n```\n")
	require.Equal(t, "make lint && go vet ./... && go test ./...", g.Regression)
	require.Empty(t, g.NotRunnable)
}

func TestStripShellComment(t *testing.T) {
	for in, want := range map[string]string{
		"make smoke     # boots": "make smoke",
		"make smoke#notacomment": "make smoke#notacomment",
		"echo 'a # b' # c":       "echo 'a # b'",
		`echo "a \" # b" # c`:    `echo "a \" # b"`,
		`echo a\ # b`:            `echo a\ # b`,
		"go test ./...":          "go test ./...",
	} {
		require.Equal(t, want, stripShellComment(in), in)
	}
}

// R12: the lift's two comparison runs split `regression:` on its
// top-level && only.
func TestSplitTopLevelAnd(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"make test", []string{"make test"}},
		{"make lint && make test && go vet ./...", []string{"make lint", "make test", "go vet ./..."}},
		{"make lint&&make test", []string{"make lint", "make test"}},
		{"make a || make b && make c", []string{"make a || make b", "make c"}},
		{"make a; make b && make c | tee x.log", []string{"make a; make b", "make c | tee x.log"}},
		{"sh -c 'a && b' && make c", []string{"sh -c 'a && b'", "make c"}},
		{`echo "x && y" && make c`, []string{`echo "x && y"`, "make c"}},
		{`echo "a \" && b" && make c`, []string{`echo "a \" && b"`, "make c"}},
		{"( cd web && npm test ) && make c", []string{"( cd web && npm test )", "make c"}},
		{"{ make a && make b; } && make c", []string{"{ make a && make b; }", "make c"}},
		{"echo $(a && b) && make c", []string{"echo $(a && b)", "make c"}},
		{"echo `a && b` && make c", []string{"echo `a && b`", "make c"}},
		{"echo ${X:-a&&b} && make c", []string{"echo ${X:-a&&b}", "make c"}},
		{`echo a\&\& b && make c`, []string{`echo a\&\& b`, "make c"}},
		{"( for d in a b; do\n  make -C $d && true\ndone\n) && make c", []string{"( for d in a b; do\n  make -C $d && true\ndone\n)", "make c"}},
		// From a state change on, the rest is one command: make test ran in web.
		{"make lint && cd web && npm test && npm run e2e", []string{"make lint", "cd web && npm test && npm run e2e"}},
		{"export GOFLAGS=-mod=mod && go test ./...", []string{"export GOFLAGS=-mod=mod && go test ./..."}},
		{"CGO_ENABLED=0 go build ./... && go test ./...", []string{"CGO_ENABLED=0 go build ./...", "go test ./..."}},
		{"X=1 && go test ./...", []string{"X=1 && go test ./..."}},
		// Unbalanced or unsplittable: the one command, as in rc.4.
		{"case $x in a) make a && make b;; esac", []string{"case $x in a) make a && make b;; esac"}},
		{"echo 'open && make b", []string{"echo 'open && make b"}},
		{"make a && && make b", []string{"make a && && make b"}},
		{"make a &", []string{"make a &"}},
	} {
		require.Equal(t, c.want, splitTopLevelAnd(c.in), c.in)
	}
}

// R13, spirometer on rc.4: its two red logs differed only in bingo's
// "(re)installing" line, printed on the tool's first run (the one at the
// tag), and the lift said "the output changed after the lift".
func TestLiftComparisonHash(t *testing.T) {
	tag := "(re)installing /home/u/go/bin/golangci-lint-v2.13.2\n" +
		"golangci-lint run ./...\nlib/x.go:3:1: typecheck: undefined: Foo\n" +
		"ok  \tgithub.com/x/spiro\t0.123s\n--- FAIL: TestY (12ms)\nFAIL\tgithub.com/x/y\t4m19s\n"
	after := "golangci-lint run ./...\nlib/x.go:3:1: typecheck: undefined: Foo\n" +
		"ok  \tgithub.com/x/spiro\t0.207s\n--- FAIL: TestY (9ms)\nFAIL\tgithub.com/x/y\t4m2s\n"
	require.Equal(t, liftComparisonHash(tag), liftComparisonHash(after))
	require.NotEqual(t, liftComparisonHash(tag), liftComparisonHash(after+"lib/z.go:1:1: another\n"), "a real difference stays one")
	require.NotEqual(t, liftComparisonHash("golangci-lint-v2.13.2 TestG6s\n"), liftComparisonHash("golangci-lint-v2.13.3 TestG6s\n"), "a version is no duration")

	// No names to read (lint only): the same output, a tool install and
	// timings aside, is the tag's red.
	lintTag := "(re)installing /home/u/go/bin/golangci-lint-v2.13.2\nlib/x.go:3:1: typecheck: undefined: Foo\nmake: *** [golint] Error 1\n"
	lintAfter := "lib/x.go:3:1: typecheck: undefined: Foo\nmake: *** [golint] Error 1\n"
	v, note, _ := judgeLiftRegression(&CheckResult{Exit: 2, Log: "t.log", OutputSHA256: "a"}, &CheckResult{Exit: 2, Log: "a.log", OutputSHA256: "b"}, lintTag, lintAfter)
	require.Equal(t, LiftRegressionRedAtTag, v)
	require.Contains(t, note, "failing as it did there")
	require.NotContains(t, note, "the output changed")
}
