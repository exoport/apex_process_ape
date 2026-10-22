package apexv1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// Module fences (W7, D207, D337): a library: true project with a go.mod
// at the root gets one go.mod in each APEX folder that exists — .claude/,
// _apex/, the development folder, and a root evidence/ when there is one
// (a lifted project's; v1 keeps evidence inside the development folder) —
// so 3 in a new v1 library and 4 in a lifted one with evidence/. A folder
// holding a go.mod is another module, which the go command leaves out of
// the root module's zip, its build, vet and tests (measured on pdfa: 1,207
// files in the zip, 154 with the fences; tidy, verify and go work sync
// green, no file changed). A fence names no real module
// (apex.invalid/fence/<folder>) and copies the root's go line, so a gate
// program that needs more than the root module graph runs from its fence
// with its own require and replace lines (gaterule.go): fence.altered
// judges only the module and go lines. A module in a subfolder (the docs'
// option A) needs no fence: the APEX folders are outside it.

// FenceSubject is the subject of the commit `ape project fence` makes.
const FenceSubject = "apex: module fences"

// FenceDocsURL is the page a fence's comment points to.
const FenceDocsURL = "https://github.com/exoport/apex_process_ape/blob/main/docs/how-to/publish-a-library.md"

// FenceModulePrefix starts every fence's module path.
const FenceModulePrefix = "apex.invalid/fence/"

// The states of a fence, as `ape project fence` and its --check find it.
const (
	FenceOK          = "ok"
	FenceMissing     = "missing"
	FenceUncommitted = "uncommitted"
	FenceAltered     = "altered"
)

// Fence is one APEX folder's fence.
type Fence struct {
	Folder string `json:"folder"`
	Path   string `json:"path"`
	State  string `json:"state"`
	// Detail says what is altered.
	Detail string `json:"detail,omitempty"`
	// broken: it does not parse, so ape does not rewrite it.
	broken bool
}

// FenceResult is `ape project fence`'s report: what it found, what it
// wrote, and its commit.
type FenceResult struct {
	// Library is _apex/config.yaml's library: true; GoMod a go.mod at
	// the root. Without both there is nothing to fence.
	Library bool `json:"library"`
	GoMod   bool `json:"go_mod"`
	// Go is the root go.mod's go version, which every fence copies.
	Go     string  `json:"go,omitempty"`
	Fences []Fence `json:"fences"`
	// Written are the fences written or rewritten; with Commit, those
	// committed (an uncommitted one is committed as it is).
	Written []string `json:"written"`
	Commit  string   `json:"commit,omitempty"`
}

// Pending reports a fence that is not in place.
func (r *FenceResult) Pending() bool {
	for _, f := range r.Fences {
		if f.State != FenceOK {
			return true
		}
	}
	return false
}

// fenceContent is a new fence for folder, go the root's go version ("" to
// write no go line, as the root has none).
func fenceContent(folder, goVersion string) string {
	out := "// This folder is APEX's, not the library's: this go.mod keeps it out of the module zip.\n" +
		"// See " + FenceDocsURL + ". Written by ape project fence.\n" +
		"module " + FenceModulePrefix + folder + "\n"
	if goVersion != "" {
		out += "\ngo " + goVersion + "\n"
	}
	return out
}

// fenceFolders are the APEX folders that exist, in ApexFolders' order.
func (p *Project) fenceFolders() []string {
	var out []string
	for _, f := range p.ApexFolders() {
		if dirExists(filepath.Join(p.Root, filepath.FromSlash(f))) {
			out = append(out, f)
		}
	}
	return out
}

// errNoRootGoMod: the project has no go.mod at its root.
var errNoRootGoMod = errors.New("no go.mod at the root")

// rootGoMod is the root go.mod, parsed; errNoRootGoMod when there is
// none, a fence.unreadable usage error when it cannot be read or parsed.
func (p *Project) rootGoMod() (*modfile.File, error) {
	data, err := os.ReadFile(filepath.Join(p.Root, "go.mod"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoRootGoMod
	}
	if err == nil {
		var mf *modfile.File
		if mf, err = modfile.ParseLax("go.mod", data, nil); err == nil {
			return mf, nil
		}
	}
	return nil, &UsageError{Code: CodeFenceUnreadable, Msg: "go.mod cannot be read: " + err.Error() + " — fix it; the module fences copy its go line"}
}

// fencePlan is what `ape project fence` finds: each present APEX folder's
// fence and its state. Nothing to fence (no fences) without library: true
// or a root go.mod. A fence git does not track is uncommitted: the module
// zip is built from git, so it fences nothing yet.
func (p *Project) fencePlan(ctx context.Context) (*FenceResult, error) {
	res := &FenceResult{Library: p.IsLibrary(), Fences: []Fence{}, Written: []string{}}
	if !res.Library {
		return res, nil
	}
	root, err := p.rootGoMod()
	if errors.Is(err, errNoRootGoMod) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.GoMod = true
	if root.Go != nil {
		res.Go = root.Go.Version
	}
	var tracked map[string]bool
	if p.isWorkTree(ctx) {
		tracked = map[string]bool{}
		var rels []string
		for _, folder := range p.fenceFolders() {
			rels = append(rels, folder+"/go.mod")
		}
		if out, err := git(ctx, p.Root, append([]string{"ls-files", "--"}, rels...)...); err == nil {
			for l := range strings.SplitSeq(out, "\n") {
				tracked[strings.TrimSpace(l)] = true
			}
		}
	}
	for _, folder := range p.fenceFolders() {
		f := Fence{Folder: folder, Path: folder + "/go.mod", State: FenceOK}
		data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(f.Path)))
		switch {
		case errors.Is(err, os.ErrNotExist):
			f.State = FenceMissing
		case err != nil:
			return res, &UsageError{Code: CodeFenceUnreadable, Msg: f.Path + " cannot be read: " + err.Error() + " — fix its permissions, then run it again"}
		default:
			f.Detail, f.broken = judgeFence(f.Path, folder, data, res.Go)
			if f.Detail != "" {
				f.State = FenceAltered
			} else if tracked != nil && !tracked[f.Path] {
				f.State = FenceUncommitted
			}
		}
		res.Fences = append(res.Fences, f)
	}
	return res, nil
}

