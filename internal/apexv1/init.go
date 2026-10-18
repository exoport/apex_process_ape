package apexv1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"gopkg.in/yaml.v3"
)

// Hook commands `ape project init` writes. Literal `ape`: inside an
// `ape task` session PATH starts with the running binary (selfpath).
const (
	HookCmdSessionStart = "ape hook session-start"
	HookCmdStop         = "ape hook stop"
	HookCmdPreTool      = "ape hook pre-tool"
	PreToolMatcher      = "Edit|Write|MultiEdit|NotebookEdit|Bash"
	// StopHookTimeout is the stop hook's timeout in seconds: it re-runs
	// the change's short command checks, within StopRunBudget (600s), and
	// never a long one (D58 2.5) — so it is not derived from
	// timeouts.check, as D55 proposed: a longer timeout would only let a
	// wedged hook hold the session longer.
	StopHookTimeout = 1800
)

// InitResult lists what `ape project init` created and what it left alone.
type InitResult struct {
	Root       string   `json:"root"`
	Created    []string `json:"created"`
	Kept       []string `json:"kept"`
	HooksAdded []string `json:"hooks_added"`
	Brownfield bool     `json:"brownfield"`
	Commit     string   `json:"commit,omitempty"`
	// Amended lists existing files init changed: a CLAUDE.md missing the
	// @AGENTS.md import, an AGENTS.md missing the project-context line
	// (D82), and an _apex/config.yaml without models: (D86).
	Amended []string `json:"amended,omitempty"`
	// AmendedUncommitted are the Amended files `apex: project init` left
	// out of its commit: a config that was untracked or had uncommitted
	// changes before init (D86), whose commit stays the person's.
	AmendedUncommitted []string `json:"amended_uncommitted,omitempty"`
	// Warnings are what init could not settle itself: a models: block
	// without an entry for a skill init has a default for, or a config
	// the defaults could not be appended to (D86).
	Warnings []string `json:"warnings,omitempty"`
}

// hasAgentsImport reports whether a CLAUDE.md imports the map.
func hasAgentsImport(doc string) bool {
	for l := range strings.SplitSeq(doc, "\n") {
		if strings.TrimSpace(l) == "@AGENTS.md" {
			return true
		}
	}
	return false
}

const projectSkeleton = `# %s

## Vision
<!-- one paragraph: what this is and why it exists -->

## Users
<!-- who uses it, and for what -->

## Scope
<!-- what it does -->

## Out of scope
<!-- what it deliberately does not do -->

## Constraints
<!-- hard limits: platforms, performance, compliance, NEVER rules that shape the product -->

## Stack
<!-- optional: languages, frameworks, runtime; or name this section ## Tools: what the work is made
     with, when it is not software (D118). No versions here: a pinned version is a decision (ADR). -->
`

// agentsSkeletonTemplate is the AGENTS.md init writes, {dev} the
// development folder. Its first line under the title is the
// project-context import (D82).
const agentsSkeletonTemplate = `# AGENTS.md

{context}

<!-- The project map, at most 16 KB (caps.agents_md): what this is, how to build, test and run the e2e
     checks, the NEVER rules, and pointers. Detail lives in {dev}/ and docs/. -->

## What this is

## Commands
<!-- build / test / e2e, one line each -->

## NEVER

## Pointers
- {dev}/project.md — vision, users, scope, constraints
- {dev}/roadmap.md — milestones and their gates
- {dev}/specs/ — living behaviour specs; they change only through ` + "`ape change archive`" + `
- {dev}/changes/ — units of change; ` + "`ape status`" + ` shows what is in flight
- {dev}/archive/ — archived and abandoned changes (read-only)
- {dev}/decisions/ — ADRs
- {dev}/briefs/ — each plan's input, dated and verbatim: history, never edited
`

// agentsSkeleton is the AGENTS.md skeleton for a project whose
// development folder is dev.
func agentsSkeleton(dev string) string {
	return strings.NewReplacer("{context}", projectContextLine(dev), "{dev}", dev).Replace(agentsSkeletonTemplate)
}

