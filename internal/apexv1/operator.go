package apexv1

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// The operator interface (rc.7, D171): apex-orchestrator becomes the
// operator's skill, opened with /apex-orchestrator in a person's own
// Claude Code session, and the loop is apex-loop. What a project carries
// for it:
//
//   - models: a row for apex-loop and none for apex-orchestrator, which runs
//     in the person's session with the model they chose (D175, D176). init
//     and the lift write apex-loop's (modelDefaultsYAML); until seq-07 has
//     run, a models.apex-orchestrator row is read as apex-loop's (Q96);
//   - ape's hooks in .claude/settings.json, as init merges them (the hook
//     set itself is MergeHooks'; rc.7 adds UserPromptSubmit);
//   - AGENTS.md's OperatorLine (A11, A13).
//
// `ape project operator-interface` (the framework's v1.0.0_seq-07, Q97)
// brings a project made before rc.7 there in one commit.
//
// How ape knows seq-07 ran: from the config itself, not a marker. The one
// state seq-07 leaves behind it is "no models.apex-orchestrator row, or
// one beside an apex-loop row" — a marker file would be lost by a fresh
// clone (_apex/state/ is ignored but for its whitelist) or drift from the
// config it describes. So: a models.apex-orchestrator row with no
// models.apex-loop row is the pre-seq-07 state, read as apex-loop's with a
// warning; any other models.apex-orchestrator row is a leftover beside
// apex-loop's, which wins, and a project check notice. Both readings name
// the same row the same way, so a project that drifted back (a person
// re-added the old row alone) is read as un-migrated, which seq-07 fixes.

// OperatorInterfaceSubject is the commit `ape project operator-interface`
// makes: the framework's v1.0.0_seq-07 migration.
const OperatorInterfaceSubject = "apex: operator interface (seq-07)"

// OperatorLine is AGENTS.md's line naming the operator's interface (A11,
// A13): init's and the lift's skeleton carry it, seq-07 adds it.
const OperatorLine = "Talk to APEX: type `/apex-orchestrator` in Claude Code."

// rc.7 (rc7-rename).
const (
	// CodeOperatorInterfacePending: `ape project operator-interface
	// --check` while seq-07 has work left (Q97).
	CodeOperatorInterfacePending = "project.operator-interface-pending"
	// CodeModelsOrchestratorRow: project check's notice for a
	// models.apex-orchestrator row (Q96). It was models.operator-row until
	// v1.3, kept as its alias (CodeAliases, D510).
	CodeModelsOrchestratorRow = "models.orchestrator-row"
)

// LoopModel is the models: entry apex-loop's session reads: models.apex-loop,
// else — before seq-07 (Q96) — models.apex-orchestrator, with legacy true.
func LoopModel(models map[string]SkillModel) (entry SkillModel, legacy bool) {
	if m, ok := models[OrchestratorSkill]; ok {
		return m, false
	}
	if m, ok := models[OperatorSkill]; ok {
		return m, true
	}
	return SkillModel{}, false
}

// LoopModelWarning is the warning a session of apex-loop prints when its
// model comes from models.apex-orchestrator (Q96).
const LoopModelWarning = "warning: _apex/config.yaml has a models.apex-orchestrator row and none for apex-loop: it is read as apex-loop's until the operator-interface migration moves it" +
	" — run `ape project operator-interface` (the framework's v1.0.0_seq-07), which renames the row byte for byte"

// hasOperatorLine reports whether AGENTS.md names the operator's slash
// command on a line outside fenced code: the reference counts, not the
// sentence, as for the project-context line (Q38).
func hasOperatorLine(doc string) bool {
	for _, l := range splitLines(doc) {
		if !l.InCode && strings.Contains(l.Text, "/"+OperatorSkill) {
			return true
		}
	}
	return false
}

// withOperatorLine inserts OperatorLine into an AGENTS.md that lacks it:
// after the project-context line (outside code) when there is one, else
// after its first H1, else at the top, set off by blank lines. Nothing
// else changes.
func withOperatorLine(doc, dev string) string {
	at := 0
	for _, l := range splitLines(doc) {
		if !l.InCode && hasProjectContext(l.Text, dev) {
			at = l.N
			break
		}
	}
	if at == 0 {
		for _, l := range splitLines(doc) {
			if lv, _ := headingLevel(l); lv == 1 {
				at = l.N
				break
			}
		}
	}
	line := OperatorLine + "\n"
	raw := strings.SplitAfter(doc, "\n")
	blank := func(i int) bool { return i >= len(raw) || strings.TrimSpace(raw[i]) == "" }
	if at == 0 {
		if doc == "" {
			return line
		}
		if blank(0) {
			return line + doc
		}
		return line + "\n" + doc
	}
	head := strings.Join(raw[:at], "")
	if !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	out := head + "\n" + line
	if !blank(at) {
		out += "\n"
	}
	return out + strings.Join(raw[at:], "")
}

