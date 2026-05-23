package framework

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillScope tags whether a skill was resolved under the project's
// .claude/skills tree or the user-scoped ~/.claude/skills tree. Matches
// the lookup order claude itself uses (project wins over user).
type SkillScope string

const (
	// ScopeProject — skill resolved under <projectRoot>/.claude/skills/.
	ScopeProject SkillScope = "project"
	// ScopeUser — skill resolved under <home>/.claude/skills/.
	ScopeUser SkillScope = "user"
	// ScopeNone — skill did not resolve at either location.
	ScopeNone SkillScope = ""
)

// ResolveSkill reports whether a skill name resolves to an on-disk
// SKILL.md, mirroring claude's lookup order: project-scoped
// `<projectRoot>/.claude/skills/<name>/SKILL.md` first, then
// user-scoped `~/.claude/skills/<name>/SKILL.md`. An empty projectRoot
// disables the project-scope check. ResolveSkill returns the absolute
// path, the scope it was found in, and a found flag.
func ResolveSkill(name, projectRoot string) (path string, scope SkillScope, found bool) {
	if name == "" {
		return "", ScopeNone, false
	}
	if projectRoot != "" {
		projPath := filepath.Join(projectRoot, ProjectSkillsDir, name, "SKILL.md")
		if _, err := os.Stat(projPath); err == nil {
			return projPath, ScopeProject, true
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".claude", "skills", name, "SKILL.md")
		if _, err := os.Stat(userPath); err == nil {
			return userPath, ScopeUser, true
		}
	}
	return "", ScopeNone, false
}

// ListInstalledSkills returns the names of every skill installed under
// `<dir>/<name>/SKILL.md`, sorted. Returns an empty slice when dir
// doesn't exist or has no skills. Errors only on unexpected I/O issues
// (a missing dir is not an error).
func ListInstalledSkills(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// ProjectSkillsPath returns the absolute path of the project's
// .claude/skills directory. Convenience for callers that don't want to
// reach for filepath.Join + the layout constant.
func ProjectSkillsPath(projectRoot string) string {
	return filepath.Join(projectRoot, ProjectSkillsDir)
}

// UserSkillsPath returns the absolute path of the user-scoped
// ~/.claude/skills directory, or empty string when the home directory
// is not resolvable.
func UserSkillsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "skills")
}

// IsFrameworkSkill reports whether a skill name is one the framework
// manages — currently every name starting with the `apex-` prefix.
// Non-framework skills installed by the user under the same tree are
// left alone by `ape framework update` and are tagged "custom" in
// doctor output.
func IsFrameworkSkill(name string) bool {
	return strings.HasPrefix(name, SkillPrefix)
}