// projectContextLine is the line AGENTS.md carries so every session reads
// project.md first (D82): Claude Code expands the inline @path import
// (outside code), other harnesses read it as an instruction. Plain text:
// in a code span the import would not expand.
func projectContextLine(dev string) string {
	return "Project context: read @" + dev + "/project.md before planning or changing anything."
}

// hasProjectContext reports whether AGENTS.md imports project.md: a line
// outside fenced code carrying the token @<dev>/project.md outside a code
// span. The reference counts, not the sentence (Q38): a person may reword it.
func hasProjectContext(doc, dev string) bool {
	re := regexp.MustCompile(`(^|[\s(\[])@` + regexp.QuoteMeta(dev+"/project.md") + `($|[^\w./-]|\.($|\s))`)
	for _, l := range splitLines(doc) {
		if !l.InCode && re.MatchString(stripCodeSpans(l.Text)) {
			return true
		}
	}
	return false
}

// stripCodeSpans drops a line's inline code spans: a run of n backticks
// up to the next run of exactly n. An unmatched run is literal text.
func stripCodeSpans(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '`' {
			b.WriteByte(s[i])
			i++
			continue
		}
		n := 0
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		end := -1
		for j := i + n; j < len(s); {
			if s[j] != '`' {
				j++
				continue
			}
			m := 0
			for j+m < len(s) && s[j+m] == '`' {
				m++
			}
			if m == n {
				end = j + m
				break
			}
			j += m
		}
		if end < 0 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		i = end
	}
	return b.String()
}

// withProjectContext inserts the project-context line into an AGENTS.md
// that lacks it: after its first H1 (outside code), set off by blank
// lines, or at the very top when it has none. Nothing else changes.
func withProjectContext(doc, dev string) string {
	line := projectContextLine(dev) + "\n"
	raw := strings.SplitAfter(doc, "\n")
	at := -1
	for _, l := range splitLines(doc) {
		if lv, _ := headingLevel(l); lv == 1 {
			at = l.N
			break
		}
	}
	blank := func(i int) bool { return i >= len(raw) || strings.TrimSpace(raw[i]) == "" }
	if at < 0 {
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

// modelDefaultsYAML is the models: block a new _apex/config.yaml starts
// with (D65), and the one init appends to an existing config that has
// none (D86): the build (apex-apply, the orchestrator, and apex-release,
// D93) on sonnet/high, review, acceptance (apex-accept, D93) and planning
// on opus/medium. A project overrides any of them; models: is not guarded
// (config.changed), as it loosens nothing.
const modelDefaultsYAML = `# The model and effort each skill's session runs on (D65); change them freely.
models:
  apex-apply: {model: sonnet, effort: high}
  apex-orchestrator: {model: sonnet, effort: high}
  apex-review: {model: opus, effort: medium}
  apex-plan: {model: opus, effort: medium}
  apex-change: {model: opus, effort: medium}
  apex-accept: {model: opus, effort: medium}
  apex-release: {model: sonnet, effort: high}
`

// modelDefaultSkills are modelDefaultsYAML's skills, in its order.
func modelDefaultSkills() []string {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(modelDefaultsYAML), &doc); err != nil {
		panic("modelDefaultsYAML: " + err.Error())
	}
	return mappingKeys(topLevelValue(&doc, "models"))
}

// topLevelValue is the value of key in a YAML document's top-level
// mapping; nil when the document is empty, not a mapping, or lacks key.
func topLevelValue(doc *yaml.Node, key string) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mappingKeys are a mapping node's keys, in order; none for any other node.
func mappingKeys(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, n.Content[i].Value)
	}
	return out
}

// Init creates the v1 skeleton where absent and merges ape's hooks into
// .claude/settings.json. It never overwrites a file.
func Init(ctx context.Context, root string, brownfield bool) (*InitResult, error) {
	res, p, err := initSkeleton(ctx, root, brownfield)
	if err != nil {
		return res, err
	}
	if paths := p.initCommitPaths(res); len(paths) > 0 {
		sha, err := p.commitRecord(ctx, "apex: project init", "", paths...)
		if err != nil {
			return res, &FailError{Code: CodeCommitFailed, Msg: "skeleton written but its commit failed: " + err.Error()}
		}
		res.Commit = sha
	}
	return res, nil
}

