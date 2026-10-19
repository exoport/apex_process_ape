package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

// The v0 layouts the lift reads (H25b): framework v0.15.0 (tenax, ape
// 0.0.66) through the last v0 line. Every one of them writes the same
// _apex/framework.yaml (ape's framework.Metadata since 2026-05) and
// resolves its folders through _apex/config.yaml (apexcfg), so one reader
// covers the range; a version outside it is refused rather than assumed.
const (
	liftMinVersion = "v0.15.0"
	// liftMaxVersion is exclusive: v0.31.x is the last v0 line.
	liftMaxVersion = "v0.32.0"
	// LiftTag is the tag the person pushes before the lift: the archive of
	// everything the lift deletes.
	LiftTag = "apex-v0-final"
	// LiftSubjectPrefix starts the lift commit's subject: how other code
	// finds the v1 epoch.
	LiftSubjectPrefix = "apex: lift"
	// LiftDisposition is the file the lift writes for review, relative
	// to the development folder (D47 Q4).
	LiftDisposition = "lift-disposition.md"
	// LiftReport holds the disposition's lists whole (L11), relative to
	// ape's state folder: ignored, never committed (R3). An rc.3 lift
	// committed it beside the disposition file; nothing reads that copy.
	LiftReport    = "lift-disposition.json"
	frameworkYAML = "_apex/framework.yaml"
	configYAML    = "_apex/config.yaml"
	// settingsJSONRel is the project's Claude Code settings file, as a
	// finding names it.
	settingsJSONRel = ".claude/settings.json"
	// v0SHALen is how much of the commit a v0 version was read from the
	// report names.
	v0SHALen = 12
	// skillPathParts splits a skill file's path into .claude, skills, the
	// skill and the rest.
	skillPathParts = 4
)

// untrackedNormal lists untracked files one by one under a git status
// that is narrowed to paths.
const untrackedNormal = "--untracked-files=normal"

// v0Meta is the part of _apex/framework.yaml the lift reads.
type v0Meta struct {
	Framework struct {
		VersionTag string `yaml:"version_tag"`
		GitHash    string `yaml:"git_hash"`
	} `yaml:"framework"`
	Ape struct {
		Version string `yaml:"version"`
	} `yaml:"ape"`
	Sources struct {
		Skills struct {
			Paths []string `yaml:"paths"`
		} `yaml:"skills"`
	} `yaml:"sources"`
}

func (m *v0Meta) major() string {
	if v := normSemver(m.Framework.VersionTag); v != "" {
		return semver.Major(v)
	}
	return ""
}

func normSemver(v string) string {
	v = "v" + strings.TrimPrefix(strings.TrimSpace(v), "v")
	if semver.IsValid(v) {
		return v
	}
	return ""
}

// liftLayout is what the lift found of the v0 install.
type liftLayout struct {
	Version       string
	VersionSource string
	// Skills are the v0 install's skill folders; SkillsSource says
	// whether they came from its manifest or the apex- prefix the v0
	// installer owned.
	Skills       []string
	SkillsSource string
	// V1Skills are the skill folders a v1 framework install already put
	// there (the lift runs inside `ape framework update`): never deleted.
	V1Skills  []string
	V1Install bool
	Cfg       *apexcfg.Resolved
}

var updateSubjectVersionRe = regexp.MustCompile(`update APEX framework to (v\d+\.\d+\.\d+\S*)`)

// readMeta parses framework.yaml content; nil when it does not parse.
func readMeta(data string) *v0Meta {
	var m v0Meta
	if err := yaml.Unmarshal([]byte(data), &m); err != nil {
		return nil
	}
	return &m
}

