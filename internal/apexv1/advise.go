package apexv1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Advise (v1.1 W6, Q-E1): an expert's rules that the change, apply and
// review sessions read before they work. A tools.advise entry names a
// skill and the files under its folder (load:); a change names the entries
// whose when: it matches in change.md's advise: frontmatter, as it does
// review_tools:. ape starts no session for it and gives no verdict: it
// resolves each file where the skill resolves (a plugin skill in the
// listed plugin's install, else a user or project skill, a user one
// winning as in Claude Code — the folder that holds its SKILL.md), sizes
// and hashes it, and never reads it for meaning (Q-E11: its text is a
// person's to accept, under the config.changed guard on tools:).
//
//   - `ape status --json` lists each change's files, changes[].advise:
//     [{name, skill, for, file, path, bytes, sha256}], path absolute, so a
//     session reads them with no plugin-cache knowledge;
//   - `ape change check` refuses a name tools.advise does not declare
//     (tools.unknown), and files over caps.advise per entry or
//     caps.advise_session per session (tools.too-big);
//   - every verify records each file's sha256 in state.json
//     (verify.advise), so an expert updated in the middle of a change
//     shows in the record.

// AdviseFile is one advise file of a change, or of a tools.advise entry.
type AdviseFile struct {
	// Name is the tools.advise entry; Skill its skill.
	Name  string `json:"name"`
	Skill string `json:"skill,omitempty"`
	// For: the sessions that read it (change, apply, review).
	For []string `json:"for,omitempty"`
	// File is the load: entry, relative to the skill's folder; Path the
	// file it resolved to (absolute), or where it was looked for.
	File   string `json:"file,omitempty"`
	Path   string `json:"path,omitempty"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	// Error says why the file is not there to read: the entry is not
	// declared, its skill is not installed, the file is missing.
	Error string `json:"error,omitempty"`
}

// adviseFilesOf resolves an advise entry's load: files under dir, the
// folder of its skill.
func adviseFilesOf(tool Tool, dir string) []AdviseFile {
	out := make([]AdviseFile, 0, len(tool.Load))
	for _, rel := range tool.Load {
		f := AdviseFile{Name: tool.Name, Skill: tool.Skill, For: tool.Sessions(), File: rel, Path: filepath.Join(dir, filepath.FromSlash(rel))}
		f.Bytes, f.SHA256, f.Error = hashFile(f.Path)
		out = append(out, f)
	}
	return out
}

// hashFile is a regular file's size and sha256, or why there is none.
func hashFile(path string) (size int, sum, problem string) {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return 0, "", "missing"
	case err != nil:
		return 0, "", "unreadable"
	case !fi.Mode().IsRegular():
		return 0, "", "not a regular file"
	}
	fh, err := os.Open(path)
	if err != nil {
		return 0, "", "unreadable"
	}
	defer func() { _ = fh.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return 0, "", "unreadable"
	}
	return int(n), hex.EncodeToString(h.Sum(nil)), ""
}

// adviseCLI is the `claude plugin` CLI an advise entry's plugin skill is
// found through (its install folder), nil when claude is not on PATH. It
// is asked only when an entry a change names is a plugin skill. A var so
// tests use a fake.
var adviseCLI = func() PluginCLI {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil
	}
	return ClaudePluginCLI{}
}

// adviseResolver resolves tools.advise entries for one command: the
// plugin installs are read once, and only if an entry needs them.
type adviseResolver struct {
	p       *Project
	cfg     Config
	rows    []PluginStatus
	checked bool
	read    bool
	cache   map[string]ToolStatus
}

// newAdviseResolver resolves against cfg, the live checkout's skills and
// installs (a view's too: liveRoot).
func (p *Project) newAdviseResolver(cfg Config) *adviseResolver {
	return &adviseResolver{p: &Project{Root: p.liveRoot(), DevRel: p.DevRel}, cfg: cfg, cache: map[string]ToolStatus{}}
}

// entry resolves the advise entry named name: its status with its files
// (Files only when the skill was found), or ok false when tools.advise
// declares no such entry.
func (r *adviseResolver) entry(ctx context.Context, name string) (ToolStatus, bool) {
	if st, ok := r.cache[name]; ok {
		return st, true
	}
	tool := r.cfg.Tools.Find("advise", name)
	if tool == nil {
		return ToolStatus{}, false
	}
	if strings.Contains(tool.Skill, ":") && !r.read {
		r.read = true
		if len(pluginConfigErrors(r.cfg)) == 0 && len(r.cfg.Plugins) > 0 {
			rows, checked, err := r.p.pluginStatuses(ctx, ListOnce(adviseCLI()), r.cfg)
			if err == nil {
				r.rows, r.checked = rows, checked
			}
		}
	}
	st := r.p.resolveTool(r.cfg, r.rows, r.checked, "advise", *tool)
	st.For = tool.Sessions()
	if st.State == ToolOK {
		st.Files = adviseFilesOf(*tool, filepath.Dir(st.Path))
	}
	r.cache[name] = st
	return st, true
}

// files lists a change's advise: names as files: one row per load: file
// of each, or one row with Error for a name not declared or a skill not
// installed.
func (r *adviseResolver) files(ctx context.Context, names []string) []AdviseFile {
	out := []AdviseFile{}
	for _, name := range names {
		st, ok := r.entry(ctx, name)
		switch {
		case !ok:
			out = append(out, AdviseFile{Name: name, Error: "not declared in tools.advise of _apex/config.yaml"})
		case st.State != ToolOK:
			out = append(out, AdviseFile{Name: name, Skill: st.Skill, For: st.For, Error: "skill " + st.Skill + " is " + st.State + ": " + st.Path})
		default:
			out = append(out, st.Files...)
		}
	}
	return out
}

// adviseFindings are change check's advise: findings for change c: a
// name tools.advise does not declare (tools.unknown); with perEntry, an
// entry whose files are over caps.advise — project check reports that
// once, on _apex/config.yaml — and, for each session, the files it would
// read over caps.advise_session (tools.too-big). Files that cannot be
// resolved are project check's (tools.not-installed) and count 0 here.
func (p *Project) adviseFindings(ctx context.Context, file string, c *Change, perEntry bool) []Finding {
	if len(c.Meta.Advise) == 0 {
		return nil
	}
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return nil
	}
	caps := cfg.Caps.Limits()
	r := p.newAdviseResolver(cfg)
	var out []Finding
	perSession := map[string]int{}
	readers := map[string][]string{}
	for _, name := range c.Meta.Advise {
		st, ok := r.entry(ctx, name)
		if !ok {
			out = append(out, Finding{File: file, Line: 1, Message: "advise: " + name + " is not declared in tools.advise of _apex/config.yaml" +
				" — name a declared advise entry (" + toolNames(cfg.Tools.Advise) + "), or drop it"})
			continue
		}
		total := 0
		for i := range st.Files {
			total += st.Files[i].Bytes
		}
		if perEntry && total > caps.Advise {
			out = append(out, Finding{File: file, Line: 1, Message: "advise: " + name + " loads " + sizeKB(total) + ", over the " + capSize(caps.Advise) + " cap (caps.advise)" +
				" — drop it from advise:, or a person shortens its files or raises caps.advise in _apex/config.yaml"})
		}
		for _, s := range st.For {
			perSession[s] += total
			readers[s] = append(readers[s], name)
		}
	}
	for _, s := range AdviseSessions {
		if n := perSession[s]; n > caps.AdviseSession {
			out = append(out, Finding{File: file, Line: 1, Message: "advise: the " + s + " session would read " + sizeKB(n) + " (" + strings.Join(readers[s], ", ") +
				"), over the " + capSize(caps.AdviseSession) + " cap (caps.advise_session) — list fewer advise entries, or a person narrows an entry's for: or raises caps.advise_session in _apex/config.yaml"})
		}
	}
	return out
}

// ---- tools.unmatched (W6, Q-E2) ----------------------------------------

// CodeToolsUnmatched: a notice — a change whose planned paths: or
// touches: (change check), or whose diff (verify), match a tools entry's
// paths:/touches: that the change does not list.
const CodeToolsUnmatched = "tools.unmatched"

// toolListed reports whether change c lists the slot's entry name: a
// design tool on a task's `— tool:`, a review tool in review_tools:, an
// advise entry in advise:.
func toolListed(c *Change, slot, name string) bool {
	switch slot {
	case "design":
		return slices.ContainsFunc(c.Tasks, func(t Task) bool { return t.Tool == name })
	case "review":
		return slices.Contains(c.Meta.ReviewTools, name)
	}
	return slices.Contains(c.Meta.Advise, name)
}

// toolListing is where a change lists a slot's entry, for the notice.
func toolListing(slot, name string) string {
	switch slot {
	case "design":
		return "a task's `— tool: " + name + "`"
	case "review":
		return "review_tools: [" + name + "]"
	}
	return "advise: [" + name + "]"
}

// globsMeet is how a change's planned paths: glob meets an entry's: one
// glob matches the other read as a path ("internal/bus/**" and
// "internal/bus/*.go"; "internal/**" and "internal/bus/**";
// "**/*jetstream*.go" and "internal/bus/jetstream.go").
func globsMeet(a, b string) bool {
	ra, errA := globRegexp(a)
	rb, errB := globRegexp(b)
	if errA != nil || errB != nil {
		return false
	}
	norm := func(g string) string { return strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(g)), "./") }
	return ra.MatchString(norm(b)) || rb.MatchString(norm(a))
}

// unmatchedTools are the tools.unmatched notices for change c: each tools
// entry with paths: or touches: that c does not list and that c matches —
// a touches: slug c's touches: names, or, with diff nil (change check), a
// paths: glob that meets one of c's planned paths:, or, with diff (verify,
// the files the change's commits touched), a paths: glob a changed file
// matches.
func unmatchedTools(tools ToolsConfig, c *Change, file string, diff []string) []Diagnostic {
	out := []Diagnostic{}
	for _, slot := range AllToolSlots {
		ts := tools.Slot(slot)
		for i := range ts {
			t := &ts[i]
			if (len(t.Paths) == 0 && len(t.Touches) == 0) || toolListed(c, slot, t.Name) {
				continue
			}
			var why []string
			for _, s := range t.Touches {
				if slices.Contains(c.Meta.Touches, s) {
					why = append(why, "touches: "+s)
				}
			}
			why = append(why, pathsMeet(t.Paths, c.Meta.Paths, diff)...)
			if len(why) == 0 {
				continue
			}
			out = append(out, notice(Diagnostic{Code: CodeToolsUnmatched, File: file, Line: 1, Message: "tools." + slot + " entry " + t.Name + " matches this change (" +
				strings.Join(limit(why, 3), "; ") + ") and the change does not list it — if its when: (\"" + t.When + "\") applies, list it: " +
				toolListing(slot, t.Name) + "; if not, leave it: a warning, never a refusal"}))
		}
	}
	return out
}

// pathsMeet says how an entry's paths: globs meet a change: its planned
// globs (diff nil), or the files its diff touched.
func pathsMeet(globs, planned, diff []string) []string {
	var why []string
	for _, g := range globs {
		if diff == nil {
			for _, h := range planned {
				if globsMeet(g, h) {
					why = append(why, "paths: "+h+" ~ "+g)
				}
			}
			continue
		}
		re, err := globRegexp(g)
		if err != nil {
			continue
		}
		for _, f := range diff {
			if re.MatchString(f) {
				why = append(why, f+" matches "+g)
				break
			}
		}
	}
	return why
}

// ChangeNotices are change check's notices for change id, as the branch
// holding its records has it: tools.unmatched against its planned paths:
// and touches: (W6). Never findings: they do not change the exit.
func (p *Project) ChangeNotices(ctx context.Context, id string) []Diagnostic {
	view, done, err := p.viewAt(ctx, p.changeBranchOf(ctx, id))
	if err != nil {
		view, done = p, func() {}
	}
	defer done()
	doc, err := readFileString(view.ChangeMD(id))
	if err != nil {
		return []Diagnostic{}
	}
	cfg, err := LoadConfig(view.Root)
	if err != nil {
		return []Diagnostic{}
	}
	file := view.Rel(view.ChangeMD(id))
	c := ParseChange(file, doc)
	if c.MetaErr != nil {
		return []Diagnostic{}
	}
	return unmatchedTools(cfg.Tools, c, file, nil)
}

// verifyAdvise is what a verify reads of the change's advise (W6): its
// files, hashed now, and the tools.unmatched notices against the files
// its commits touched since base_sha (the development folder and ape's
// state left out).
func (p *Project) verifyAdvise(ctx context.Context, ci ChangeInfo) (files []AdviseFile, notices []Diagnostic) {
	cfg, err := LoadConfig(p.Root)
	if err != nil || ci.Doc == nil {
		return nil, nil
	}
	if len(ci.Doc.Meta.Advise) > 0 {
		files = p.newAdviseResolver(cfg).files(ctx, ci.Doc.Meta.Advise)
	}
	var diff []string
	if ci.State != nil && ci.State.BaseSHA != "" {
		if out, err := git(ctx, p.Root, "diff", "--name-only", "--no-renames", ci.State.BaseSHA, "HEAD"); err == nil {
			for f := range strings.SplitSeq(out, "\n") {
				if f != "" && !p.notCode(f) {
					diff = append(diff, f)
				}
			}
		}
	}
	if diff == nil {
		diff = []string{}
	}
	return files, unmatchedTools(cfg.Tools, ci.Doc, p.Rel(p.ChangeMD(ci.ID)), diff)
}

// adviseRecord is files as state.json keeps them at a verify: no
// absolute path (a plugin's lives in this machine's cache), no for:.
func adviseRecord(files []AdviseFile) []AdviseFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]AdviseFile, len(files))
	for i := range files {
		out[i] = files[i]
		out[i].Path, out[i].For = "", nil
	}
	return out
}

// AttachAdvise fills each change's advise in a status report (W6): the
// files of the entries its advise: names, resolved and hashed now. The
// `ape status` command calls it; Status alone does not, as a plugin
// skill's folder is found through the claude plugin CLI, which a hook or
// the board should not wait for.
func (p *Project) AttachAdvise(ctx context.Context, r *StatusReport) {
	var res *adviseResolver
	for i := range r.Changes {
		c := &r.Changes[i]
		if len(c.adviseNames) == 0 {
			continue
		}
		if res == nil {
			cfg, err := LoadConfig(p.Root)
			if err != nil {
				return
			}
			res = p.newAdviseResolver(cfg)
		}
		c.Advise = res.files(ctx, c.adviseNames)
	}
}
