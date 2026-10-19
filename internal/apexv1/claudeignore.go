package apexv1

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// What .claude/ commits (D203): .claude/settings.json (ape's hooks) and
// .claude/skills/ (the framework's pinned skills) are the team's and
// tracked; the rest of .claude/ (settings.local.json, a person's agents)
// and CLAUDE.local.md are personal and never tracked. The root .gitignore
// says so in one shape, which init, the lift, `ape framework setup|update`
// on an APEX v1 release (Q113) and the operator interface (seq-07, Q114)
// put in place: a block marked by claudeIgnoreMarker, or an existing
// whole-folder .claude ignore rewritten in place to it — a folder ignore
// keeps git from ever reading a negation under it, so it must go. The
// project's own .claude/<x> lines stay as they are: still ignored under
// .claude/*.

// claudeIgnoreMarker opens the block ape writes.
const claudeIgnoreMarker = "# APEX (ape): .claude/settings.json and .claude/skills/ are the team's; the rest of .claude/ and CLAUDE.local.md stay personal"

// The .claude/ shape's lines, in order.
const (
	claudeIgnoreChildren = ".claude/*"
	claudeIgnoreSettings = "!.claude/settings.json"
	claudeIgnoreSkills   = "!.claude/skills/"
	claudeIgnoreLocalMD  = "CLAUDE.local.md"
)

// claudeSkillsProbe is the path git is asked about for .claude/skills/: a
// skill folder no release ships, so never tracked, named as the
// framework's are.
const claudeSkillsProbe = ".claude/skills/apex-probe/SKILL.md"

// What EnsureClaudeIgnoreShape did to the root .gitignore.
const (
	// ClaudeIgnoreAdded: the shape's missing lines appended, as a marked
	// block (the file created when absent).
	ClaudeIgnoreAdded = "added"
	// ClaudeIgnoreRewritten: a whole-folder .claude ignore replaced in
	// place by the block.
	ClaudeIgnoreRewritten = "rewritten"
	// ClaudeIgnoreAlready: the shape was there; nothing written.
	ClaudeIgnoreAlready = "already"
)

// ClaudeIgnoreResult is what EnsureClaudeIgnoreShape did.
type ClaudeIgnoreResult struct {
	// Action is ClaudeIgnoreAdded, ClaudeIgnoreRewritten or
	// ClaudeIgnoreAlready.
	Action string `json:"action"`
	// Created: the root .gitignore did not exist.
	Created bool `json:"created,omitempty"`
	// Uncommitted: init left the .gitignore out of its commit — it was
	// untracked or had uncommitted changes before.
	Uncommitted bool `json:"uncommitted,omitempty"`
	// Revealed are files under .claude/ git ignored before the rewrite and
	// no longer does, which the command did not commit (a folder per
	// skill): the person commits what the team shares, or ignores it by
	// name.
	Revealed []string `json:"revealed,omitempty"`
	// RevealedFiles is every file the rewrite un-ignored, before the
	// caller drops what it commits.
	RevealedFiles []string `json:"-"`
}

// Changed reports whether the .gitignore was written.
func (r *ClaudeIgnoreResult) Changed() bool {
	return r != nil && r.Action != ClaudeIgnoreAlready && r.Action != ""
}

// SetRevealed fills Revealed from RevealedFiles less those skip says the
// command commits (or removes), collapsed to a folder per skill.
func (r *ClaudeIgnoreResult) SetRevealed(skip func(rel string) bool) {
	r.Revealed = nil
	for _, f := range r.RevealedFiles {
		if skip != nil && skip(f) {
			continue
		}
		parts := strings.SplitN(f, "/", skillPathParts)
		if len(parts) == skillPathParts && parts[1] == "skills" {
			f = strings.Join(parts[:3], "/") + "/"
		}
		if !slices.Contains(r.Revealed, f) {
			r.Revealed = append(r.Revealed, f)
		}
	}
}

// ClaudeIgnoreLines are the lines a command prints for r: what it did to
// the .gitignore, then the files it un-ignored and left uncommitted.
func ClaudeIgnoreLines(r *ClaudeIgnoreResult) []string {
	if r == nil || r.Action == "" {
		return nil
	}
	shape := strings.Join([]string{claudeIgnoreChildren, claudeIgnoreSettings, claudeIgnoreSkills, claudeIgnoreLocalMD}, " ")
	var line string
	switch r.Action {
	case ClaudeIgnoreAdded:
		line = ".gitignore: added the .claude/ shape (" + shape + ")"
		if r.Created {
			line = ".gitignore: created with the .claude/ shape (" + shape + ")"
		}
	case ClaudeIgnoreRewritten:
		line = ".gitignore: rewrote the whole-folder .claude ignore in place to the .claude/ shape (" + shape + ")"
	default:
		line = ".gitignore: the .claude/ shape is already in place"
	}
	if r.Uncommitted {
		line += "; not committed: it was untracked or had uncommitted changes — commit it yourself"
	}
	lines := []string{line}
	if len(r.Revealed) > 0 {
		lines = append(lines, ".gitignore: now visible to git, not committed: "+strings.Join(r.Revealed, ", ")+
			" — commit what the team shares, or ignore it by name")
	}
	return lines
}

