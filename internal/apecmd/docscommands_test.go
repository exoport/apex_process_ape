package apecmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Q150 (make docs-commands-check): every `ape …` command line written in
// README.md and docs/ resolves on ape's command tree, its flags included.
// docs-cli-check keeps the generated reference in step with the tree; this
// keeps the hand-written pages in step with it: a page naming a verb that
// moved or a flag that was renamed fails here, not in a reader's terminal.
//
// What counts as a command: a line of a fenced block, or an inline code
// span, split at && || ; | and $( — each part that starts with `ape`, after
// a `$ ` prompt and VAR=value or `env -u X` prefixes. Its words are walked
// down the tree, hidden commands included (notify, hook, gen-docs are
// real), to the first flag or placeholder (<x>, [x], {x}, $X, …); then each
// --flag must be one the command takes, its own or inherited.
//
// Two kinds of line are allowed to name what the tree does not have, and
// only these:
//   - a retired APEX v0 command (retiredV0Commands) in a paragraph, or
//     under a heading, that says "retired" or "v0": the docs name them to
//     say they are gone, or for a v0 project's pinned ape;
//   - a command in a fenced block that the same block shows failing with
//     cobra's "Error: unknown flag" / "Error: unknown command".
func TestDocsCommandsResolve(t *testing.T) {
	root := newRootCmd()
	files := docsMarkdownFiles(t, filepath.Join("..", ".."))
	require.NotEmpty(t, files)
	var problems []string
	seen := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		rel, _ := filepath.Rel(filepath.Join("..", ".."), f)
		for _, c := range docsCommands(string(data)) {
			seen++
			if p := resolveDocCommand(root, c); p != "" {
				problems = append(problems, fmt.Sprintf("%s:%d: %s    [%s]", filepath.ToSlash(rel), c.line, p, truncate(c.text, 100)))
			}
		}
	}
	require.Greater(t, seen, 100, "the docs name hundreds of ape commands: the extraction found too few")
	require.Empty(t, problems, "ape commands in the docs that do not resolve on the command tree:\n%s", strings.Join(problems, "\n"))
}

// retiredV0Commands are APEX v0's top-level commands ape v1 retired
// (AGENTS.md, backlog 38). TestDocsCommands_RetiredAreGone keeps the list
// honest: one that comes back is no longer retired.
var retiredV0Commands = []string{
	"pipeline", "story", "sprint", "adr", "pattern", "capability", "feature", "trait", "bootstrap",
	"registry", "governance", "deferred", "memory", "context", "planning", "doc", "release",
}

func TestDocsCommands_RetiredAreGone(t *testing.T) {
	root := newRootCmd()
	for _, name := range retiredV0Commands {
		require.Nil(t, childNamed(root, name), "ape %s is in the tree: take it off retiredV0Commands", name)
	}
}

// docCommand is one `ape …` found in a page.
type docCommand struct {
	text string
	line int
	// retiredOK: the paragraph or heading says the command is retired or v0's.
	retiredOK bool
	// failureShown: the fenced block shows cobra refusing it.
	failureShown bool
}

var (
	inlineCode   = regexp.MustCompile("(`+)(.+?)`+")
	retiredWords = regexp.MustCompile(`(?i)\bretired\b|\bv0\b`)
	cmdSeparator = regexp.MustCompile(`\s*(?:&&|\|\||;|\||\$\()\s*`)
	envPrefix    = regexp.MustCompile(`^(?:env\s+(?:-u\s+\S+\s+)*)?(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*`)
	versionWord  = regexp.MustCompile(`^v?\d+\.\d+(\.\d+)?\S*$`)
)

// docsCommands extracts the `ape …` commands of one markdown page.
func docsCommands(page string) []docCommand {
	lines := strings.Split(strings.ReplaceAll(page, "\r\n", "\n"), "\n")
	var out []docCommand
	heading := ""
	inFence := false
	var fence []docCommand // the open block's commands, flagged when it shows a refusal
	paraStart := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if inFence {
				out = append(out, fence...)
				fence = nil
			}
			inFence = !inFence
			paraStart = i + 1
			continue
		}
		if inFence {
			if strings.HasPrefix(trimmed, "Error: unknown flag") || strings.HasPrefix(trimmed, "Error: unknown command") {
				for j := range fence {
					fence[j].failureShown = true
				}
			}
			for _, c := range commandsIn(trimmed) {
				fence = append(fence, docCommand{text: c, line: i + 1, retiredOK: retiredWords.MatchString(heading)})
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			heading = trimmed
		}
		if trimmed == "" {
			paraStart = i + 1
			continue
		}
		para := paragraphAt(lines, paraStart)
		for _, m := range inlineCode.FindAllStringSubmatch(line, -1) {
			// A table cell escapes its pipes, inside code spans too.
			for _, c := range commandsIn(strings.ReplaceAll(m[2], `\|`, "|")) {
				out = append(out, docCommand{
					text: c, line: i + 1,
					retiredOK: retiredWords.MatchString(para) || retiredWords.MatchString(heading),
				})
			}
		}
	}
	return append(out, fence...)
}

// paragraphAt is the run of non-blank lines from start: a paragraph, a
// list or a table.
func paragraphAt(lines []string, start int) string {
	end := start
	for end < len(lines) && strings.TrimSpace(lines[end]) != "" {
		end++
	}
	return strings.Join(lines[start:end], "\n")
}

// commandsIn splits one line or code span into the `ape …` commands it holds.
func commandsIn(text string) []string {
	var out []string
	for _, part := range cmdSeparator.Split(text, -1) {
		part = strings.TrimSpace(part)
		part = strings.TrimSpace(strings.TrimPrefix(part, "$ "))
		part = envPrefix.ReplaceAllString(part, "")
		if part == "ape" || strings.HasPrefix(part, "ape ") {
			out = append(out, part)
		}
	}
	return out
}