// judgeFence says what is altered in a fence: its module line not
// FenceModulePrefix+folder, or its go line not the root's. Everything
// else in it (a gate's require and replace lines, comments) is the
// project's. broken: it does not parse.
func judgeFence(rel, folder string, data []byte, rootGo string) (detail string, broken bool) {
	mf, err := modfile.Parse(rel, data, nil)
	if err != nil {
		return "it does not parse: " + strings.ReplaceAll(err.Error(), "\n", "; "), true
	}
	var what []string
	want := FenceModulePrefix + folder
	switch {
	case mf.Module == nil:
		what = append(what, "no module line, want module "+want)
	case mf.Module.Mod.Path != want:
		what = append(what, "module "+mf.Module.Mod.Path+", want "+want)
	}
	have := ""
	if mf.Go != nil {
		have = mf.Go.Version
	}
	if have != rootGo {
		switch {
		case rootGo == "":
			what = append(what, "go "+have+", and the root go.mod has no go line")
		case have == "":
			what = append(what, "no go line, want go "+rootGo+" as the root go.mod")
		default:
			what = append(what, "go "+have+", want go "+rootGo+" as the root go.mod")
		}
	}
	return strings.Join(what, "; "), false
}

// rewriteFence is an altered fence with its module and go lines set,
// every other line kept.
func rewriteFence(rel, folder string, data []byte, rootGo string) ([]byte, error) {
	mf, err := modfile.Parse(rel, data, nil)
	if err != nil {
		return nil, err
	}
	if err := mf.AddModuleStmt(FenceModulePrefix + folder); err != nil {
		return nil, err
	}
	if rootGo == "" {
		mf.DropGoStmt()
	} else if err := mf.AddGoStmt(rootGo); err != nil {
		return nil, err
	}
	mf.Cleanup()
	return mf.Format()
}

// fenceFinding is a pending fence as a project check finding.
func fenceFinding(f Fence) Finding {
	switch f.State {
	case FenceMissing:
		return Finding{File: f.Path, Message: "module fence " + f.Path + " is missing, so the library's module zip carries " + f.Folder +
			"/ — `ape project fence` writes it (see ape project fence --help)"}
	case FenceUncommitted:
		return Finding{File: f.Path, Message: "module fence " + f.Path + " is not committed, so the module zip, which is built from git, still carries " +
			f.Folder + "/ — `ape project fence` commits it"}
	}
	if f.broken {
		return Finding{File: f.Path, Message: "module fence " + f.Path + " is altered (" + f.Detail + ") — fix or delete it, then run `ape project fence`; ape does not overwrite it"}
	}
	return Finding{File: f.Path, Message: "module fence " + f.Path + " is altered (" + f.Detail + ") — `ape project fence` restores its module and go lines and keeps the rest"}
}

// fenceFindings are project check's fence findings (fence.missing,
// fence.altered), only for library: true with a go.mod at the root. A
// root go.mod that does not parse is the go command's to report.
func (p *Project) fenceFindings(ctx context.Context) []Finding {
	res, err := p.fencePlan(ctx)
	if err != nil {
		return nil
	}
	var out []Finding
	for _, f := range res.Fences {
		if f.State != FenceOK {
			out = append(out, fenceFinding(f))
		}
	}
	return out
}

// FenceCheck is `ape project fence --check`, a migration's check: nil
// when every present APEX folder's fence is in place (or there is nothing
// to fence), else fence.missing (any missing or uncommitted) or
// fence.altered, with a finding per fence (exit 1). It writes nothing.
func (p *Project) FenceCheck(ctx context.Context) (*FenceResult, error) {
	res, err := p.fencePlan(ctx)
	if err != nil || !res.Pending() {
		return res, err
	}
	code := CodeFenceAltered
	var fs []Finding
	for _, f := range res.Fences {
		if f.State == FenceOK {
			continue
		}
		if f.State != FenceAltered {
			code = CodeFenceMissing
		}
		fs = append(fs, fenceFinding(f))
	}
	return res, &FailError{Code: code, Msg: strconv.Itoa(len(fs)) + " module fence(s) not in place — `ape project fence` writes them in one commit", Findings: fs}
}