// detectV0 finds the v0 install: its version, its skills, its folders.
// The working framework.yaml when it records a v0 version; when a v1
// install has replaced it (the lift runs inside, or after, `ape framework
// update`), the newest one in the history that does.
func detectV0(ctx context.Context, root string) (*liftLayout, error) {
	l := &liftLayout{}
	var found []string
	var v0 *v0Meta
	var v0src string
	if data, err := os.ReadFile(filepath.Join(root, frameworkYAML)); err == nil {
		m := readMeta(string(data))
		switch {
		case m == nil:
			found = append(found, frameworkYAML+" (unparseable)")
		case m.major() == "v0" || m.major() == "" && m.Framework.GitHash != "":
			v0, v0src = m, frameworkYAML
		default:
			l.V1Install = true
			l.V1Skills = m.Sources.Skills.Paths
			found = append(found, frameworkYAML+" records "+m.Framework.VersionTag)
		}
	}
	if v0 == nil && l.V1Install {
		// A v1 install wrote framework.yaml over the v0 one (the lift runs
		// inside, or after, `ape framework update`): the history has it.
		out, _ := git(ctx, root, "log", "--format=%H", "--", frameworkYAML)
		for sha := range strings.SplitSeq(out, "\n") {
			if sha == "" {
				continue
			}
			data, err := gitRaw(ctx, root, "show", sha+":"+frameworkYAML)
			if err != nil {
				continue
			}
			if m := readMeta(data); m != nil && m.major() == "v0" {
				v0, v0src = m, frameworkYAML+" at "+sha[:min(v0SHALen, len(sha))]
				break
			}
		}
	}
	cfgPath := filepath.Join(root, "_apex", "config.yaml")
	if v0 == nil {
		if data, err := os.ReadFile(cfgPath); err == nil && v0ConfigRe.Match(data) {
			found = append(found, "_apex/config.yaml with v0 folder keys, and no framework.yaml that records a v0 version")
			return nil, &FailError{Code: CodeUnsupportedLayout, Msg: "found " + strings.Join(found, "; ") +
				" — install a v0.15–v0.31 framework with `ape framework update` (pinned v0 ape), commit it, then lift"}
		}
		msg := "no APEX v0 install here (no " + frameworkYAML + " recording a v0 version)"
		if len(found) > 0 {
			msg += "; found " + strings.Join(found, "; ")
		}
		return nil, &FailError{Code: "lift.not-v0", Msg: msg + " — `ape project lift` converts an APEX v0 project; a new project starts with `ape project init`"}
	}
	l.Version, l.VersionSource = v0.Framework.VersionTag, v0src
	if normSemver(l.Version) == "" {
		// A worktree install records no tag; ape's own update commit
		// names the version it installed.
		out, _ := git(ctx, root, "log", "-1", "--format=%s", "--grep=update APEX framework to v")
		if m := updateSubjectVersionRe.FindStringSubmatch(out); m != nil {
			l.Version, l.VersionSource = m[1], "commit subject "+strings.TrimSpace(out)
		}
	}
	v := normSemver(l.Version)
	if v == "" || semver.Compare(v, liftMinVersion) < 0 || semver.Compare(v, liftMaxVersion) >= 0 {
		what := "framework version " + l.Version
		if l.Version == "" {
			what = "no framework version (" + v0src + " records git_hash " + v0.Framework.GitHash + " and no tag; no update commit names one)"
		}
		return nil, &FailError{Code: CodeUnsupportedLayout, Msg: "found " + what + " in " + l.VersionSource +
			"; the lift reads v0.15.0–v0.31.x — `ape framework update` to a v0.31 release (pinned v0 ape) first, commit it, then lift"}
	}
	l.Skills = v0.Sources.Skills.Paths
	l.SkillsSource = "the v0 install manifest (" + v0src + ")"
	cfg, err := apexcfg.ResolveAt(root, nil)
	if err != nil {
		return nil, &FailError{Code: CodeUnsupportedLayout, Msg: "found framework " + l.Version + " but _apex/config.yaml does not resolve: " + err.Error() +
			" — fix the YAML (and config.local.yaml), then lift"}
	}
	l.Cfg = cfg
	return l, nil
}

var v0ConfigRe = regexp.MustCompile(`(?m)^(?:implementation_folder|planning_folder|governance_folder|output_folder|config_schema_version):`)

// liftFamily is one v0 family the lift deletes.
type liftFamily struct {
	Name  string
	Paths []string
}

// LiftDeletion is one path the lift removes, with its family.
type LiftDeletion struct {
	Path   string `json:"path"`
	Family string `json:"family"`
	// Untracked is a v0 skill folder the manifest lists that git does not
	// track (an ignored .claude/, L2): removed from the working tree after
	// the commit, and not archived by the tag.
	Untracked bool `json:"untracked,omitempty"`
}

// v0Tables are the framework-owned files and folders a v0 install wrote
// under _apex/ (internal/framework's Project* paths). framework.yaml,
// config.yaml, config.local.yaml and state/ are not among them.
var v0Tables = []string{
	"_apex/apex-operating-rules.md", "_apex/terminal-contracts.csv", "_apex/output-styles.csv",
	"_apex/agent-manifest.csv", "_apex/apex-help.csv", "_apex/effort-defaults.yaml",
	"_apex/change-routes.yaml", "_apex/commit-owners.csv", "_apex/aboard/recipes",
	"_apex/config.local.example.yaml",
}

// v1SharedTables are v0 tables a v1 framework install ships as well: kept
// once a v1 install is present, deleted otherwise (the v1 install writes
// its own).
var v1SharedTables = []string{"_apex/ape-commands.yaml", "_apex/migrations"}