// gitignorePattern is a .gitignore line's pattern: its line ending and
// trailing blanks dropped, as git reads it.
func gitignorePattern(line string) string {
	return strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), " ")
}

// claudeFolderIgnore reports a pattern that ignores the whole .claude
// folder, which no negation under it can undo.
func claudeFolderIgnore(p string) bool {
	switch strings.TrimPrefix(p, "/") {
	case ".claude", ".claude/", ".claude/**":
		return true
	}
	return false
}

// shapeClaudeIgnore is the .gitignore text doc with the .claude/ shape in
// place, and what that took (ClaudeIgnore*). Every line it does not
// rewrite is kept byte for byte, and its line endings are doc's.
func shapeClaudeIgnore(doc string) (shaped, action string) {
	eol := "\n"
	if strings.Contains(doc, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.SplitAfter(doc, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	hasLocal := slices.ContainsFunc(lines, func(l string) bool {
		return strings.TrimPrefix(gitignorePattern(l), "/") == claudeIgnoreLocalMD
	})
	block := func(rules ...string) []string {
		out := make([]string, 0, 1+len(rules))
		out = append(out, claudeIgnoreMarker+eol)
		for _, r := range rules {
			out = append(out, r+eol)
		}
		return out
	}
	shape := []string{claudeIgnoreChildren, claudeIgnoreSettings, claudeIgnoreSkills}
	if !hasLocal {
		shape = append(shape, claudeIgnoreLocalMD)
	}
	// A whole-folder ignore: the first becomes the block, in place; any
	// other would re-ignore the folder after it, so it goes.
	rewritten := false
	out := make([]string, 0, len(lines)+len(shape)+1)
	for _, l := range lines {
		if !claudeFolderIgnore(gitignorePattern(l)) {
			out = append(out, l)
			continue
		}
		if !rewritten {
			if !strings.HasSuffix(l, "\n") {
				// The last line, unterminated: so is the block.
				b := block(shape...)
				b[len(b)-1] = strings.TrimSuffix(b[len(b)-1], eol)
				out = append(out, b...)
			} else {
				out = append(out, block(shape...)...)
			}
			hasLocal, rewritten = true, true
		}
	}
	// What is still missing: each negation counts only after the last
	// .claude/* (git's last match wins).
	children, settings, skills := false, false, false
	for _, l := range out {
		p := gitignorePattern(l)
		neg := strings.HasPrefix(p, "!")
		switch strings.TrimPrefix(strings.TrimPrefix(p, "!"), "/") {
		case claudeIgnoreChildren:
			if !neg {
				children, settings, skills = true, false, false
			}
		case ".claude/settings.json":
			settings = neg && children
		case ".claude/skills/", ".claude/skills":
			skills = neg && children
		}
	}
	var missing []string
	if !children {
		missing = append(missing, claudeIgnoreChildren, claudeIgnoreSettings, claudeIgnoreSkills)
	} else {
		if !settings {
			missing = append(missing, claudeIgnoreSettings)
		}
		if !skills {
			missing = append(missing, claudeIgnoreSkills)
		}
	}
	if !hasLocal {
		missing = append(missing, claudeIgnoreLocalMD)
	}
	action = ClaudeIgnoreAlready
	if rewritten {
		action = ClaudeIgnoreRewritten
	}
	if len(missing) == 0 {
		return strings.Join(out, ""), action
	}
	if !rewritten {
		action = ClaudeIgnoreAdded
	}
	return strings.Join(appendBlock(out, eol, block(missing...)), ""), action
}

// appendBlock appends block to a .gitignore's lines, after a line ending
// and a blank line when the file has text.
func appendBlock(lines []string, eol string, block []string) []string {
	if n := len(lines); n > 0 {
		if !strings.HasSuffix(lines[n-1], "\n") {
			lines[n-1] += eol
		}
		if strings.TrimSpace(lines[n-1]) != "" {
			lines = append(lines, eol)
		}
	}
	return append(lines, block...)
}

// claudeIgnorePlan is what EnsureClaudeIgnoreShape would do at root,
// writing nothing: the new .gitignore text and the action.
func claudeIgnorePlan(root string) (shaped, action string, err error) {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	shaped, action = shapeClaudeIgnore(string(data))
	return shaped, action, nil
}

// ClaudeIgnorePending is the action EnsureClaudeIgnoreShape would take at
// root ("" when the shape is in place); it writes nothing.
func ClaudeIgnorePending(root string) (string, error) {
	_, action, err := claudeIgnorePlan(root)
	if err != nil || action == ClaudeIgnoreAlready {
		return "", err
	}
	return action, nil
}

// EnsureClaudeIgnoreShape puts the .claude/ shape in root's .gitignore
// (D203), creating the file when absent; idempotent. In a git work tree
// it also lists the files under .claude/ the rewrite un-ignored
// (RevealedFiles). It commits nothing: the caller's commit carries it.
func EnsureClaudeIgnoreShape(ctx context.Context, root string) (*ClaudeIgnoreResult, error) {
	shaped, action, err := claudeIgnorePlan(root)
	if err != nil {
		return nil, err
	}
	file := filepath.Join(root, ".gitignore")
	res := &ClaudeIgnoreResult{Action: action}
	if action == ClaudeIgnoreAlready {
		return res, nil
	}
	res.Created = !fileExists(file)
	repo := inGitRepo(ctx, root)
	var ignored []string
	if repo {
		ignored = untrackedUnder(ctx, root, true, ".claude")
	}
	if err := writeFileAtomic(file, shaped); err != nil {
		return nil, err
	}
	if repo {
		visible := untrackedUnder(ctx, root, false, ".claude")
		for _, f := range ignored {
			if slices.Contains(visible, f) {
				res.RevealedFiles = append(res.RevealedFiles, f)
			}
		}
	}
	return res, nil
}

// untrackedUnder lists the untracked files under dir (relative to root)
// that git ignores (ignored) or does not.
func untrackedUnder(ctx context.Context, root string, ignored bool, dir string) []string {
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	if ignored {
		args = append(args, "--ignored")
	}
	out, err := gitRaw(ctx, root, append(args, "--", dir)...)
	if err != nil {
		return nil
	}
	var files []string
	for f := range strings.SplitSeq(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// checkIgnoreLine is one line of `git check-ignore -v`: the rule's
// source, line and pattern, a tab, the path.
var checkIgnoreLine = regexp.MustCompile(`^(.*):(\d+):(.*)\t(.*)$`)

// gitIgnoredAs is gitIgnored (a tracked path is never ignored) with the
// root .gitignore read as rootIgnore: the rules as they would stand once
// ape's shape is written, judged without writing it. git check-ignore runs
// over a scratch work tree that holds rootIgnore and the nested .gitignore
// files on each path's way, with the repository's own git dir, so
// .git/info/exclude and core.excludesFile apply as they do. It returns
// each ignored path with the rule that ignores it ("<source>:<line>:
// <pattern>", the source relative to root when it is inside it).
func gitIgnoredAs(ctx context.Context, root, rootIgnore string, paths ...string) (map[string]string, error) {
	gitDir, err := git(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "ape-ignore-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := os.WriteFile(filepath.Join(tmp, ".gitignore"), []byte(rootIgnore), 0o600); err != nil {
		return nil, err
	}
	for _, p := range paths {
		for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
			src := filepath.Join(root, filepath.FromSlash(d), ".gitignore")
			dst := filepath.Join(tmp, filepath.FromSlash(d), ".gitignore")
			data, err := os.ReadFile(src)
			if err != nil || fileExists(dst) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, data, 0o600); err != nil {
				return nil, err
			}
		}
	}
	c := exec.CommandContext(ctx, "git", "check-ignore", "--no-index", "-v", "--stdin")
	c.Dir = tmp
	c.Env = append(os.Environ(), "GIT_DIR="+gitDir, "GIT_WORK_TREE="+tmp)
	c.Stdin = strings.NewReader(strings.Join(paths, "\n") + "\n")
	var out bytes.Buffer
	c.Stdout = &out
	// Exit 1 is "none of them is ignored": stdout empty.
	_ = c.Run()
	ignored := map[string]string{}
	for l := range strings.SplitSeq(out.String(), "\n") {
		m := checkIgnoreLine.FindStringSubmatch(l)
		if len(m) != 5 || strings.HasPrefix(m[3], "!") {
			continue // a negation matched: not ignored
		}
		src := m[1]
		if rel, err := filepath.Rel(root, src); err == nil && filepath.IsAbs(src) && !strings.HasPrefix(rel, "..") {
			src = filepath.ToSlash(rel)
		}
		ignored[m[4]] = src + ":" + m[2] + ": " + m[3]
	}
	if len(ignored) == 0 {
		return ignored, nil
	}
	var asked []string
	for p := range ignored {
		asked = append(asked, p)
	}
	if tracked, err := gitRaw(ctx, root, append([]string{"ls-files", "-z", "--"}, asked...)...); err == nil {
		for f := range strings.SplitSeq(tracked, "\x00") {
			delete(ignored, f)
		}
	}
	return ignored, nil
}