// Fence is `ape project fence`: the missing fences written, the altered
// ones' module and go lines restored, and those with an uncommitted one
// committed, in ONE commit, FenceSubject. Idempotent: with every fence in
// place it writes nothing. Refused (nothing written) outside a git
// repository, on a change branch, while a fence it would rewrite has
// uncommitted changes, and for a fence that does not parse.
func (p *Project) Fence(ctx context.Context) (*FenceResult, error) {
	res, err := p.fencePlan(ctx)
	if err != nil || !res.Pending() {
		return res, err
	}
	for _, f := range res.Fences {
		if f.broken {
			return res, &FailError{
				Code: CodeFenceAltered, Msg: f.Path + " does not parse (" + f.Detail + ") — fix or delete it, then run `ape project fence` again; nothing was written",
				Findings: []Finding{fenceFinding(f)},
			}
		}
	}
	if !p.isWorkTree(ctx) {
		return res, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — the module fences are one commit, and the module zip is built from git"}
	}
	if err := p.recoverArchive(ctx); err != nil {
		return res, err
	}
	if err := p.refuseOnChangeBranch(ctx, "the module fences"); err != nil {
		return res, err
	}
	var paths, rewrite []string
	for _, f := range res.Fences {
		if f.State != FenceOK {
			paths = append(paths, f.Path)
		}
		if f.State == FenceAltered {
			rewrite = append(rewrite, f.Path)
		}
	}
	if len(rewrite) > 0 {
		if dirty, err := gitRaw(ctx, p.Root, append([]string{"status", "--porcelain=v1", untrackedNormal, "--"}, rewrite...)...); err != nil {
			return res, &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
		} else if strings.TrimSpace(dirty) != "" {
			return res, &FailError{Code: "tree.dirty", Msg: "the module fences are one commit, and " + strings.Join(rewrite, ", ") +
				" has uncommitted changes — commit or discard them first, then run it again"}
		}
	}
	abs := func(rel string) string { return filepath.Join(p.Root, filepath.FromSlash(rel)) }
	before := map[string][]byte{}
	undo := func(cause string) error {
		for _, rel := range paths {
			if data, ok := before[rel]; ok {
				_ = writeFileAtomic(abs(rel), string(data))
			} else if slices.Contains(res.Written, rel) {
				_ = os.Remove(abs(rel))
			}
		}
		_, _ = git(ctx, p.Root, append([]string{"reset", "-q", "--"}, paths...)...)
		return &FailError{Code: CodeCommitFailed, Msg: "the module fences failed: " + cause + " — nothing was committed and the files are as they were; fix the cause and run it again"}
	}
	for _, f := range res.Fences {
		switch f.State {
		case FenceMissing:
			if err := writeFileAtomic(abs(f.Path), fenceContent(f.Folder, res.Go)); err != nil {
				return res, undo(err.Error())
			}
			res.Written = append(res.Written, f.Path)
		case FenceAltered:
			data, err := os.ReadFile(abs(f.Path))
			if err != nil {
				return res, undo(err.Error())
			}
			before[f.Path] = data
			out, err := rewriteFence(f.Path, f.Folder, data, res.Go)
			if err != nil {
				return res, undo(err.Error())
			}
			if err := writeFileAtomic(abs(f.Path), string(out)); err != nil {
				return res, undo(err.Error())
			}
			res.Written = append(res.Written, f.Path)
		}
	}
	if err := stageFences(ctx, p.Root, paths); err != nil {
		return res, undo(err.Error())
	}
	sha, err := p.commitRecord(ctx, FenceSubject, "", paths...)
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

// stageFences stages fences with -f: the .claude/ shape init puts in the
// root .gitignore (D203) ignores .claude/go.mod, and a fence git does
// not hold fences nothing. Once tracked, the ignore no longer applies.
func stageFences(ctx context.Context, root string, rels []string) error {
	if len(rels) == 0 {
		return nil
	}
	_, err := git(ctx, root, append([]string{"add", "-f", "--"}, rels...)...)
	return err
}

// writeMissingFences writes the fences missing from the present APEX
// folders of a library: true project with a root go.mod, stages them
// in a git work tree (stageFences), and returns them (relative,
// sorted): init and the lift, which commit them with their own commit
// (or, in a model session init withholds its commit from, leave them
// staged for the session's). An existing fence, altered or not, is
// left to `ape project fence`: init never overwrites a file.
func (p *Project) writeMissingFences(ctx context.Context) ([]string, error) {
	res, err := p.fencePlan(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range res.Fences {
		if f.State != FenceMissing {
			continue
		}
		if err := writeFileAtomic(filepath.Join(p.Root, filepath.FromSlash(f.Path)), fenceContent(f.Folder, res.Go)); err != nil {
			return out, err
		}
		out = append(out, f.Path)
	}
	sort.Strings(out)
	if p.isWorkTree(ctx) {
		if err := stageFences(ctx, p.Root, out); err != nil {
			return out, err
		}
	}
	return out, nil
}