// initSkeleton is Init without its commit: `ape project lift` lays the
// same skeleton down inside the lift commit.
func initSkeleton(ctx context.Context, root string, brownfield bool) (*InitResult, *Project, error) {
	p, err := Open(root)
	if err != nil {
		return nil, nil, err
	}
	res := &InitResult{Root: p.Root, Brownfield: brownfield}
	name := filepath.Base(p.Root)
	if cfg, err := LoadConfig(p.Root); err == nil && cfg.ProjectName != "" {
		name = cfg.ProjectName
	}

	write := func(path, content string) error {
		if fileOrDirExists(path) {
			res.Kept = append(res.Kept, p.Rel(path))
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(path, content); err != nil {
			return err
		}
		res.Created = append(res.Created, p.Rel(path))
		return nil
	}
	dir := func(path string, keep bool) error {
		if dirExists(path) {
			return nil
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		res.Created = append(res.Created, p.Rel(path)+"/")
		if keep {
			return writeFileAtomic(filepath.Join(path, ".gitkeep"), "")
		}
		return nil
	}

	// An existing config is kept, save for the models: defaults appended
	// when it has no models: block (D86).
	cfgExisted := fileExists(ConfigPath(p.Root))
	if err := write(ConfigPath(p.Root), "project_name: "+yamlScalar(name)+"\ndevelopment_folder: "+p.DevRel+"\n"+modelDefaultsYAML); err != nil {
		return nil, nil, err
	}
	if cfgExisted {
		if err := p.amendConfigModels(ctx, res); err != nil {
			return nil, nil, err
		}
	}
	for _, d := range []string{p.SpecsDir(), p.ChangesDir(), p.ArchiveDir(), p.DecisionsDir(), p.BriefsDir()} {
		if err := dir(d, true); err != nil {
			return nil, nil, err
		}
	}
	if err := dir(p.StateDir(), false); err != nil {
		return nil, nil, err
	}
	steps := []struct{ path, content string }{
		{p.ProjectMD(), fmt.Sprintf(projectSkeleton, name)},
		{p.RoadmapMD(), "# Roadmap\n"},
		{p.AgentsMD(), agentsSkeleton(p.DevRel)},
		{p.ClaudeMD(), "@AGENTS.md\n"},
		// A whitelist: whatever ape adds to _apex/state/ later is ignored
		// unless it is named here.
		{p.StateGitignore(), runlog.V1StateGitignore},
	}
	for _, s := range steps {
		if err := write(s.path, s.content); err != nil {
			return nil, nil, err
		}
	}
	// A CLAUDE.md that existed without the import gets the one line it
	// needs, and nothing else (item 15, OpenSpec Q8): the project's own
	// instructions stay as they are.
	if doc, err := readFileString(p.ClaudeMD()); err == nil && !hasAgentsImport(doc) {
		if !strings.HasSuffix(doc, "\n") && doc != "" {
			doc += "\n"
		}
		if err := writeFileAtomic(p.ClaudeMD(), doc+"@AGENTS.md\n"); err != nil {
			return nil, nil, err
		}
		res.Amended = append(res.Amended, "CLAUDE.md")
	}
	// So is an AGENTS.md without the project-context line (D82; one made
	// before rc.4, or the project's own): that line goes under its title.
	if doc, err := readFileString(p.AgentsMD()); err == nil && !hasProjectContext(doc, p.DevRel) {
		if err := writeFileAtomic(p.AgentsMD(), withProjectContext(doc, p.DevRel)); err != nil {
			return nil, nil, err
		}
		res.Amended = append(res.Amended, "AGENTS.md")
	}
	if !fileExists(p.ProjectJSON()) {
		rec := map[string]any{"initialized_at": Stamp(), "brownfield": brownfield, "base_sha": HeadSHA(ctx, p.Root)}
		data, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		if err := writeFileAtomic(p.ProjectJSON(), string(data)+"\n"); err != nil {
			return nil, nil, err
		}
		res.Created = append(res.Created, p.Rel(p.ProjectJSON()))
	} else {
		res.Kept = append(res.Kept, p.Rel(p.ProjectJSON()))
	}
	added, err := MergeHooks(p.SettingsJSON())
	if err != nil {
		return nil, nil, err
	}
	res.HooksAdded = added
	return res, p, nil
}

// amendConfigModels appends modelDefaultsYAML to an existing
// _apex/config.yaml that has no top-level models: key (D86), keeping its
// text byte for byte; a config the append would make unparseable, or
// whose appended block YAML would not read, is restored and warned about. The amendment joins `apex: project init`
// only when the config was tracked and clean before (AmendedUncommitted
// otherwise: committing it would commit the person's own edits). With
// models: present the file is left alone, and each skill init has a
// default for that it does not name is a warning: that skill runs on
// claude's default model.
func (p *Project) amendConfigModels(ctx context.Context, res *InitResult) error {
	path := ConfigPath(p.Root)
	rel := p.Rel(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if models := topLevelValue(&doc, "models"); models != nil {
		have := map[string]bool{}
		for _, k := range mappingKeys(models) {
			have[k] = true
		}
		for _, skill := range modelDefaultSkills() {
			if !have[skill] {
				res.Warnings = append(res.Warnings, rel+" models: has no "+skill+" entry — "+skill+
					" runs on claude's default model; add one to models: to choose its model and effort (see ape project init --help)")
			}
		}
		return nil
	}
	commit := p.trackedClean(ctx, rel)
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := writeFileAtomic(path, out+modelDefaultsYAML); err != nil {
		return err
	}
	// Parsing is not enough: YAML reads only the first document, so after
	// a "---", or a flow mapping, the block would parse and be ignored.
	problem := ""
	if cfg, err := LoadConfig(p.Root); err != nil {
		problem = "would not parse (" + err.Error() + ")"
	} else if len(cfg.Models) != len(modelDefaultSkills()) {
		problem = "would not be read (the file's first YAML document ends before them)"
	}
	if problem != "" {
		if err := writeFileAtomic(path, string(data)); err != nil {
			return err
		}
		res.Warnings = append(res.Warnings, rel+" has no models: block, and appending init's defaults "+problem+
			" — it is restored as it was; add a models: block by hand (see ape project init --help)")
		return nil
	}
	res.Amended = append(res.Amended, rel)
	if !commit {
		res.AmendedUncommitted = append(res.AmendedUncommitted, rel)
	}
	return nil
}

// trackedClean reports whether git tracks rel and it matches HEAD, in
// the index and the working tree; false outside a repo or before its
// first commit.
func (p *Project) trackedClean(ctx context.Context, rel string) bool {
	if _, err := git(ctx, p.Root, "ls-files", "--error-unmatch", "--", rel); err != nil {
		return false
	}
	if _, err := git(ctx, p.Root, "diff", "--quiet", "HEAD", "--", rel); err != nil {
		return false
	}
	_, err := git(ctx, p.Root, "diff", "--quiet", "--cached", "HEAD", "--", rel)
	return err == nil
}

// initCommitPaths is what `apex: project init` commits, so the tree is
// clean after it (D11): only what init itself wrote — a brownfield repo's
// own dirty files stay as they were, and so does a config init amended
// but did not commit (AmendedUncommitted). project.json is ignored (the
// whitelist), so it is not staged; the whitelist always is, in case
// another ape command wrote it before init ran.
func (p *Project) initCommitPaths(res *InitResult) []string {
	skip := map[string]bool{p.Rel(p.ProjectJSON()): true}
	for _, u := range res.AmendedUncommitted {
		skip[u] = true
	}
	var paths []string
	for _, c := range append(append([]string(nil), res.Created...), res.Amended...) {
		if !skip[c] {
			paths = append(paths, c)
		}
	}
	if fileExists(p.StateGitignore()) {
		paths = append(paths, p.Rel(p.StateGitignore()))
	}
	if len(res.HooksAdded) > 0 {
		paths = append(paths, p.Rel(p.SettingsJSON()))
	}
	return paths
}

func yamlScalar(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(data)
}
