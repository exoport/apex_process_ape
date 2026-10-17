package apexv1

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Args templates (D77). Every tools.* entry and human_review take an
// args: template that ape fills into `ape task <skill> --args "…"`:
//
//	human_review: {delegate: apex-accept, args: "--task {task} --change {id}"}
//
// The placeholders are {id} (the change id), {task} (a task id, T<n>),
// {assets} (the change's assets folder) and {change} (its change.md), the
// two paths project-relative. `ape task` splits --args on whitespace and
// types the result after the skill, so a value is one token: ids and paths
// holding whitespace, quotes or a shell metacharacter are refused rather
// than quoted — no quoting survives a typed slash line the same way twice.

// ArgsPlaceholders are the placeholders an args template may use.
var ArgsPlaceholders = []string{"{id}", "{task}", "{assets}", "{change}"}

// ArgsVars are the values an args template is filled with. Task may be
// empty, but only for a template that does not use {task}.
type ArgsVars struct {
	ID     string
	Task   string
	Assets string
	Change string
}

// argsValueRe is one shell-safe token: what an id, a task id or a
// project-relative path may hold.
var argsValueRe = regexp.MustCompile(`^[A-Za-z0-9._/@:+,=-]+$`)

// argsPlaceholderRe is anything that looks like a placeholder.
var argsPlaceholderRe = regexp.MustCompile(`\{[^{}\s]*\}`)

// ValidateArgsTemplate refuses a template that holds a placeholder other
// than ArgsPlaceholders (or a stray brace), or more than one line. An
// empty template is valid: the caller's default applies.
func ValidateArgsTemplate(tmpl string) error {
	if strings.ContainsAny(tmpl, "\r\n") {
		return fmt.Errorf("args %q: must be one line", tmpl)
	}
	for _, ph := range argsPlaceholderRe.FindAllString(tmpl, -1) {
		if !slices.Contains(ArgsPlaceholders, ph) {
			return fmt.Errorf("args %q: unknown placeholder %s (the placeholders are %s)", tmpl, ph, strings.Join(ArgsPlaceholders, ", "))
		}
	}
	rest := tmpl
	for _, ph := range ArgsPlaceholders {
		rest = strings.ReplaceAll(rest, ph, "")
	}
	if strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("args %q: a stray brace (the placeholders are %s)", tmpl, strings.Join(ArgsPlaceholders, ", "))
	}
	return nil
}

// FillArgs fills tmpl with v. It refuses an invalid template, a {task}
// with no task, and a value used that is not one shell-safe token.
func FillArgs(tmpl string, v ArgsVars) (string, error) {
	if err := ValidateArgsTemplate(tmpl); err != nil {
		return "", err
	}
	values := []struct{ ph, val string }{{"{id}", v.ID}, {"{task}", v.Task}, {"{assets}", v.Assets}, {"{change}", v.Change}}
	pairs := make([]string, 0, 2*len(values))
	for _, x := range values {
		if !strings.Contains(tmpl, x.ph) {
			continue
		}
		if x.val == "" {
			return "", fmt.Errorf("args %q: uses %s, and there is no value for it", tmpl, x.ph)
		}
		if !argsValueRe.MatchString(x.val) {
			return "", fmt.Errorf("args %q: the value of %s, %q, is not one shell-safe token (letters, digits and ._/@:+,=- only)", tmpl, x.ph, x.val)
		}
		pairs = append(pairs, x.ph, x.val)
	}
	return strings.Join(strings.Fields(strings.NewReplacer(pairs...).Replace(tmpl)), " "), nil
}

// ArgsVarsFor is the values for change id's folder (project-relative,
// slash-separated: development/changes/<id> in flight, or its archive
// folder) and task, "" when there is none.
func ArgsVarsFor(id, task, folder string) ArgsVars {
	folder = strings.TrimSuffix(filepath.ToSlash(folder), "/")
	return ArgsVars{ID: id, Task: task, Assets: folder + "/assets", Change: folder + "/change.md"}
}
