package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// R24b: a multi-line ( … ) block of simple commands splits into them; any
// line that is not one keeps the block as written, and says which.
func TestSplitSubshell(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
		refused  string // a substring of the refusal; "" for none
	}{
		{"cortex's shape", "( make lint\ngo vet ./...\ngo test ./...\n)", []string{"make lint", "go vet ./...", "go test ./..."}, ""},
		{
			"blank lines and comments dropped, a trailing comment cut", "(\n  # the gates\n  make lint   # lint\n\n  go test ./... -run 'A|B'\n)",
			[]string{"make lint", "go test ./... -run 'A|B'"},
			"",
		},
		{
			"pipes, redirections, an env prefix, a nested subshell, a here-string", "( go test ./... 2>&1 | tee t.log\nCGO_ENABLED=0 go build ./...\n(cd web && npm test)\ngrep -q x <<< \"$v\" && echo \"a; b\"\nfind . -name '*.tmp' -exec rm {} \\;\n)",
			[]string{"go test ./... 2>&1 | tee t.log", "CGO_ENABLED=0 go build ./...", "(cd web && npm test)", "grep -q x <<< \"$v\" && echo \"a; b\"", "find . -name '*.tmp' -exec rm {} \\;"},
			"",
		},

		{"one line: no block", "( make lint && go test ./... )", nil, ""},
		{"no parentheses", "make lint\ngo test ./...", nil, ""},
		{"two blocks", "( make lint )\n( go test ./... )", nil, ""},
		{"an arithmetic (( ))", "(( 1 +\n2 ))", nil, ""},
		{"a block of one command", "(\n  go test ./...\n  # nothing else\n)", nil, ""},
		{"text after the block", "( make lint\ngo test ./...\n) || true", nil, ""},

		{"cd", "( cd web\nnpm test\n)", nil, "line 1 changes shell state (cd)"},
		{"pushd", "( make\npushd web\n)", nil, "line 2 changes shell state (pushd)"},
		{"export", "( export GOFLAGS=-mod=mod\ngo test ./...\n)", nil, "changes shell state (export)"},
		{"unset", "( unset GOPATH\ngo test ./...\n)", nil, "changes shell state (unset)"},
		{"set -e", "( set -e\nmake lint\ngo test ./...\n)", nil, "changes shell state (set)"},
		{"source", "( source env.sh\ngo test ./...\n)", nil, "changes shell state (source)"},
		{"dot", "( . ./env.sh\ngo test ./...\n)", nil, "changes shell state (.)"},
		{"alias, trap, umask, ulimit, exec, shopt", "( make\ntrap 'rm -f x' EXIT\n)", nil, "changes shell state (trap)"},
		{"local", "( make\nlocal x=1\n)", nil, "changes shell state (local)"},
		{"declare after an operator", "( make\ntrue && declare -x A=1\n)", nil, "changes shell state (declare)"},
		{"a bare assignment", "( GOFLAGS=-mod=mod\ngo test ./...\n)", nil, "changes shell state (a bare NAME=value)"},
		{"a bare quoted assignment", "( make\nMSG=\"a b\"\n)", nil, "changes shell state (a bare NAME=value)"},
		{"a continued line", "( go test \\\n  ./...\nmake lint\n)", nil, "line 1 continues on the next line"},
		{"if", "( if [ -f x ]; then make; fi\nmake lint\n)", nil, "opens a construct (if)"},
		{"a one-line for loop (the lift's extractor makes such blocks)", "( make lint\nfor t in integration bench; do go build -tags $t ./...; done\n)", nil, "line 2 opens a construct (for)"},
		{"while", "( make\nwhile read l; do echo $l; done < f\n)", nil, "opens a construct (while)"},
		{"a do on its own line", "( make\ndo\n)", nil, "opens a construct (do)"},
		{"case", "( make\ncase \"$x\" in (a) echo a;; esac\n)", nil, "opens a construct (case)"},
		{"a brace group", "( make\nmake test || { echo failed; exit 1; }\n)", nil, "opens a construct ({)"},
		{"a function", "( make\nf() { go test ./...; }\n)", nil, "opens a construct (a function definition)"},
		{"a here-doc", "( make\ncat <<EOF > x\n)", nil, "opens a construct (a here-doc <<)"},
		{"an unbalanced quote", "( make\necho \"a\nb\"\n)", nil, "line 2 has unbalanced quotes or parentheses"},
		{"an unbalanced parenthesis", "( make\necho (\necho )\n)", nil, "line 2 has unbalanced quotes or parentheses"},
		{"already joined: &&", "( make lint &&\ngo test ./...\n)", nil, "line 1 is already joined to the next"},
		{"already joined: ||", "( make lint ||\ntrue\n)", nil, "is already joined"},
		{"already joined: ;", "( make lint;\ngo test ./...\n)", nil, "is already joined"},
	} {
		got, refused := splitSubshell(c.in)
		require.Equal(t, c.want, got, c.name)
		if c.refused == "" {
			require.Empty(t, refused, c.name)
		} else {
			require.Contains(t, refused, c.refused, c.name)
		}
	}
}

// The lift's extractor wraps a fence in a ( … ) block only when a line in
// it opens a construct, so a lifted block runs as written (R24a's
// verdict, not R24b's split, is what catches its hidden failures).
func TestSplitSubshell_LiftedBlockRunsAsWritten(t *testing.T) {
	g := extractGates("pc.md", "## Code Validation\n\n```sh\nmake lint\nfor t in a b; do go build -tags $t ./...; done\ngo test ./...\n```\n")
	require.Len(t, g.Commands, 1)
	require.True(t, strings.HasPrefix(g.Regression, "( "))
	cmds, refused := splitSubshell(g.Regression)
	require.Nil(t, cmds)
	require.Contains(t, refused, "opens a construct (for)")
}

// R24b, one run: ( true ⏎ false ⏎ true ) was green, as only its last
// line's exit counted. Split, it is red; the result keeps the configured
// command and the log says how it ran.
func TestRunCheck_SubshellBlockSplit(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "c.log")
	cmd := "( true\nfalse\ntrue )"
	r := RunCheck(ctx(), dir, cmd, log, 0)
	require.Equal(t, 1, r.Exit, "the false line's exit counts")
	require.Equal(t, cmd, r.Cmd, "the recorded cmd is the configured string")
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "[ape] split into 3 commands: the ( … ) block runs as its lines joined by &&, so each line's exit counts, as under set -e: true && false && true\n"), string(data))

	// The last line red: red either way.
	require.Equal(t, 1, RunCheck(ctx(), dir, "( true\ntrue\nfalse\n)", "", 0).Exit)
	require.Equal(t, 0, RunCheck(ctx(), dir, "( true\n# fine\ntrue\n)", "", 0).Exit)

	// A block that does not split runs as written: a cd holds for the
	// lines after it, and only the last exit counts, as before.
	r = RunCheck(ctx(), dir, "( cd /\nfalse\ntrue\n)", log, 0)
	require.Equal(t, 0, r.Exit)
	data, err = os.ReadFile(log)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "[ape] the ( … ) block runs as written, not split into commands: its line 1 changes shell state (cd)\n"), string(data))

	// Not a block: no note at all.
	r = RunCheck(ctx(), dir, "echo hi", log, 0)
	require.Equal(t, 0, r.Exit)
	data, err = os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(data))
}
