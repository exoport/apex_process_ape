package apexv1

import (
	"regexp"
	"strconv"
	"strings"
)

// The gate commands a v0 project declared in project-context.md's
// `## Code Validation` become AGENTS.md's command list and the regression
// command (`regression:` in _apex/config.yaml, backlog 32). The extraction
// is mechanical and says what it could not take.

// LiftGates is what the lift took from `## Code Validation`.
type LiftGates struct {
	// Source is the file and line of the section, "" when there is none.
	Source   string   `json:"source,omitempty"`
	Commands []string `json:"commands,omitempty"`
	// Regression is the runnable commands joined with &&.
	Regression string `json:"regression,omitempty"`
	// NotRunnable are commands with a placeholder (<board>, {name}, …):
	// listed in AGENTS.md, left out of the regression command.
	NotRunnable []string `json:"not_runnable,omitempty"`
}

var (
	codeSpanRe     = regexp.MustCompile("`([^`]+)`")
	listItemRe     = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	tableRowRe     = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	placeholderRe  = regexp.MustCompile(`<[^<>\s][^<>]*>|\{\{?[A-Za-z_][^{}]*\}?\}|\.{3}|…`)
	commandSpanRe  = regexp.MustCompile(`^(?:\./|[A-Za-z0-9_][A-Za-z0-9_.+-]*\s+\S)`)
	compoundLineRe = regexp.MustCompile(`^\s*(?:for|while|until|if|case|do|done|then|else|elif|fi|esac|\{|\})\b`)
	fileSpanRe     = regexp.MustCompile(`^[\w./-]+\.(?:md|ya?ml|json|txt|go|sh|py)$`)
)

// extractGates reads the `## Code Validation` section of doc (any heading
// level, case-insensitive) up to the next heading of its level or above.
// A fenced block is one unit: its lines joined, or kept as a block when
// they hold a shell compound (for … done); outside fences, a list item's
// or a table row's code spans that read as commands (two words, or a
// ./path) are taken. Duplicates are dropped, order kept.
func extractGates(file, doc string) LiftGates {
	var g LiftGates
	lines := splitLines(doc)
	start, level := -1, 0
	for i, l := range lines {
		if lv, t := headingLevel(l); lv > 0 && strings.EqualFold(strings.TrimSpace(t), "Code Validation") {
			start, level = i, lv
			g.Source = file + ":" + strconv.Itoa(l.N)
			break
		}
	}
	if start < 0 {
		return g
	}
	seen := map[string]bool{}
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		g.Commands = append(g.Commands, c)
		if placeholderRe.MatchString(c) {
			g.NotRunnable = append(g.NotRunnable, c)
		}
	}
	var fence []string
	inFence := false
	flush := func() {
		var cmds []string
		compound := false
		cont := ""
		for _, l := range fence {
			t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "$ "))
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if compoundLineRe.MatchString(t) {
				compound = true
			}
			if head, ok := strings.CutSuffix(t, "\\"); ok {
				cont += head + " "
				continue
			}
			cmds = append(cmds, cont+t)
			cont = ""
		}
		if compound {
			add("( " + strings.Join(cmds, "\n") + "\n)")
		} else {
			for _, c := range cmds {
				add(c)
			}
		}
		fence = nil
	}
	for _, l := range lines[start+1:] {
		t := strings.TrimSpace(l.Text)
		isFence := strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
		if isFence {
			if inFence {
				flush()
			}
			inFence = !inFence
			continue
		}
		if inFence {
			fence = append(fence, l.Text)
			continue
		}
		if lv, _ := headingLevel(l); lv > 0 && lv <= level {
			break
		}
		if !listItemRe.MatchString(l.Text) && !tableRowRe.MatchString(l.Text) {
			continue
		}
		for _, m := range codeSpanRe.FindAllStringSubmatch(l.Text, -1) {
			c := strings.TrimSpace(m[1])
			if commandSpanRe.MatchString(c) && !fileSpanRe.MatchString(c) {
				add(c)
			}
		}
	}
	var run []string
	for _, c := range g.Commands {
		if !placeholderRe.MatchString(c) {
			run = append(run, c)
		}
	}
	g.Regression = strings.Join(run, " && ")
	return g
}

// withCommands puts the commands under AGENTS.md's `## Commands` heading
// (after a comment line right below it), or appends the section.
func withCommands(doc string, g LiftGates) string {
	if len(g.Commands) == 0 {
		return doc
	}
	var items []string
	for _, c := range g.Commands {
		if strings.Contains(c, "\n") {
			items = append(items, "- ```\n"+indent(c, "  ")+"\n  ```")
			continue
		}
		items = append(items, "- `"+c+"`")
	}
	block := strings.Join(items, "\n") + "\n"
	note := "<!-- lifted from " + g.Source + " (## Code Validation) at " + LiftTag + " -->\n"
	lines := strings.SplitAfter(doc, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "## Commands" {
			continue
		}
		at := i + 1
		if at < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[at]), "<!--") && strings.HasSuffix(strings.TrimSpace(lines[at]), "-->") {
			at++
		}
		out := strings.Join(lines[:at], "") + note + block + strings.Join(lines[at:], "")
		return out
	}
	if !strings.HasSuffix(doc, "\n") && doc != "" {
		doc += "\n"
	}
	return doc + "\n## Commands\n" + note + block
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(s, "\n", "\n"+pre)
}