// families lists what the lift deletes, from the resolved v0 folders.
// Only paths git tracks are listed; a folder variable that names the
// project root, the development folder itself or the docs folder is
// never deleted whole.
func (lf *lifter) families(ctx context.Context) []liftFamily {
	paths := lf.layout.Cfg.Paths
	rel := func(abs string) string {
		if abs == "" {
			return ""
		}
		r, err := filepath.Rel(lf.root, abs)
		if err != nil || r == "." || strings.HasPrefix(r, "..") {
			return ""
		}
		return filepath.ToSlash(r)
	}
	protected := map[string]bool{"": true, ".": true, lf.devRel: true, rel(paths.Docs): true, "_apex": true, ".claude": true}
	whole := func(r string) string {
		if protected[r] {
			return ""
		}
		return r
	}
	var fams []liftFamily
	add := func(name string, rs ...string) {
		var keep []string
		for _, r := range rs {
			if r != "" && !lf.dirtyAt(r) && lf.tracked(ctx, r) {
				keep = append(keep, r)
			}
		}
		if len(keep) > 0 {
			fams = append(fams, liftFamily{Name: name, Paths: keep})
		}
	}

	skills := lf.layout.Skills
	if len(skills) == 0 {
		lf.layout.SkillsSource = "the apex- prefix the v0 installer owns (the manifest lists none)"
		out, _ := git(ctx, lf.root, "ls-files", "--", ".claude/skills/apex-*")
		seen := map[string]bool{}
		for f := range strings.SplitSeq(out, "\n") {
			// .claude/skills/<skill>/…
			if parts := strings.SplitN(f, "/", skillPathParts); len(parts) >= 3 && !seen[parts[2]] {
				seen[parts[2]] = true
				skills = append(skills, ".claude/skills/"+parts[2])
			}
		}
	}
	v1 := map[string]bool{}
	for _, s := range lf.layout.V1Skills {
		v1[filepath.ToSlash(s)] = true
	}
	var sk []string
	for _, s := range skills {
		s = filepath.ToSlash(filepath.Clean(s))
		if strings.HasPrefix(s, ".claude/skills/") && !v1[s] {
			sk = append(sk, s)
		}
	}
	sort.Strings(sk)
	// The install may already have removed them (inside `framework
	// update`): a deletion git still has to record.
	var skKeep []string
	for _, s := range sk {
		switch {
		case lf.tracked(ctx, s):
			skKeep = append(skKeep, s)
		case !lf.dirtyAt(s) && dirExists(filepath.Join(lf.root, filepath.FromSlash(s))):
			lf.untracked = append(lf.untracked, s)
		}
	}
	if len(skKeep) > 0 {
		fams = append(fams, liftFamily{Name: "apex-v0 skills", Paths: skKeep})
	}

	tables := append([]string(nil), v0Tables...)
	if !lf.layout.V1Install {
		tables = append(tables, v1SharedTables...)
	}
	add("apex-v0 _apex tables", tables...)
	add("pipelines", "_apex/pipelines")
	add("stories, retrospectives and the sprint tracker", whole(rel(paths.Implementation)))
	add("planning: PRD, architecture, epics, proposals, readiness reports, release records", whole(rel(paths.Planning)))
	add("ADR records and indexes (converted to decisions/)", whole(rel(paths.ADRs)))
	add("patterns", whole(rel(paths.Patterns)))
	add("capability registry", whole(rel(paths.Capabilities)))
	add("feature registry", whole(rel(paths.Features)))
	add("deferred records", whole(rel(paths.Deferred)), rel(paths.DeferredLegacy))
	add("team memory", rel(paths.TeamMemory))
	pc := rel(paths.ProjectContext)
	if lf.projectContext != "" {
		pc = lf.projectContext
	}
	add("project context", pc)
	return fams
}

// tracked reports whether git tracks rel (a file, or anything under a
// folder) at HEAD.
func (lf *lifter) tracked(ctx context.Context, rel string) bool {
	out, err := git(ctx, lf.root, "ls-tree", "-r", "--name-only", "HEAD", "--", rel)
	return err == nil && out != ""
}

// storyKeys reads the corpus's story keys: the story files' stems and the
// tracker's rows.
func storyKeys(implAbs string) map[string]bool {
	keys := map[string]bool{}
	if implAbs == "" {
		return keys
	}
	re := regexp.MustCompile(`^(\d+)-(\d+)[_-]`)
	_ = filepath.WalkDir(implAbs, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable folder holds no keys
		}
		if m := re.FindStringSubmatch(d.Name()); m != nil {
			keys[m[1]+"-"+m[2]] = true
		}
		return nil
	})
	if data, err := os.ReadFile(filepath.Join(implAbs, "sprint-status.yaml")); err == nil {
		row := regexp.MustCompile(`(?m)^\s+(\d+)-(\d+)[_-]`)
		for _, m := range row.FindAllStringSubmatch(string(data), -1) {
			keys[m[1]+"-"+m[2]] = true
		}
	}
	return keys
}