// resolveDocCommand walks c down the tree and checks its flags; "" when it
// resolves.
func resolveDocCommand(root *cobra.Command, c docCommand) string {
	words := shellWords(c.text)[1:]
	cmd := root
	i := 0
	for ; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") || isPlaceholder(w) {
			break
		}
		if !hasSubcommands(cmd) {
			break
		}
		if next := childNamed(cmd, w); next != nil {
			cmd = next
			continue
		}
		if w == "help" || (cmd == root && versionWord.MatchString(w)) {
			return ""
		}
		if c.failureShown || (cmd == root && c.retiredOK && slices.Contains(retiredV0Commands, w)) {
			return ""
		}
		return "unknown command: " + strings.TrimSpace(cmd.CommandPath()+" "+w)
	}
	for _, w := range words[i:] {
		if w == "--" || w == ">" || w == "2>&1" || strings.HasPrefix(w, ">") {
			break
		}
		if !strings.HasPrefix(w, "--") || len(w) == 2 {
			continue
		}
		name, _, _ := strings.Cut(w[2:], "=")
		if name == "" || isPlaceholder(name) || hasFlag(cmd, name) || c.failureShown {
			continue
		}
		return "unknown flag --" + name + " on: " + cmd.CommandPath()
	}
	return ""
}

func hasSubcommands(c *cobra.Command) bool {
	for _, s := range c.Commands() {
		if s.Name() != "help" && s.Name() != "completion" {
			return true
		}
	}
	return false
}

// childNamed is c's subcommand called name or one of its aliases, hidden
// ones included.
func childNamed(c *cobra.Command, name string) *cobra.Command {
	for _, s := range c.Commands() {
		if s.Name() == name || slices.Contains(s.Aliases, name) {
			return s
		}
	}
	return nil
}

// hasFlag: c's own flags, the persistent ones it inherits, and the two
// cobra adds when it executes: help everywhere, version where a Version is
// set (the root's is set by Execute; the aboard mount carries its own).
func hasFlag(c *cobra.Command, name string) bool {
	if name == "help" || (name == "version" && (!c.HasParent() || c.Version != "")) {
		return true
	}
	return c.Flags().Lookup(name) != nil || c.PersistentFlags().Lookup(name) != nil || c.InheritedFlags().Lookup(name) != nil
}

func isPlaceholder(w string) bool {
	return strings.ContainsAny(w[:1], "<[{$") || strings.Contains(w, "…") || strings.Contains(w, "...")
}

// shellWords splits a command line as a shell would, near enough: quotes
// group, a backslash escapes, an unquoted # starts a comment, and a word
// that only closes a $( … ) loses its ")". Unbalanced quotes fall back to
// whitespace.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == '#' && !inWord:
			return closeSubshell(words)
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return closeSubshell(strings.Fields(s))
	}
	if inWord {
		words = append(words, cur.String())
	}
	return closeSubshell(words)
}

func closeSubshell(words []string) []string {
	for i, w := range words {
		if strings.HasSuffix(w, ")") && !strings.Contains(w, "(") {
			words[i] = strings.TrimSuffix(w, ")")
		}
	}
	return slices.DeleteFunc(words, func(w string) bool { return w == "" })
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// docsMarkdownFiles is README.md and every page under docs/, sorted.
func docsMarkdownFiles(t *testing.T, repo string) []string {
	t.Helper()
	files := []string{filepath.Join(repo, "README.md")}
	require.NoError(t, filepath.WalkDir(filepath.Join(repo, "docs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			files = append(files, p)
		}
		return err
	}))
	slices.Sort(files[1:])
	return files
}

// The checker's own rules, on lines that exercise each.
func TestDocsCommands_Extraction(t *testing.T) {
	root := newRootCmd()
	page := strings.Join([]string{
		"# Page", // 1
		"Run `ape status --json`, then `ape change new add-wave --milestone M1`.",
		"A typo: `ape stauts` and `ape status --jsn`.",
		"In a table | `ape hook pre-commit \\|\\| true` |",
		"A hidden one: `ape notify --event Stop`; placeholders: `ape change <verb> <id>`.",
		"", // 6
		"`ape pipeline run` is gone.",
		"",
		"It was retired:", // 9
		"`ape pipeline run`.",
		"",
		"```sh", // 12
		"$ APE_X=1 ape status --json # a comment --nope",
		"out=$(ape status --wait 9m --json)",
		"ape task <skill> --programmatic",
		"Error: unknown flag: --programmatic",
		"```",
		"## APEX v0 project findings", // 18
		"| row | `ape registry verify --all` |",
	}, "\n")
	var got []string
	for _, c := range docsCommands(page) {
		got = append(got, fmt.Sprintf("%d %s => %s", c.line, c.text, resolveDocCommand(root, c)))
	}
	require.Equal(t, []string{
		"2 ape status --json => ",
		"2 ape change new add-wave --milestone M1 => ",
		"3 ape stauts => unknown command: ape stauts",
		"3 ape status --jsn => unknown flag --jsn on: ape status",
		"4 ape hook pre-commit => ",
		"5 ape notify --event Stop => ",
		"5 ape change <verb> <id> => ",
		"7 ape pipeline run => unknown command: ape pipeline",
		"10 ape pipeline run => ",
		"13 ape status --json # a comment --nope => ",
		"14 ape status --wait 9m --json) => ",
		"15 ape task <skill> --programmatic => ",
		"19 ape registry verify --all => ",
	}, got)
}