// operatorRow is where models.apex-orchestrator sits in a config's text,
// and what seq-07 does with it.
type operatorRow struct {
	// key is the row's key node; nil when there is no such row.
	key *yaml.Node
	// loop: models.apex-loop exists too, so the row is removed; else
	// it is renamed.
	loop bool
	// flow: models: is a flow mapping, which only a person edits.
	flow bool
}

// findOperatorRow locates models.apex-orchestrator in a config's text.
func findOperatorRow(data []byte) (operatorRow, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return operatorRow{}, err
	}
	models := topLevelModels(&doc)
	if models == nil || models.Kind != yaml.MappingNode {
		return operatorRow{}, nil
	}
	row := operatorRow{flow: models.Style&yaml.FlowStyle != 0}
	for i := 0; i+1 < len(models.Content); i += 2 {
		switch models.Content[i].Value {
		case OperatorSkill:
			row.key = models.Content[i]
		case OrchestratorSkill:
			row.loop = true
		}
	}
	return row, nil
}

// moveOperatorRow is seq-07's edit of the config's text (Q97): the
// models.apex-orchestrator key renamed to apex-loop, its value kept byte
// for byte — or, when models.apex-loop exists (it wins), the row's lines
// removed. Every other byte is kept. The result is checked by parsing it:
// the models: block must read as the move says, the rest of the document
// as before.
func moveOperatorRow(data []byte) ([]byte, error) {
	row, err := findOperatorRow(data)
	if err != nil || row.key == nil {
		return data, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	n := row.key.Line - 1
	if n < 0 || n >= len(lines) {
		return nil, errors.New("models.apex-orchestrator is not where the parser put it")
	}
	if row.loop {
		lines, err = removeOperatorRow(lines, n, row)
	} else {
		lines, err = renameOperatorKey(lines, n, row)
	}
	if err != nil {
		return nil, err
	}
	out := []byte(strings.Join(lines, ""))
	if err := checkOperatorMove(data, out, row.loop); err != nil {
		return nil, err
	}
	return out, nil
}

// removeOperatorRow removes the row at line n (models.apex-loop exists
// and wins): the key's line and every more-indented line under it.
func removeOperatorRow(lines []string, n int, row operatorRow) ([]string, error) {
	if row.flow {
		return nil, errors.New("models: is a flow mapping ({…}), so its apex-orchestrator entry is not removed by line")
	}
	indent := row.key.Column - 1
	end := n + 1
	for end < len(lines) {
		l := strings.TrimRight(lines[end], "\r\n")
		if strings.TrimSpace(l) == "" || len(l)-len(strings.TrimLeft(l, " \t")) <= indent {
			break
		}
		end++
	}
	return append(lines[:n], lines[end:]...), nil
}

// renameOperatorKey renames the key at line n to apex-loop, every other
// byte of the line kept.
func renameOperatorKey(lines []string, n int, row operatorRow) ([]string, error) {
	col := row.key.Column - 1
	l := lines[n]
	name := OperatorSkill
	if col < len(l) && (l[col] == '"' || l[col] == '\'') {
		col++
	}
	if col > len(l) || !strings.HasPrefix(l[col:], name) {
		return nil, errors.New("models.apex-orchestrator's key is not where the parser put it")
	}
	lines[n] = l[:col] + OrchestratorSkill + l[col+len(name):]
	return lines, nil
}

// checkOperatorMove parses before and after: after's models has no
// apex-orchestrator, apex-loop as before had it (or before's
// apex-orchestrator, when it had none), and everything else unchanged.
func checkOperatorMove(before, after []byte, removed bool) error {
	var b, a map[string]any
	if err := yaml.Unmarshal(before, &b); err != nil {
		return err
	}
	if err := yaml.Unmarshal(after, &a); err != nil {
		return errors.New("the edited config does not parse: " + err.Error())
	}
	bm, _ := b["models"].(map[string]any)
	if bm == nil {
		return errors.New("models: did not read as a mapping")
	}
	want := map[string]any{}
	maps.Copy(want, bm)
	if !removed {
		want[OrchestratorSkill] = bm[OperatorSkill]
	}
	delete(want, OperatorSkill)
	b["models"] = want
	if !reflect.DeepEqual(a, b) {
		return errors.New("the edited config does not read as the move")
	}
	return nil
}

// OperatorInterfaceResult is what `ape project operator-interface`
// reports: what it did, or under --check what is left (Q97).
type OperatorInterfaceResult struct {
	// Models is what happens to models.apex-orchestrator: "moved" (renamed
	// to apex-loop, its value kept), "removed" (apex-loop's row wins), or
	// "" when there is none.
	Models string `json:"models,omitempty"`
	// Hooks are ape's hooks merged into .claude/settings.json, as init
	// merges them ("<Event>: <command>", " (restored)" for an altered
	// one); [] when every one is there.
	Hooks []string `json:"hooks"`
	// AgentsLine: AGENTS.md gains (or lacks) OperatorLine.
	AgentsLine bool `json:"agents_line"`
	// GitIgnore is what happens to the root .gitignore's .claude/ rules
	// (D203, Q114): "added" (the shape's lines appended, the file created
	// when absent) or "rewritten" (a whole-folder .claude ignore replaced
	// in place); "" when the shape is there.
	GitIgnore string `json:"gitignore,omitempty"`
	// Revealed are files under .claude/ the rewrite un-ignored that the
	// commit does not carry (a folder per skill).
	Revealed []string `json:"revealed,omitempty"`
	// Commit is the one commit, OperatorInterfaceSubject; "" when nothing
	// was left to do.
	Commit string `json:"commit,omitempty"`
}

// Pending reports whether the result has anything to do.
func (r *OperatorInterfaceResult) Pending() bool {
	return r.Models != "" || len(r.Hooks) > 0 || r.AgentsLine || r.GitIgnore != ""
}

// operatorInterfacePlan is what seq-07 would do, writing nothing.
func (p *Project) operatorInterfacePlan() (*OperatorInterfaceResult, error) {
	res := &OperatorInterfaceResult{Hooks: []string{}}
	if data, err := os.ReadFile(ConfigPath(p.Root)); err == nil {
		row, err := findOperatorRow(data)
		if err != nil {
			return res, err // Open has refused an unparseable config already
		}
		switch {
		case row.key != nil && row.loop:
			res.Models = "removed"
		case row.key != nil:
			res.Models = "moved"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	for _, f := range p.CheckHooks() {
		res.Hooks = append(res.Hooks, f.Message)
	}
	doc, err := readFileString(p.AgentsMD())
	res.AgentsLine = err == nil && !hasOperatorLine(doc)
	if res.GitIgnore, err = ClaudeIgnorePending(p.Root); err != nil {
		return res, err
	}
	return res, nil
}

// OperatorInterfaceCheck is `ape project operator-interface --check`, the
// migration's check: nil when nothing is left, else
// project.operator-interface-pending naming what is (exit 1). It writes
// nothing and needs no git.
func (p *Project) OperatorInterfaceCheck() (*OperatorInterfaceResult, error) {
	res, err := p.operatorInterfacePlan()
	if err != nil || !res.Pending() {
		return res, err
	}
	var left []string
	switch res.Models {
	case "moved":
		left = append(left, "models.apex-orchestrator to move to models.apex-loop")
	case "removed":
		left = append(left, "models.apex-orchestrator to remove (models.apex-loop is there)")
	}
	if len(res.Hooks) > 0 {
		left = append(left, strings.Join(res.Hooks, "; "))
	}
	if res.AgentsLine {
		left = append(left, "AGENTS.md lacks the line \""+OperatorLine+"\"")
	}
	switch res.GitIgnore {
	case ClaudeIgnoreAdded:
		left = append(left, ".gitignore lacks the .claude/ shape (.claude/*, !.claude/settings.json, !.claude/skills/, CLAUDE.local.md)")
	case ClaudeIgnoreRewritten:
		left = append(left, ".gitignore ignores the whole .claude folder, to rewrite to the .claude/ shape (.claude/*, !.claude/settings.json, !.claude/skills/)")
	}
	return res, &FailError{Code: CodeOperatorInterfacePending, Msg: "the operator interface (seq-07) is not applied: " + strings.Join(left, "; ") +
		" — `ape project operator-interface` applies it in one commit"}
}

// OperatorInterface is `ape project operator-interface` (Q97, the
// framework's v1.0.0_seq-07): models.apex-orchestrator moved to
// models.apex-loop byte for byte (or removed when apex-loop's row exists),
// ape's hooks merged into .claude/settings.json with init's merge, and
// AGENTS.md's OperatorLine added when absent, and the .claude/ shape put in
// the root .gitignore (D203, Q114) — in ONE commit,
// OperatorInterfaceSubject. Idempotent: with nothing left it writes
// nothing. Refused outside a git repository, on a change branch, and while
// the files it edits have uncommitted changes (nothing written).
func (p *Project) OperatorInterface(ctx context.Context) (*OperatorInterfaceResult, error) {
	res, err := p.operatorInterfacePlan()
	if err != nil || !res.Pending() {
		return res, err
	}
	if !p.isWorkTree(ctx) {
		return res, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — the operator interface is one commit"}
	}
	if err := p.recoverArchive(ctx); err != nil {
		return res, err
	}
	if err := p.refuseOnChangeBranch(ctx, "the operator interface"); err != nil {
		return res, err
	}
	paths := []string{p.Rel(ConfigPath(p.Root)), p.Rel(p.SettingsJSON()), p.Rel(p.AgentsMD())}
	if res.GitIgnore != "" {
		paths = append(paths, ".gitignore")
	}
	if dirty, err := gitRaw(ctx, p.Root, append([]string{"status", "--porcelain=v1", untrackedNormal, "--"}, paths...)...); err != nil {
		return res, &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	} else if strings.TrimSpace(dirty) != "" {
		return res, &FailError{Code: "tree.dirty", Msg: "the operator interface is one commit of " + strings.Join(paths, ", ") +
			", and one of them has uncommitted changes — commit or discard them first, then run it again"}
	}
	abs := func(rel string) string { return filepath.Join(p.Root, filepath.FromSlash(rel)) }
	// Each file as it was, to put back when the commit does not land.
	before := map[string][]byte{}
	for _, rel := range paths {
		if data, err := os.ReadFile(abs(rel)); err == nil {
			before[rel] = data
		}
	}
	undo := func(cause string) error {
		for _, rel := range paths {
			if data, ok := before[rel]; ok {
				_ = writeFileAtomic(abs(rel), string(data))
			} else {
				_ = os.Remove(abs(rel))
			}
		}
		_, _ = git(ctx, p.Root, append([]string{"reset", "-q", "--"}, paths...)...)
		return &FailError{Code: CodeCommitFailed, Msg: "the operator interface failed: " + cause + " — nothing was committed and the files are as they were; fix the cause and run it again"}
	}
	if res.Models != "" {
		out, err := moveOperatorRow(before[paths[0]])
		if err != nil {
			return res, &FailError{Code: CodeOperatorInterfacePending, Msg: "models.apex-orchestrator in " + paths[0] + " cannot be moved by ape: " + err.Error() +
				" — a person renames it to apex-loop by hand (or drops it beside an apex-loop row), then runs this again; nothing was written"}
		}
		if !bytes.Equal(out, before[paths[0]]) {
			if err := writeFileAtomic(ConfigPath(p.Root), string(out)); err != nil {
				return res, undo(err.Error())
			}
		}
	}
	if res.GitIgnore != "" {
		// D203 (Q114): before the hooks, so a settings.json it un-ignores
		// is committed with them.
		gi, err := EnsureClaudeIgnoreShape(ctx, p.Root)
		if err != nil {
			return res, undo(err.Error())
		}
		gi.SetRevealed(func(rel string) bool { return rel == settingsJSONRel })
		res.Revealed = gi.Revealed
	}
	added, err := MergeHooks(p.SettingsJSON())
	if err != nil {
		return res, undo(err.Error())
	}
	res.Hooks = append([]string{}, added...)
	if res.AgentsLine {
		if err := writeFileAtomic(p.AgentsMD(), withOperatorLine(string(before[paths[2]]), p.DevRel)); err != nil {
			return res, undo(err.Error())
		}
	}
	var commit []string
	for _, rel := range paths {
		if fileExists(abs(rel)) && len(gitIgnored(ctx, p.Root, false, rel)) == 0 {
			commit = append(commit, rel)
		}
	}
	sha, err := p.commitRecord(ctx, OperatorInterfaceSubject, "apex-orchestrator is the operator's interface; the loop is apex-loop (rc.7).", commit...)
	if err != nil {
		failed := undo(err.Error())
		if fe, ok := errors.AsType[*FailError](err); ok {
			return res, fe // a refusal with its own code (commit.secret)
		}
		return res, failed
	}
	res.Commit = sha
	return res, nil
}

// operatorRowNotices are project check's notices for a
// models.apex-orchestrator row (Q96): before seq-07 (no apex-loop row) it
// is read as apex-loop's; beside an apex-loop row it is a leftover, which
// apex-loop's row overrides. Neither fails the check: the loop runs either
// way.
func (p *Project) operatorRowNotices() []Diagnostic {
	data, err := os.ReadFile(ConfigPath(p.Root))
	if err != nil {
		return nil
	}
	row, err := findOperatorRow(data)
	if err != nil || row.key == nil {
		return nil
	}
	msg := "models.apex-orchestrator is read as apex-loop's until the operator-interface migration (seq-07) moves it; the operator runs in your session and has no models row" +
		" — `ape project operator-interface` renames it to apex-loop, its value kept"
	if row.loop {
		msg = "models.apex-orchestrator is a leftover: the operator runs in your session and has no models row, and apex-loop's row is the loop's" +
			" — drop it (`ape project operator-interface` removes it)"
	}
	return []Diagnostic{notice(Diagnostic{Code: CodeModelsOrchestratorRow, File: configYAML, Line: row.key.Line, Message: msg})}
}
