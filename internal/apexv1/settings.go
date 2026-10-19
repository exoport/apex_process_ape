package apexv1

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// HookSpec is one hook ape registers: the event, the tool matcher ("" for
// none), the command and its timeout in seconds (0 for Claude Code's).
type HookSpec struct {
	Event, Matcher, Command string
	Timeout                 int
}

// APEHooks are the hooks `ape project init` merges into
// .claude/settings.json, in the order it writes them.
func APEHooks() []HookSpec {
	return []HookSpec{
		{"SessionStart", "", HookCmdSessionStart, 0},
		{"Stop", "", HookCmdStop, StopHookTimeout},
		{"SubagentStop", "", HookCmdStop, StopHookTimeout},
		{"PreToolUse", PreToolMatcher, HookCmdPreTool, 0},
		promptHook,
	}
}

// promptHook records the person's prompts (E2, D178): never inline — in a
// session ape launches it records nothing (T7) — and, missing, a notice
// (hooks.prompt-absent), not a finding: without it a person act from
// Claude Code is refused, as before, and nothing else changes.
var promptHook = HookSpec{"UserPromptSubmit", "", HookCmdUserPrompt, 0}

// requiredHooks are the hooks a missing or altered one of is a finding.
func requiredHooks() []HookSpec {
	return slices.DeleteFunc(APEHooks(), func(h HookSpec) bool { return h == promptHook })
}

// Inline hooks (backlog 31).
//
// `ape task` passes ape's hooks in its --settings as well, so a session it
// launches has them whatever .claude/settings.json says. The inline copy
// carries InlineHookFlag and the spawn carries EnvInlineHooks; a hook
// started WITHOUT the flag in a process WITH the variable is the settings
// file's copy of a hook the inline copy already runs, and does nothing.
// Exactly one copy acts on every event, whether or not Claude Code
// deduplicates the two — the stop hook's consecutive-block counter and its
// verdict log, which `ape task` matches one-to-one with Stops, depend on
// that.
//
// Measured on Claude Code 2.1.301 (SessionStart, the one hook event that
// fires with no model turn): a command identical in .claude/settings.json
// and --settings ran ONCE — deduplicated by command string, regardless of
// timeout, matcher or async — and two different commands both ran. ape
// does not rely on the dedup: the docs promise it only "in more than one
// settings file", and a changed rule would silently double every Stop.
const (
	InlineHookFlag = "--inline"
	EnvInlineHooks = "APE_V1_HOOKS"
)

// InlineHooks are APEHooks as `ape task` injects them.
func InlineHooks() []HookSpec {
	out := requiredHooks()
	for i := range out {
		out[i].Command += " " + InlineHookFlag
	}
	return out
}

// hookEntry is one hook command found in a settings file, with the group
// that holds it.
type hookEntry struct {
	group, hook int
	matcher     string
	entry       map[string]any
}

// findHook returns every entry of event whose command is command.
func findHook(groups []any, command string) []hookEntry {
	var out []hookEntry
	for gi, g := range groups {
		gm, _ := g.(map[string]any)
		matcher, _ := gm["matcher"].(string)
		hs, _ := gm["hooks"].([]any)
		for hi, h := range hs {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == command {
				out = append(out, hookEntry{group: gi, hook: hi, matcher: matcher, entry: hm})
			}
		}
	}
	return out
}

// alteration says how a registered ape hook differs from what init
// writes, or "" when it is intact. Only what changes the hook's effect
// counts: a different matcher, a type other than command, async (an async
// hook cannot block), or a stop-hook timeout below the checks' budget.
func alteration(w HookSpec, e hookEntry) string {
	if e.matcher != w.Matcher {
		if w.Matcher == "" {
			return fmt.Sprintf("matcher %q, want none", e.matcher)
		}
		return fmt.Sprintf("matcher %q, want %q", e.matcher, w.Matcher)
	}
	if t, _ := e.entry["type"].(string); t != "command" {
		return fmt.Sprintf("type %q, want command", t)
	}
	if async, _ := e.entry["async"].(bool); async {
		return "async, so it cannot block"
	}
	if w.Timeout > 0 {
		if t, ok := e.entry["timeout"].(float64); !ok || int(t) < w.Timeout {
			return fmt.Sprintf("timeout %v, want %d", e.entry["timeout"], w.Timeout)
		}
	}
	return ""
}

// readSettings reads a settings.json into a map; an absent file is empty.
func readSettings(path string) (map[string]any, error) {
	root := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return root, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, &FailError{Code: "settings.invalid-json", Msg: path + " is not valid JSON (" + err.Error() + ") — fix it, then run `ape project init` again"}
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func writeSettings(path string, root map[string]any) error {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, string(out)+"\n")
}

