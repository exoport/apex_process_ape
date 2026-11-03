package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The assessor session's confinement (PLAN-V15-ASSESSOR step 4; research's
// review 1, 6 and R2, R3). `ape task apex-assess` runs in a batch folder
// (BuildAssessFolder) and marks the session with EnvAssess = the folder.
// Then pre-tool, registered for every tool, is an allow-list, not the
// deny-list of known mistakes it is elsewhere: Read, Grep and Glob inside
// the folder, and a Write of the folder's notice.json — nothing else, an
// MCP tool included. Claude Code's --tools allows only those four built-ins
// as well; the hook is what binds the paths, and what holds for a tool
// --tools does not cover.
const (
	// AssessSkill is the assessor's skill; AssessPlugin the plugin ape
	// supplies it from, since --setting-sources user drops project skills
	// (measured on Claude Code 2.1.316): the typed name is
	// AssessPlugin:AssessSkill.
	AssessSkill  = "apex-assess"
	AssessPlugin = "ape-assess"
	// EnvAssess marks an assessor session: the batch folder's path.
	EnvAssess = "APE_ASSESS"
	// AssessDeniedSuffix names the file beside the folder where every
	// denial is recorded: any line in it fails the batch (R3, step 5).
	AssessDeniedSuffix = ".denied.jsonl"
)

// AssessTools are the assessor's roster, as --tools takes it.
var AssessTools = []string{"Read", "Grep", "Glob", "Write"}

// AssessDenial is one recorded denial.
type AssessDenial struct {
	At     string `json:"at"`
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

// AssessGuard is pre-tool's decision in an assessor session whose batch
// folder is folder. A denial is also recorded beside the folder.
func AssessGuard(payload HookPayload, folder string) PreToolDecision {
	d := assessDecide(payload, folder)
	if d.Deny {
		recordAssessDenial(folder, payload.ToolName, d.Reason)
	}
	return d
}

func assessDecide(payload HookPayload, folder string) PreToolDecision {
	deny := func(reason string) PreToolDecision {
		return PreToolDecision{Deny: true, Reason: reason + ": the assessor session reads its batch folder with Read, Grep and Glob, and writes only notice.json there; anything else ends the batch failed, with nothing sent"}
	}
	if !slices.Contains(AssessTools, payload.ToolName) {
		return deny(payload.ToolName + " is not the assessor's to use")
	}
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
		Glob     string `json:"glob"`
	}
	if err := json.Unmarshal(payload.ToolInput, &in); err != nil {
		return deny("its input does not parse")
	}
	cwd := payload.Cwd
	if cwd == "" {
		cwd = folder
	}
	root := resolveFull(folder)
	// A path is bound by where the OS would take it, and a `..` after a
	// link is not where its lexical cleaning says: a path with a `..`
	// element is refused outright (the folder's own paths never need one).
	inside := func(p string) bool {
		if p == "" {
			p = cwd
		}
		if hasDotDot(p) {
			return false
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		rel, err := filepath.Rel(root, resolveFull(filepath.Clean(p)))
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	switch payload.ToolName {
	case "Read":
		if in.FilePath == "" || !inside(in.FilePath) {
			return deny("Read of " + in.FilePath + " is outside the batch folder")
		}
	case "Grep":
		if !inside(in.Path) {
			return deny("Grep in " + in.Path + " is outside the batch folder")
		}
		if !localPattern(in.Glob) {
			return deny("Grep's glob " + in.Glob + " reaches outside the batch folder")
		}
	case "Glob":
		if !inside(in.Path) {
			return deny("Glob in " + in.Path + " is outside the batch folder")
		}
		if !localPattern(in.Pattern) {
			return deny("Glob's pattern " + in.Pattern + " reaches outside the batch folder")
		}
	case "Write":
		want := filepath.Join(root, AssessNotice)
		p := in.FilePath
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if in.FilePath == "" || hasDotDot(in.FilePath) || resolveFull(filepath.Clean(p)) != want {
			return deny("Write of " + in.FilePath + " is not the batch folder's notice.json")
		}
	}
	return PreToolDecision{}
}

// localPattern: a glob that is empty, or relative with no .. element, so
// it matches under its search root only.
func localPattern(g string) bool {
	if g == "" {
		return true
	}
	if filepath.IsAbs(g) || strings.HasPrefix(g, "~") {
		return false
	}
	return !hasDotDot(g)
}

// hasDotDot: p has a `..` element.
func hasDotDot(p string) bool {
	for el := range strings.SplitSeq(filepath.ToSlash(p), "/") {
		if el == ".." {
			return true
		}
	}
	return false
}

// resolveFull is p with every symbolic link resolved (R1): the whole path
// when it exists, else its nearest existing ancestor's, with the rest
// joined on.
func resolveFull(p string) string {
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// AssessDeniedPath is the file folder's denials are recorded in.
func AssessDeniedPath(folder string) string {
	return filepath.Clean(folder) + AssessDeniedSuffix
}

func recordAssessDenial(folder, tool, reason string) {
	line, err := json.Marshal(AssessDenial{At: time.Now().UTC().Format(time.RFC3339), Tool: tool, Reason: reason})
	if err != nil {
		return
	}
	f, err := os.OpenFile(AssessDeniedPath(folder), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

// AssessDenials are the denials recorded for folder's session.
func AssessDenials(folder string) []AssessDenial {
	data, err := os.ReadFile(AssessDeniedPath(folder))
	if err != nil {
		return nil
	}
	var out []AssessDenial
	for line := range strings.SplitSeq(string(data), "\n") {
		var d AssessDenial
		if json.Unmarshal([]byte(line), &d) == nil && d.Tool != "" {
			out = append(out, d)
		}
	}
	return out
}
