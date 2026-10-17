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