// MergeHooks adds ape's hooks to a settings.json, keeping every other key
// and hook, and restores one that was altered (see alteration): the
// altered entry is removed and the hook written as init writes it. An
// intact hook is left as it is, so a second run changes nothing. Returns
// "<Event>: <command>" for each one added, with " (restored)" for one
// that was altered.
func MergeHooks(path string) ([]string, error) {
	root, err := readSettings(path)
	if err != nil {
		return nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	var added []string
	for _, w := range APEHooks() {
		groups, _ := hooks[w.Event].([]any)
		found := findHook(groups, w.Command)
		intact := slices.ContainsFunc(found, func(e hookEntry) bool { return alteration(w, e) == "" })
		if intact {
			continue
		}
		note := ""
		if len(found) > 0 {
			groups = removeHookEntries(groups, found)
			note = " (restored)"
		}
		cmd := map[string]any{"type": "command", "command": w.Command}
		if w.Timeout > 0 {
			cmd["timeout"] = w.Timeout
		}
		group := map[string]any{"hooks": []any{cmd}}
		if w.Matcher != "" {
			group["matcher"] = w.Matcher
		}
		hooks[w.Event] = append(groups, group)
		added = append(added, w.Event+": "+w.Command+note)
	}
	if len(added) == 0 {
		return nil, nil
	}
	root["hooks"] = hooks
	return added, writeSettings(path, root)
}

// removeHookEntries drops the found entries, and any group they leave
// with no hooks.
func removeHookEntries(groups []any, found []hookEntry) []any {
	drop := map[[2]int]bool{}
	for _, e := range found {
		drop[[2]int{e.group, e.hook}] = true
	}
	out := make([]any, 0, len(groups))
	for gi, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			out = append(out, g)
			continue
		}
		hs, _ := gm["hooks"].([]any)
		kept := make([]any, 0, len(hs))
		for hi, h := range hs {
			if !drop[[2]int{gi, hi}] {
				kept = append(kept, h)
			}
		}
		if len(kept) == 0 && len(hs) > 0 {
			continue
		}
		gm["hooks"] = kept
		out = append(out, gm)
	}
	return out
}

// CheckHooks reports each of ape's hooks missing from, or altered in,
// .claude/settings.json (backlog 31). `ape task` injects them anyway; this
// is for every other session in the project — a person's, or a pipeline's.
func (p *Project) CheckHooks() []Finding {
	const file = ".claude/settings.json"
	root, err := readSettings(p.SettingsJSON())
	if err != nil {
		if _, ok := errors.AsType[*FailError](err); ok {
			return []Finding{{File: file, Message: "settings.json is not valid JSON — fix it, then run `ape project init`"}}
		}
		return []Finding{{File: file, Message: "settings.json is unreadable (" + err.Error() + ") — fix it, then run `ape project init`"}}
	}
	hooks, _ := root["hooks"].(map[string]any)
	var out []Finding
	for _, w := range requiredHooks() {
		groups, _ := hooks[w.Event].([]any)
		found := findHook(groups, w.Command)
		if len(found) == 0 {
			out = append(out, Finding{File: file, Message: fmt.Sprintf("hook %#q is not registered for %s — run `ape project init` to merge ape's hooks back", w.Command, w.Event)})
			continue
		}
		if slices.ContainsFunc(found, func(e hookEntry) bool { return alteration(w, e) == "" }) {
			continue
		}
		out = append(out, Finding{File: file, Message: fmt.Sprintf("hook %#q for %s is altered (%s) — run `ape project init` to restore it", w.Command, w.Event, alteration(w, found[0]))})
	}
	return out
}

// RequiredHookCount is how many hooks CheckHooks requires.
func RequiredHookCount() int { return len(requiredHooks()) }

// PromptHookProblem says why the UserPromptSubmit hook does not record
// the person's prompts — missing or altered in .claude/settings.json —
// or "" when it does (T10).
func (p *Project) PromptHookProblem() string {
	root, err := readSettings(p.SettingsJSON())
	if err != nil {
		return ".claude/settings.json is unreadable, so no prompt is recorded"
	}
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks[promptHook.Event].([]any)
	found := findHook(groups, promptHook.Command)
	if len(found) == 0 {
		return fmt.Sprintf("hook %#q is not registered for %s", promptHook.Command, promptHook.Event)
	}
	if slices.ContainsFunc(found, func(e hookEntry) bool { return alteration(promptHook, e) == "" }) {
		return ""
	}
	return fmt.Sprintf("hook %#q for %s is altered (%s)", promptHook.Command, promptHook.Event, alteration(promptHook, found[0]))
}

// promptHookNotices is the hooks.prompt-absent notice, or none.
func (p *Project) promptHookNotices() []Diagnostic {
	why := p.PromptHookProblem()
	if why == "" {
		return nil
	}
	return []Diagnostic{notice(Diagnostic{Code: CodeHooksPromptAbsent, File: ".claude/settings.json", Message: why +
		": the person's prompts are not recorded, so a person act (approve, answer, accept …) from a Claude Code session is refused (E2) — " +
		"`ape project init` merges the hook back"})}
}
