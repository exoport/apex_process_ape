package apexv1

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// `ape project lift` is the mechanical half of the v0 → v1 migration
// (backlog 39, BRIEF D36, D37, D47 Q4): one commit, on a tree whose v0
// state is archived at the pushed tag apex-v0-final, that deletes the v0
// families, converts the ADRs, strips v0 ids from code comments, carries
// the gate commands over, lays down the v1 skeleton and writes the
// disposition file apex-plan works from. Everything it does is
// deterministic; the judgment (which rule lives where, what M1 delivers)
// is apex-plan's and the person's.

// LiftOptions are the knobs of one lift.
type LiftOptions struct {
	// DryRun computes and returns the whole plan and writes nothing;
	// pre-flight problems are reported instead of refused.
	DryRun bool
	// FromUpdate is the lift run by `ape framework update` (the v1.0.0
	// migration entry): uncommitted changes are allowed where the
	// framework install writes (.claude/skills/, _apex/, .aboard/,
	// .gitignore), and the lift leaves them to the install's own commit.
	FromUpdate bool
	// NoVerifyRun skips running the regression command after the commit.
	NoVerifyRun bool
	// Exclude are globs (** allowed) of files the lift must not edit —
	// a byte-identity perimeter, say. Deletions are not affected.
	Exclude []string
	Run     RunOptions
	// Ape is the binary running the lift and the project's pin of ape,
	// filled by the command (L1); nil leaves the pin alone.
	Ape *LiftApe
}

// LiftApe is which ape runs the lift, and what happens to the project's
// ape pin (.bingo/ape.mod): a v0 pin names an ape with no v1 commands, so
// the lift moves it to the running version in its commit, or says the
// one command that does.
type LiftApe struct {
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
	// PinFile is the project's pin, "" when it has none; Pinned the
	// version it names.
	PinFile string `json:"pin_file,omitempty"`
	Pinned  string `json:"pinned,omitempty"`
	// PinMoved: the lift commit moves the pin to Version.
	PinMoved bool `json:"pin_moved,omitempty"`
	// PinCommand is the one command that moves the pin, when the lift did
	// not (or, in a dry run, will not).
	PinCommand string `json:"pin_command,omitempty"`
	PinNote    string `json:"pin_note,omitempty"`
	// MovePin moves the pin in the working tree (bingo get); nil when the
	// lift cannot, and PinNote says why.
	MovePin func(ctx context.Context) error `json:"-"`
}

// LiftProblem is a pre-flight refusal a dry run reports.
type LiftProblem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LiftSkip is a file the lift did not edit, and why.
type LiftSkip struct {
	File         string `json:"file"`
	Reason       string `json:"reason"`
	IDs          int    `json:"ids,omitempty"`
	ADRCitations int    `json:"adr_citations,omitempty"`
}

// LiftFileChange is one file the lift edited.
type LiftFileChange struct {
	File          string `json:"file"`
	IDsRemoved    int    `json:"comments_stripped,omitempty"`
	ADRsRewritten int    `json:"adr_citations_rewritten,omitempty"`
}

// LiftCandidate is one binding-rule candidate for the disposition file.
type LiftCandidate struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// LiftResult is the lift's report (and, with DryRun, its plan).
type LiftResult struct {
	Root   string `json:"root"`
	DryRun bool   `json:"dry_run"`
	// Ape is the binary that runs the lift and the fate of the pin (L1).
	Ape           *LiftApe `json:"ape,omitempty"`
	Version       string   `json:"v0_version"`
	VersionSource string   `json:"v0_version_source"`
	SkillsSource  string   `json:"skills_source"`
	Tag           string   `json:"tag"`
	TagSHA        string   `json:"tag_sha,omitempty"`
	Remote        string   `json:"remote,omitempty"`
	// Preflight lists what a real run would refuse (dry run only).
	Preflight []LiftProblem    `json:"preflight,omitempty"`
	Deleted   []LiftDeletion   `json:"deleted"`
	KeptApex  []string         `json:"kept_apex,omitempty"`
	ADRs      []LiftADR        `json:"adrs"`
	Canon     string           `json:"canon,omitempty"`
	Changed   []LiftFileChange `json:"changed"`
	// ADRsNotConverted are record-shaped files no id could be read from:
	// left in place.
	ADRsNotConverted    []string    `json:"adrs_not_converted,omitempty"`
	UnresolvedCitations []LiftIDRef `json:"unresolved_adr_citations,omitempty"`
	IDsLeft             []LiftIDRef `json:"ids_left,omitempty"`
	Skipped             []LiftSkip  `json:"skipped,omitempty"`
	// GoUnformatted are edited Go files that were not gofmt-clean at the
	// tag: their edit is not formatted either (L5).
	GoUnformatted []string    `json:"go_not_formatted,omitempty"`
	DanglingRefs  []LiftIDRef `json:"references_to_removed_paths,omitempty"`
	// Hashed are edited files a test, CI script or lock file seems to pin
	// the bytes of: a perimeter to --exclude before lifting (L6). Those a
	// lock file names come first, then those a hasher names, then those
	// tied to one by their folder (R6). A dry run lists every one; a real
	// lift at most hashedCap (the report has all). HashedTotal counts
	// them (R1).
	Hashed      []LiftHashed `json:"edited_but_hashed,omitempty"`
	HashedTotal int          `json:"edited_but_hashed_total,omitempty"`
	// V0StateReaders are kept code, test, script, Makefile and CI files
	// that name v0 state the lift deletes outside their comments, with
	// what each reads: they go red once it is gone, and are re-pointed to
	// v1 state or retired (D141).
	V0StateReaders []LiftV0Reader  `json:"v0_state_readers,omitempty"`
	Gates          LiftGates       `json:"gates"`
	Candidates     []LiftCandidate `json:"rule_candidates"`
	Readings       []string        `json:"readings,omitempty"`
	Disposition    string          `json:"disposition"`
	// Report is the disposition's full lists, as JSON (L11): written to
	// ape's ignored state folder after the commit, never committed (R3).
	Report        string       `json:"report"`
	Init          *InitResult  `json:"init,omitempty"`
	CommitMessage string       `json:"commit_message"`
	Commit        string       `json:"commit,omitempty"`
	Regression    *CheckResult `json:"regression,omitempty"`
	// RegressionAtTag is the same command run at the tag, before anything
	// is written: the baseline that tells a pre-existing red from one the
	// lift caused (L4).
	RegressionAtTag *CheckResult `json:"regression_at_tag,omitempty"`
	// RegressionVerdict is one of the LiftRegression* values.
	RegressionVerdict string `json:"regression_verdict,omitempty"`
	RegressionNote    string `json:"regression_note,omitempty"`
	// RegressionNewFailures are the failing tests after the lift that the
	// run at the tag did not have (red-at-tag-new-failures; at most
	// liftNewFailuresShown, the note counts them all) (R4).
	RegressionNewFailures []string `json:"regression_new_failures,omitempty"`
	// RegressionCommands: the regression command is a top-level && chain,
	// so each of its commands ran on its own at both points, past a red
	// one, and was judged on its own (R12); the regression_* fields above
	// are then the worst command's. Absent for a command that does not
	// split, which runs and is judged whole.
	RegressionCommands []LiftRegressionCommand `json:"regression_commands,omitempty"`
}

// LiftRegressionCommand is one command of a split regression chain,
// judged at the lift commit against its own run at the tag (R12).
type LiftRegressionCommand struct {
	Cmd string `json:"cmd"`
	// Verdict is one of the LiftRegression* values; Note says why.
	Verdict         string       `json:"verdict"`
	Note            string       `json:"note"`
	NewFailures     []string     `json:"new_failures,omitempty"`
	Regression      *CheckResult `json:"regression,omitempty"`
	RegressionAtTag *CheckResult `json:"regression_at_tag,omitempty"`
}

// The lift's regression verdicts: the command at the lift commit judged
// against the same command at the tag (L4).
const (
	LiftRegressionGreen = "green"
	// LiftRegressionRedAtTag: red at the tag too, with the same failing
	// tests (or fewer) — pre-existing, for apex-plan to roadmap; or, when
	// no test names could be read, with the same output, else a note that
	// the output changed after the lift (R4).
	LiftRegressionRedAtTag = "red-at-tag"
	// LiftRegressionRedAtTagNewFailures: red at the tag too, but after the
	// lift tests fail that did not at the tag — those are the lift's (R4).
	LiftRegressionRedAtTagNewFailures = "red-at-tag-new-failures"
	// LiftRegressionRedAfterLift: green at the tag, red at the lift
	// commit — the lift broke it, a lift failure for the person.
	LiftRegressionRedAfterLift = "red-after-lift"
	// LiftRegressionCannotRun: exit 126 or 127 at the lift commit as at
	// the tag, or an output saying a command was not found with make's
	// Error 127 (R17) — a tool it runs is missing or not executable here,
	// which is not a red (D70 B).
	LiftRegressionCannotRun = "cannot-run"
	// LiftRegressionNoBaseline: red at the lift commit, and the run at
	// the tag could not run, so nothing says whose red it is.
	LiftRegressionNoBaseline = "red-no-baseline"
)

type lifter struct {
	ctx    context.Context //nolint:containedctx // a lifter lives for one Lift call and carries that call's ctx
	root   string
	opt    LiftOptions
	layout *liftLayout
	devRel string
	// projectContext is the project-context.md the lift reads, relative.
	projectContext string
	// dirty are paths the framework install changed (FromUpdate): left
	// alone.
	dirty    map[string]bool
	excludes []*regexp.Regexp
	res      *LiftResult
	// writes are the files the lift writes, relative path → content.
	writes map[string]string
	// deletes are the pathspecs it removes; keepADR the record files of
	// the ADR folder it could not convert (kept).
	deletes []string
	keepADR map[string]bool
	// untracked are the manifest's v0 skill folders git does not track:
	// removed from the working tree after the commit (L2).
	untracked []string
	// texts caches the tracked text files read for the plan.
	texts map[string]string
	files []string
	// hashedAll is every hashed edit; res.Hashed the first hashedCap, in
	// a real lift.
	hashedAll []LiftHashed
	// hashedRank is each hashed edit's tier (hashedByLock, …): the
	// disposition file folds the folder tier into one line per hasher
	// (R22).
	hashedRank map[string]int
	// reportJSON is the report, written to res.Report once the commit
	// stands.
	reportJSON string
	// claudeBefore is CLAUDE.md with citations rewritten, managed block
	// still in: the line numbers of the tag.
	claudeBefore string
	rw           *adrRewriter
	// tagRuns are the regression command's runs at the tag, one per
	// command of a split chain (R12), and tagLogs their logs, outside the
	// tree until the lift's state folder exists.
	tagRuns []CheckResult
	tagLogs []string
}

// readRel reads a file as the tag has it — for the inputs the lift
// deletes (project-context, team memory) — with ADR citations
// renumbered.
func (lf *lifter) readRel(rel string) string {
	data, err := os.ReadFile(filepath.Join(lf.root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	out, _, _ := lf.rw.rewrite(string(data))
	return out
}

// Lift runs (or, with DryRun, plans) the lift of the v0 project at root.
func Lift(ctx context.Context, root string, opt LiftOptions) (*LiftResult, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	lf := &lifter{ctx: ctx, root: abs, opt: opt, writes: map[string]string{}, keepADR: map[string]bool{}, texts: map[string]string{}, dirty: map[string]bool{}}
	lf.res = &LiftResult{Root: abs, DryRun: opt.DryRun, Tag: LiftTag, Ape: opt.Ape}
	for _, g := range opt.Exclude {
		re, err := globRegexp(g)
		if err != nil {
			return nil, &UsageError{Code: "usage.lift-exclude", Msg: "--exclude " + g + " is not a valid glob: " + err.Error() + " — use path globs like schema/** or deploy/init/*.sh"}
		}
		lf.excludes = append(lf.excludes, re)
	}
	if _, err := git(ctx, abs, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, &FailError{Code: "git.not-a-repo", Msg: abs + " is not a git repository — the lift is one commit, archived by a tag"}
	}
	if err := lf.refuseV1(); err != nil {
		return nil, err
	}
	if lf.layout, err = detectV0(ctx, abs); err != nil {
		return nil, err
	}
	lf.devRel = DefaultDevFolder
	if r := lf.rel(lf.layout.Cfg.Paths.Development); r != "" {
		lf.devRel = r
	}
	lf.res.Version, lf.res.VersionSource = lf.layout.Version, lf.layout.VersionSource
	for _, fe := range lf.preflight() {
		if !opt.DryRun {
			return nil, fe
		}
		lf.res.Preflight = append(lf.res.Preflight, LiftProblem{Code: fe.Code, Message: fe.Msg})
	}
	if err := lf.plan(); err != nil {
		return nil, err
	}
	if fe := lf.ignoredWrites(); fe != nil {
		if !opt.DryRun {
			return nil, fe
		}
		lf.res.Preflight = append(lf.res.Preflight, LiftProblem{Code: fe.Code, Message: fe.Msg})
	}
	if opt.DryRun {
		return lf.res, nil
	}
	if err := lf.apply(); err != nil {
		return lf.res, err
	}
	return lf.res, nil
}

func (lf *lifter) rel(abs string) string {
	if abs == "" {
		return ""
	}
	r, err := filepath.Rel(lf.root, abs)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return ""
	}
	return filepath.ToSlash(r)
}

// refuseV1 refuses a project that already has the v1 layout or a lift
// commit.
func (lf *lifter) refuseV1() error {
	dev := DefaultDevFolder
	if data, err := os.ReadFile(ConfigPath(lf.root)); err == nil {
		var c Config
		if yaml.Unmarshal(data, &c) == nil && c.DevelopmentFolder != "" {
			dev = strings.Trim(filepath.ToSlash(c.DevelopmentFolder), "/")
		}
	}
	p := &Project{Root: lf.root, DevRel: dev}
	why := ""
	switch {
	case p.IsV1():
		why = "it has the v1 layout (" + p.StateRel() + "/ or " + dev + "/project.md)"
	default:
		if out, _ := git(lf.ctx, lf.root, "log", "-1", "--format=%h %s", "--grep=^"+LiftSubjectPrefix); out != "" {
			why = "it was lifted already (" + out + ")"
		}
	}
	if why != "" {
		return &FailError{Code: "lift.not-v0", Msg: "the project is APEX v1 already: " + why + " — nothing to lift; `ape project check` and `ape status` show where it stands"}
	}
	return nil
}

// installTerritory is where `ape framework update` writes: the paths a
// lift run from inside it tolerates as uncommitted.
func installTerritory(rel string) bool {
	switch {
	case rel == ".gitignore", strings.HasPrefix(rel, ".claude/skills/"), strings.HasPrefix(rel, ".aboard/"):
		return true
	case strings.HasPrefix(rel, "_apex/"):
		return rel != configYAML && !strings.HasPrefix(rel, "_apex/state/")
	}
	return false
}

// preflight checks the tree and the tag. The tag check is the one network
// call: git ls-remote against the current branch's upstream remote.
func (lf *lifter) preflight() []*FailError {
	var out []*FailError
	status, err := gitRaw(lf.ctx, lf.root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return []*FailError{{Code: CodeGitStatusFailed, Msg: "git status: " + err.Error() + " — fix the repository, then lift"}}
	}
	var dirty []string
	for line := range strings.SplitSeq(status, "\n") {
		if len(line) <= 3 {
			continue
		}
		p := line[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		p = strings.Trim(p, "\"")
		if lf.opt.FromUpdate && installTerritory(p) {
			lf.dirty[strings.TrimSuffix(p, "/")] = true
			continue
		}
		dirty = append(dirty, p)
	}
	if len(dirty) > 0 {
		shown := dirty
		if len(shown) > 5 {
			shown = append(shown[:5:5], fmt.Sprintf("and %d more", len(dirty)-5))
		}
		out = append(out, &FailError{Code: "lift.dirty", Msg: "uncommitted changes: " + strings.Join(shown, ", ") +
			" — commit or stash them (on a branch of their own if they are work in flight), then tag and lift"})
	}

	// Every problem is reported, not only the first (L14): a dry run is
	// the person's one look before the real run.
	tagSHA, err := git(lf.ctx, lf.root, "rev-parse", "-q", "--verify", "refs/tags/"+LiftTag+"^{commit}")
	if err != nil || tagSHA == "" {
		tagSHA = ""
		out = append(out, &FailError{Code: "lift.no-tag", Msg: "no tag " + LiftTag + " — the tag is the archive of everything the lift deletes: " +
			"`git tag -a " + LiftTag + " -m \"APEX v0 final\"` on this commit, push it, then lift"})
	} else {
		lf.res.TagSHA = tagSHA
		if head := HeadSHA(lf.ctx, lf.root); head != tagSHA {
			out = append(out, &FailError{Code: "lift.tag-not-head", Msg: LiftTag + " is " + short(tagSHA) + ", HEAD is " + short(head) +
				" — the lift starts from the tagged commit: check it out, or (a person's call) move the tag to HEAD and push it again"})
		}
	}
	branch := CurrentBranch(lf.ctx, lf.root)
	remote := ""
	if branch != "" {
		remote, _ = git(lf.ctx, lf.root, "config", "--get", "branch."+branch+".remote")
	}
	if remote == "" || remote == "." {
		return append(out, &FailError{Code: CodeTagNotPushed, Msg: "the current branch has no upstream remote, so " + LiftTag +
			" cannot be shown to be pushed — set one (`git branch -u <remote>/<branch>`), `git push <remote> " + LiftTag + "`, then lift"})
	}
	lf.res.Remote = remote
	ls, err := git(lf.ctx, lf.root, "ls-remote", "--tags", remote, "refs/tags/"+LiftTag, "refs/tags/"+LiftTag+"^{}")
	if err != nil {
		return append(out, &FailError{Code: "lift.remote-unreachable", Msg: "git ls-remote " + remote + ": " + err.Error() +
			" — reach the remote (network, credentials), then lift again"})
	}
	if tagSHA == "" {
		return out
	}
	remoteSHA, peeled := "", ""
	for line := range strings.SplitSeq(ls, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if strings.HasSuffix(f[1], "^{}") {
			peeled = f[0]
		} else {
			remoteSHA = f[0]
		}
	}
	if peeled != "" {
		remoteSHA = peeled
	}
	switch {
	case remoteSHA == "":
		out = append(out, &FailError{Code: CodeTagNotPushed, Msg: LiftTag + " is not on " + remote + " — `git push " + remote + " " + LiftTag + "`, then lift"})
	case remoteSHA != tagSHA:
		out = append(out, &FailError{Code: CodeTagNotPushed, Msg: LiftTag + " on " + remote + " is " + short(remoteSHA) + ", not " + short(tagSHA) +
			" — the archive must be the commit being lifted: a person resolves which is right, then pushes it"})
	}
	return out
}

// ignoredWrites is the pre-flight check a real lift used to fail on after
// a passing dry run (L2, mdpdf and pdfa): a path the lift commits that git
// ignores. `git add` refuses it, and the lift stopped half-staged. Above
// all .claude/settings.json, where the v1 hooks go: an ignored .claude/
// would also keep them from every other checkout. Tracked paths are never
// ignored, so only the ones the lift creates count.
func (lf *lifter) ignoredWrites() *FailError {
	p := &Project{Root: lf.root, DevRel: lf.devRel}
	files := []string{p.SettingsJSON(), p.ProjectMD(), p.RoadmapMD(), p.AgentsMD(), p.ClaudeMD(), p.StateGitignore(), p.decisionsJSON()}
	dirs := []string{p.SpecsDir(), p.ChangesDir(), p.ArchiveDir(), p.DecisionsDir(), p.BriefsDir()}
	paths := make([]string, 0, len(files)+len(dirs)+len(lf.writes))
	for _, f := range files {
		paths = append(paths, p.Rel(f))
	}
	for _, d := range dirs {
		paths = append(paths, p.Rel(filepath.Join(d, ".gitkeep")))
	}
	for rel := range lf.writes {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	ignored := gitIgnored(lf.ctx, lf.root, false, uniqueStrings(paths)...)
	if len(ignored) == 0 {
		return nil
	}
	fix := "un-ignore them"
	if slices.Contains(ignored, ".claude/settings.json") {
		fix = "un-ignore them — for .claude/, `.claude/*` and `!.claude/settings.json` in .gitignore, which also shares the v1 hooks with every checkout"
	}
	return &FailError{Code: CodeClaudeIgnored, Msg: "git ignores what the lift commits: " + strings.Join(ignored, ", ") +
		", so `git add` would refuse it mid-lift — " + fix + "; commit that, move " + LiftTag + " to it and push the tag, then lift"}
}

// excluded reports a path --exclude names.
func (lf *lifter) excluded(rel string) bool {
	for _, re := range lf.excludes {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// isDeleted reports whether rel goes with a deleted family.
func (lf *lifter) isDeleted(rel string) bool {
	if lf.keepADR[rel] {
		return false
	}
	for _, d := range lf.deletes {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// dirtyAt reports a path the framework install changed (FromUpdate).
func (lf *lifter) dirtyAt(rel string) bool {
	for d := range lf.dirty {
		if d == rel || strings.HasPrefix(d, rel+"/") || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// globRegexp compiles a path glob: ** any depth, * and ? within a
// segment; a trailing / names a folder.
func globRegexp(g string) (*regexp.Regexp, error) {
	g = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(g)), "./")
	if g == "" {
		return nil, errors.New("empty")
	}
	if strings.HasSuffix(g, "/") {
		g += "**"
	}
	if _, err := path.Match(strings.ReplaceAll(g, "**", "*"), ""); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ---- plan ---------------------------------------------------------------

const liftMaxFileBytes = 4 << 20

// liftBinarySniff is how much of a file is searched for a NUL byte to
// call it binary: git's own heuristic.
const liftBinarySniff = 8000

//nolint:gocyclo,maintidx // the lift plan is one ordered pass — ADRs, renumbering, edits, gates, CLAUDE.md — each step reading the last's results
func (lf *lifter) plan() error {
	ctx := lf.ctx
	paths := lf.layout.Cfg.Paths
	lf.projectContext = lf.findProjectContext()

	// The ADR records, before their folder's deletion is planned: a record
	// no id can be read from stays.
	var local []v0ADR
	if paths.ADRs != "" {
		var bad []string
		local, bad = loadADRs(lf.root, paths.ADRs, false)
		for _, b := range bad {
			lf.keepADR[b] = true
		}
		lf.res.ADRsNotConverted = bad
	}
	for _, f := range lf.families(ctx) {
		for _, p := range f.Paths {
			lf.deletes = append(lf.deletes, p)
			lf.res.Deleted = append(lf.res.Deleted, LiftDeletion{Path: p, Family: f.Name})
		}
	}
	for _, s := range lf.untracked {
		lf.res.Deleted = append(lf.res.Deleted, LiftDeletion{Path: s, Family: "v0 skills", Untracked: true})
	}
	lf.res.SkillsSource = lf.layout.SkillsSource
	lf.listKeptApex()

	if err := lf.loadFiles(); err != nil {
		return err
	}

	// Old → new ADR numbers: the local records in order, then the canon
	// records the project cites and has no local record of.
	num := map[int]int{}
	pathMap := map[string]string{}
	decisionsRel := lf.devRel + "/decisions"
	next := 1
	var all []v0ADR
	dups := map[int]string{} // index in all → the ADR its id's citations go to
	for i := range local {
		a := local[i]
		if first, dup := num[a.num]; dup {
			a.slug += "-" + fmtNum4(next)
			dups[len(all)] = "ADR-" + fmtNum4(first)
		} else {
			num[a.num] = next
		}
		all = append(all, a)
		next++
	}
	cited := map[int]bool{}
	for _, t := range lf.texts {
		for _, n := range citedADRs(t) {
			if _, ok := num[n]; !ok {
				cited[n] = true
			}
		}
	}
	for i := range local {
		for _, n := range citedADRs(local[i].body) {
			if _, ok := num[n]; !ok {
				cited[n] = true
			}
		}
	}
	if len(cited) > 0 {
		gov := lf.layout.Cfg.GovernanceRepositoryPath
		switch {
		case gov == "" || !dirExists(gov):
			lf.res.Canon = "cited ADR numbers with no local record: " + intList(cited) +
				"; the governance repository is not resolvable here (governance_repository_path/url), so no canon ADR was copied — they are listed under unresolved citations"
		default:
			canon := findCanonADRs(gov, cited)
			for i := range canon {
				num[canon[i].num] = next
				all = append(all, canon[i])
				next++
			}
			var got []string
			for i := range canon {
				got = append(got, "ADR-"+fmtNum4(canon[i].num))
			}
			lf.res.Canon = "governance repository " + gov + ": copied " + strings.Join(got, ", ")
			if len(got) == 0 {
				lf.res.Canon = "governance repository " + gov + ": none of " + intList(cited) + " found there"
			}
		}
	}
	for i := range all {
		if a := &all[i]; !a.canon {
			pathMap[a.rel] = decisionsRel + "/" + fmtNum4(i+1) + "-" + a.slug + ".md"
		}
	}
	rw := newADRRewriter(num, pathMap, lf.rel(paths.ADRs))
	lf.rw = rw
	for i := range all {
		a := &all[i]
		date := ""
		if !a.canon {
			date = gitFirstDate(ctx, lf.root, a.rel)
		}
		la := convertADR(*a, i+1, rw, date)
		la.Path = decisionsRel + "/" + la.Path
		if first, ok := dups[i]; ok {
			la.Gaps = append(la.Gaps, "a second v0 record with id "+la.Old+": citations of that id were rewritten to "+first)
		}
		for _, u := range la.unresolved {
			lf.res.UnresolvedCitations = append(lf.res.UnresolvedCitations, LiftIDRef{File: la.Path, Text: u, Where: "decision"})
		}
		lf.writes[la.Path] = la.doc
		lf.res.ADRs = append(lf.res.ADRs, la)
	}

	// Edits: ADR citations everywhere they are prose or comments, ids out
	// of comments.
	vocab := newLiftVocab(storyKeys(paths.Implementation), []string{lf.rel(paths.Deferred), lf.rel(paths.Implementation)})
	for _, rel := range lf.files {
		src, ok := lf.texts[rel]
		if !ok {
			continue
		}
		lang := liftLangOf(rel, []byte(src[:min(len(src), 256)]))
		isMD := strings.HasSuffix(strings.ToLower(rel), ".md") || strings.HasSuffix(strings.ToLower(rel), ".markdown")
		if lf.excluded(rel) {
			if n, a := len(vocab.spans(src)), len(citedADRs(src)); n+a > 0 {
				lf.res.Skipped = append(lf.res.Skipped, LiftSkip{File: rel, Reason: "excluded (--exclude)", IDs: n, ADRCitations: a})
			}
			continue
		}
		switch {
		case isMD:
			out, n, unresolved := rw.rewrite(src)
			if n > 0 {
				lf.writes[rel] = out
				lf.res.Changed = append(lf.res.Changed, LiftFileChange{File: rel, ADRsRewritten: n})
			}
			lf.unresolvedIn(rel, src, unresolved)
		case lang != langNone:
			ed, err := editComments(rel, src, lang, vocab, rw)
			if err != nil {
				if n, a := len(vocab.spans(src)), len(citedADRs(src)); n+a > 0 {
					lf.res.Skipped = append(lf.res.Skipped, LiftSkip{File: rel, Reason: lang.String() + ": " + err.Error(), IDs: n, ADRCitations: a})
				}
				continue
			}
			if lang == langGo && ed.New != src {
				out, why := gofmtLifted(src, ed.New)
				switch {
				case why != "":
					if n, a := len(vocab.spans(src)), len(citedADRs(src)); n+a > 0 {
						lf.res.Skipped = append(lf.res.Skipped, LiftSkip{File: rel, Reason: "go: " + why + ": left unedited", IDs: n, ADRCitations: a})
					}
					continue
				case out == "":
					lf.res.GoUnformatted = append(lf.res.GoUnformatted, rel)
				default:
					ed.New = out
				}
			}
			if ed.New != src {
				lf.writes[rel] = ed.New
				lf.res.Changed = append(lf.res.Changed, LiftFileChange{File: rel, IDsRemoved: ed.IDsRemoved, ADRsRewritten: ed.ADRsRewritten})
			}
			lf.res.IDsLeft = append(lf.res.IDsLeft, ed.Left...)
			lf.res.UnresolvedCitations = append(lf.res.UnresolvedCitations, ed.Unresolved...)
		default:
			if n, a := len(vocab.spans(src)), len(citedADRs(src)); n+a > 0 {
				lf.res.Skipped = append(lf.res.Skipped, LiftSkip{File: rel, Reason: "not tokenized (" + path.Ext(rel) + "): ids and ADR citations left as written", IDs: n, ADRCitations: a})
			}
		}
	}

	// Gate commands.
	if lf.projectContext != "" {
		lf.res.Gates = extractGates(lf.projectContext, lf.readRel(lf.projectContext))
	}
	if lf.res.Gates.Source == "" {
		if doc, ok := lf.texts["CLAUDE.md"]; ok {
			lf.res.Gates = extractGates("CLAUDE.md", doc)
		}
	}

	// CLAUDE.md: the managed block imports a fragment the lift deletes.
	if doc, ok := lf.current("CLAUDE.md"); ok {
		lf.claudeBefore = doc
		if out := lf.dropV0Imports(doc); out != doc {
			lf.writes["CLAUDE.md"] = out
		}
	}
	lf.writes[configYAML] = lf.v1Config()
	lf.res.Candidates = lf.candidates()
	lf.res.Readings = lf.readings()
	lf.res.DanglingRefs = lf.dangling()
	lf.res.V0StateReaders = lf.v0Readers()
	lf.hashedAll = lf.hashed()
	lf.res.Hashed, lf.res.HashedTotal = lf.hashedAll, len(lf.hashedAll)
	if !lf.opt.DryRun {
		lf.res.Hashed = lf.hashedAll[:min(len(lf.hashedAll), hashedCap)]
	}
	sort.Slice(lf.res.Changed, func(i, j int) bool { return lf.res.Changed[i].File < lf.res.Changed[j].File })
	lf.res.Disposition = lf.devRel + "/" + LiftDisposition
	lf.res.Report = (&Project{Root: lf.root}).StateRel() + "/" + LiftReport
	lf.reportJSON = lf.report()
	lf.writes[lf.res.Disposition] = lf.disposition()
	lf.res.CommitMessage = lf.commitMessage()
	return nil
}

func intList(set map[int]bool) string {
	ns := make([]int, 0, len(set))
	for n := range set {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, "ADR-"+fmtNum4(n))
	}
	return strings.Join(out, ", ")
}

func (lf *lifter) unresolvedIn(rel, src string, cites []string) {
	if len(cites) == 0 {
		return
	}
	want := map[string]bool{}
	for _, c := range cites {
		want[c] = true
	}
	for i, l := range strings.Split(src, "\n") {
		for _, m := range adrCiteRe.FindAllStringSubmatch(l, -1) {
			for p := range strings.SplitSeq(m[1]+m[2], "/") {
				if p != "" && want["ADR-"+p] {
					lf.res.UnresolvedCitations = append(lf.res.UnresolvedCitations, LiftIDRef{File: rel, Line: i + 1, Text: "ADR-" + p, Where: "docs"})
				}
			}
		}
	}
}

// current is a file's content as the lift will leave it.
func (lf *lifter) current(rel string) (string, bool) {
	if s, ok := lf.writes[rel]; ok {
		return s, true
	}
	s, ok := lf.texts[rel]
	return s, ok
}

// findProjectContext is the project-context.md the v0 layout keeps: the
// derived path, else the one tracked file of that name (the skills'
// **/project-context.md fallback).
func (lf *lifter) findProjectContext() string {
	if r := lf.rel(lf.layout.Cfg.Paths.ProjectContext); r != "" && lf.tracked(lf.ctx, r) {
		return r
	}
	out, _ := git(lf.ctx, lf.root, "ls-files", "--", "*project-context.md")
	for f := range strings.SplitSeq(out, "\n") {
		if path.Base(f) == "project-context.md" {
			return f
		}
	}
	return ""
}

// loadFiles reads every tracked text file the lift may edit: regular
// files, not deleted, not vendored, not changed by a framework install.
func (lf *lifter) loadFiles() error {
	out, err := gitRaw(lf.ctx, lf.root, "ls-files", "-z", "-s")
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "git ls-files: " + err.Error() + " — fix the repository, then lift"}
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		_, rel, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		mode := entry[:6]
		if mode != "100644" && mode != "100755" {
			continue
		}
		// config.yaml is rewritten whole; vendored code is not the
		// project's.
		if rel == configYAML || lf.isDeleted(rel) || lf.dirtyAt(rel) || strings.HasPrefix(rel, "vendor/") || strings.Contains(rel, "node_modules/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(lf.root, filepath.FromSlash(rel)))
		if err != nil || len(data) > liftMaxFileBytes || bytes.IndexByte(data[:min(len(data), liftBinarySniff)], 0) >= 0 {
			continue
		}
		lf.files = append(lf.files, rel)
		lf.texts[rel] = string(data)
	}
	sort.Strings(lf.files)
	return nil
}

// listKeptApex names what stays under _apex/ that is not v1's own.
func (lf *lifter) listKeptApex() {
	out, _ := git(lf.ctx, lf.root, "ls-files", "--", "_apex")
	known := map[string]bool{configYAML: true, frameworkYAML: true}
	for f := range strings.SplitSeq(out, "\n") {
		if f == "" || known[f] || lf.isDeleted(f) || strings.HasPrefix(f, "_apex/state/") {
			continue
		}
		lf.res.KeptApex = append(lf.res.KeptApex, f)
	}
}

// The v0 installer's markers around the CLAUDE.md block it owned
// (internal/framework's ManagedBlockBegin/End).
const (
	v0ManagedBegin = "<!-- apex:managed:begin -->"
	v0ManagedEnd   = "<!-- apex:managed:end -->"
)

// dropV0Imports removes CLAUDE.md's apex:managed block and any import of
// a file the lift deletes.
func (lf *lifter) dropV0Imports(doc string) string {
	lines := strings.SplitAfter(doc, "\n")
	out := make([]string, 0, len(lines))
	in := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == v0ManagedBegin:
			in = true
			continue
		case t == v0ManagedEnd:
			in = false
			continue
		case in:
			continue
		case strings.HasPrefix(t, "@") && lf.isDeleted(strings.TrimPrefix(t, "@")):
			continue
		}
		out = append(out, l)
	}
	res := strings.Join(out, "")
	for strings.Contains(res, "\n\n\n") {
		res = strings.ReplaceAll(res, "\n\n\n", "\n\n")
	}
	return strings.TrimLeft(res, "\n")
}

// v1Config is the minimal v1 _apex/config.yaml: the name, the
// development folder, and the regression command.
func (lf *lifter) v1Config() string {
	name := lf.layout.Cfg.ProjectName
	if name == "" {
		name = filepath.Base(lf.root)
	}
	out := "project_name: " + yamlScalar(name) + "\ndevelopment_folder: " + lf.devRel + "\n"
	if r := lf.res.Gates.Regression; r != "" {
		out += "regression: " + yamlString(r) + "\n"
	}
	return out + modelDefaultsYAML
}

// yamlString renders s as a YAML scalar, quoted only when it must be.
func yamlString(s string) string {
	data, err := yaml.Marshal(s)
	if err != nil {
		return yamlScalar(s)
	}
	return strings.TrimSuffix(string(data), "\n")
}

// dangling lists references to removed paths left in kept text files:
// CI guards and scripts that police the v0 corpus surface here.
func (lf *lifter) dangling() []LiftIDRef {
	tops := make([]string, 0, len(lf.deletes))
	for _, d := range lf.deletes {
		tops = append(tops, regexp.QuoteMeta(d))
	}
	if len(tops) == 0 {
		return nil
	}
	sort.Slice(tops, func(i, j int) bool { return len(tops[i]) > len(tops[j]) })
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_./-])((?:\./)?(?:` + strings.Join(tops, "|") + `)(?:/[^\s"'` + "`" + `)\]]*)?)`)
	var out []LiftIDRef
	for _, rel := range lf.files {
		src, _ := lf.current(rel)
		// framework.yaml is the install's record of what it wrote.
		if rel == frameworkYAML || !re.MatchString(src) {
			continue
		}
		for i, l := range strings.Split(src, "\n") {
			for _, m := range re.FindAllStringSubmatchIndex(l, -1) {
				if !atSegmentEnd(l, m[3]) {
					continue
				}
				out = append(out, LiftIDRef{File: rel, Line: i + 1, Text: strings.TrimRight(l[m[2]:m[3]], ".,:;"), Where: "reference"})
			}
		}
	}
	return out
}

// atSegmentEnd reports a removed path matched in s up to end as a whole
// path segment (R26): at the end, before a / or a byte no name has — a
// quote, a space, a ) — or before a sentence's closing period. radix's
// `mkdir -p development/planning-artifacts` names no development/planning.
func atSegmentEnd(s string, end int) bool {
	if end >= len(s) || s[end] == '/' || !isPathByte(s[end]) {
		return true
	}
	return s[end] == '.' && (end+1 >= len(s) || !isPathByte(s[end+1]))
}

// readings are the v0 documents apex-plan may read at the tag: the
// product briefs and the newest sprint-change proposal.
func (lf *lifter) readings() []string {
	planning := lf.rel(lf.layout.Cfg.Paths.Planning)
	if planning == "" {
		return nil
	}
	out, _ := git(lf.ctx, lf.root, "ls-files", "--", planning)
	var briefs, proposals []string
	for f := range strings.SplitSeq(out, "\n") {
		base := strings.ToLower(path.Base(f))
		switch {
		case strings.Contains(base, "brief") && strings.HasSuffix(base, ".md"):
			briefs = append(briefs, f)
		case strings.Contains(base, "sprint-change-proposal") && strings.HasSuffix(base, ".md"):
			proposals = append(proposals, f)
		}
	}
	var res []string
	for _, b := range briefs {
		res = append(res, "product brief: "+LiftTag+":"+b)
	}
	if len(proposals) > 0 {
		sort.Strings(proposals)
		res = append(res, "newest sprint-change proposal: "+LiftTag+":"+proposals[len(proposals)-1])
	}
	return res
}

// ---- apply ----------------------------------------------------------------

func (lf *lifter) apply() error {
	ctx := lf.ctx
	cleanup, err := lf.regressionAtTag()
	defer cleanup()
	if err != nil {
		return err
	}
	pathsFile, err := os.CreateTemp("", "ape-lift-paths-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(pathsFile.Name()) }()
	_ = pathsFile.Close()
	pathspec := func(paths []string) []string {
		_ = os.WriteFile(pathsFile.Name(), []byte(strings.Join(paths, "\x00")), 0o600)
		return []string{"--pathspec-from-file=" + pathsFile.Name(), "--pathspec-file-nul"}
	}
	rm := lf.removals()
	if len(rm) > 0 {
		if _, err := git(ctx, lf.root, append([]string{"rm", "-r", "-q", "--ignore-unmatch"}, pathspec(rm)...)...); err != nil {
			return &FailError{Code: CodeLiftWriteFailed, Msg: "removing the v0 families: " + err.Error() + " — `git reset --hard` restores the tree; fix the cause, then lift again"}
		}
	}
	commit := append([]string(nil), rm...)
	writeOrder := make([]string, 0, len(lf.writes))
	for rel := range lf.writes {
		writeOrder = append(writeOrder, rel)
	}
	sort.Strings(writeOrder)
	for _, rel := range writeOrder {
		if rel == lf.res.Disposition {
			continue
		}
		if err := liftWrite(filepath.Join(lf.root, filepath.FromSlash(rel)), lf.writes[rel]); err != nil {
			return &FailError{Code: CodeLiftWriteFailed, Msg: "writing " + rel + ": " + err.Error() + " — `git reset --hard` restores the tree; fix the cause, then lift again"}
		}
		commit = append(commit, rel)
	}

	// The v1 skeleton, as `ape project init --brownfield` lays it down,
	// committed with the rest.
	initRes, p, err := initSkeleton(ctx, lf.root, true)
	if err != nil {
		return err
	}
	lf.res.Init = initRes
	commit = append(commit, p.initCommitPaths(initRes)...)
	if doc, err := readFileString(p.AgentsMD()); err == nil {
		if out := withCommands(doc, lf.res.Gates); out != doc {
			if err := liftWrite(p.AgentsMD(), out); err != nil {
				return err
			}
		}
		commit = append(commit, "AGENTS.md")
	}
	if lf.movePin() {
		commit = append(commit, ".bingo")
	}
	if err := recordLiftedDecisions(p, lf.res.ADRs); err != nil {
		return err
	}
	if fileExists(p.decisionsJSON()) {
		commit = append(commit, p.Rel(p.decisionsJSON()))
	}
	if err := liftWrite(filepath.Join(lf.root, filepath.FromSlash(lf.res.Disposition)), lf.writes[lf.res.Disposition]); err != nil {
		return err
	}
	commit = append(commit, lf.res.Disposition)

	commit = uniqueStrings(commit)
	// git rm staged the deletions; what was written is staged here (a
	// removed folder no longer matches a pathspec for add).
	removed := map[string]bool{}
	for _, r := range rm {
		removed[r] = true
	}
	var add []string
	for _, c := range commit {
		if !removed[c] {
			add = append(add, c)
		}
	}
	if _, err := git(ctx, lf.root, append([]string{"add", "-A"}, pathspec(add)...)...); err != nil {
		return &FailError{Code: CodeCommitFailed, Msg: "staging the lift: " + err.Error() + " — `git reset --hard` restores the tree; fix the cause, then lift again"}
	}
	if err := ScanCommit(ctx, lf.root, "the lift commit", commit...); err != nil {
		if fe, ok := errors.AsType[*FailError](err); ok {
			fe.Msg += "; `git reset --hard` restores the tree, then lift again"
		}
		return err
	}
	if _, err := git(ctx, lf.root, append([]string{"commit", "--no-verify", "-q", "-m", lf.res.CommitMessage}, pathspec(commit)...)...); err != nil {
		return &FailError{Code: CodeCommitFailed, Msg: "the lift commit failed: " + err.Error() + " — `git reset --hard` restores the tree; fix the cause (identity, lock), then lift again"}
	}
	lf.res.Commit = HeadSHA(ctx, lf.root)
	// The full lists: under _apex/state/, which the state whitelist
	// ignores, so they are this checkout's and never history (R3).
	if err := liftWrite(filepath.Join(lf.root, filepath.FromSlash(lf.res.Report)), lf.reportJSON); err != nil {
		return &FailError{Code: CodeLiftWriteFailed, Msg: "writing " + lf.res.Report + ": " + err.Error() + " — the lift commit stands; " + lf.res.Disposition + " has the summaries"}
	}
	if err := lf.removeUntracked(); err != nil {
		return err
	}
	lf.liftRegression(p)
	return nil
}

// removals are the paths the lift removes: the deleted v0 families, the
// ADR folder file by file when some of its records are kept.
func (lf *lifter) removals() []string {
	var rm []string
	for _, d := range lf.deletes {
		if d == lf.rel(lf.layout.Cfg.Paths.ADRs) && len(lf.keepADR) > 0 {
			out, _ := git(lf.ctx, lf.root, "ls-files", "--", d)
			for f := range strings.SplitSeq(out, "\n") {
				if f != "" && !lf.keepADR[f] {
					rm = append(rm, f)
				}
			}
			continue
		}
		rm = append(rm, d)
	}
	return rm
}

// liftRegression runs the regression command at the lift commit and
// judges it against its run at the tag: the baseline red gates are
// judged against (backlog 37), and the one way to tell a red the lift
// caused from one it found (L4). A top-level && chain runs and is judged
// command by command (R12).
func (lf *lifter) liftRegression(p *Project) {
	switch {
	case lf.res.Gates.Regression == "":
		lf.res.RegressionNote = "no regression command was extracted: apex-plan sets `regression:` in _apex/config.yaml"
		return
	case lf.opt.NoVerifyRun:
		lf.res.RegressionNote = "not run (--no-verify-run)"
		return
	}
	parts := splitTopLevelAnd(lf.res.Gates.Regression)
	var after []CheckResult
	if len(parts) == 1 {
		after = []CheckResult{p.runRegression(lf.ctx, parts[0], p.LogDir("lift"), "", lf.opt.Run)}
	} else {
		checks := make([]PlannedCheck, len(parts))
		for i, c := range parts {
			checks[i] = PlannedCheck{Ref: liftPartRef(i), Kind: KindRegression, Cmd: c}
		}
		after = p.RunChecks(lf.ctx, p.LogDir("lift"), "", checks, lf.opt.Run)
	}
	for i := range lf.tagRuns {
		if i >= len(lf.tagLogs) || lf.tagLogs[i] == "" {
			continue
		}
		dst := filepath.Join(p.LogDir("lift"), filepath.Base(lf.tagLogs[i]))
		if err := os.Rename(lf.tagLogs[i], dst); err == nil {
			lf.tagRuns[i].Log = p.Rel(dst)
		} else if data, rerr := os.ReadFile(lf.tagLogs[i]); rerr == nil && os.WriteFile(dst, data, 0o600) == nil {
			lf.tagRuns[i].Log = p.Rel(dst)
		}
	}
	readLog := func(c *CheckResult) string {
		if c == nil || c.Log == "" {
			return ""
		}
		data, _ := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(c.Log)))
		return string(data)
	}
	cmds := make([]LiftRegressionCommand, len(parts))
	worst := 0
	for i := range parts {
		var tag *CheckResult
		if i < len(lf.tagRuns) {
			tag = &lf.tagRuns[i]
		}
		c := &cmds[i]
		c.Cmd, c.Regression, c.RegressionAtTag = parts[i], &after[i], tag
		c.Verdict, c.Note, c.NewFailures = judgeLiftRegression(tag, &after[i], readLog(tag), readLog(&after[i]))
		if liftVerdictRank(c.Verdict) > liftVerdictRank(cmds[worst].Verdict) {
			worst = i
		}
	}
	w := cmds[worst]
	lf.res.Regression, lf.res.RegressionAtTag = w.Regression, w.RegressionAtTag
	lf.res.RegressionVerdict, lf.res.RegressionNote, lf.res.RegressionNewFailures = w.Verdict, w.Note, w.NewFailures
	if len(parts) > 1 {
		lf.res.RegressionCommands = cmds
		lf.res.RegressionNote = splitNote(cmds, worst)
	}
}

// liftPartRef is the ref, and log name, of a split chain's i-th command.
func liftPartRef(i int) string { return regressionRef + "-" + strconv.Itoa(i+1) }

// The lift's verdicts in rank order, worst highest (liftVerdictRank).
const (
	liftRankGreen = iota
	liftRankCannotRun
	liftRankRedAtTag
	liftRankNoBaseline
	liftRankLiftCaused
)

// liftVerdictRank orders the lift's verdicts, worst highest: a red the
// lift caused, then a red nothing can attribute, a pre-existing red, a
// command that cannot run here, green.
func liftVerdictRank(v string) int {
	switch v {
	case LiftRegressionRedAfterLift, LiftRegressionRedAtTagNewFailures:
		return liftRankLiftCaused
	case LiftRegressionNoBaseline:
		return liftRankNoBaseline
	case LiftRegressionRedAtTag:
		return liftRankRedAtTag
	case LiftRegressionCannotRun:
		return liftRankCannotRun
	}
	return liftRankGreen
}

// splitNote is a split chain's note: the worst command's, naming it, and
// every other command's verdict.
func splitNote(cmds []LiftRegressionCommand, worst int) string {
	var others []string
	for i := range cmds {
		if i != worst {
			others = append(others, fmt.Sprintf("%d %#q %s", i+1, cmds[i].Cmd, cmds[i].Verdict))
		}
	}
	return fmt.Sprintf("command %d of %d, %#q (each command of the && chain ran on its own, past a red one; the worst verdict is this one's): %s — the others: %s; each in regression_commands",
		worst+1, len(cmds), cmds[worst].Cmd, cmds[worst].Note, strings.Join(others, ", "))
}

// removeUntracked removes the manifest's v0 skills git never tracked (an
// ignored .claude/, L2): installer-owned, so they go too, once the commit
// stands.
func (lf *lifter) removeUntracked() error {
	for _, s := range lf.untracked {
		if err := os.RemoveAll(filepath.Join(lf.root, filepath.FromSlash(s))); err != nil {
			return &FailError{Code: CodeLiftWriteFailed, Msg: "removing the untracked v0 skill " + s + ": " + err.Error() + " — the lift commit stands; remove the folder by hand"}
		}
	}
	return nil
}

// regressionAtTag runs the regression command at the tag, first, before
// anything is written: HEAD is the tag (pre-flight) and the tree is
// clean. A top-level && chain runs command by command, past a red one
// (R12). Its logs stay outside the tree until the lift's state folder
// exists; cleanup removes them. No command, or --no-verify-run: nothing.
func (lf *lifter) regressionAtTag() (cleanup func(), err error) {
	cleanup = func() {}
	if lf.res.Gates.Regression == "" || lf.opt.NoVerifyRun {
		return cleanup, nil
	}
	tmp, err := os.MkdirTemp("", "ape-lift-tag-*")
	if err != nil {
		return cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	p := &Project{Root: lf.root, DevRel: lf.devRel}
	parts := splitTopLevelAnd(lf.res.Gates.Regression)
	inGit := p.isWorkTree(lf.ctx)
	lf.tagRuns = make([]CheckResult, 0, len(parts))
	for i, c := range parts {
		ref, name := regressionRef, "regression-at-tag.log"
		if len(parts) > 1 {
			ref = liftPartRef(i)
			name = ref + "-at-tag.log"
		}
		logPath := filepath.Join(tmp, name)
		progressf(lf.opt.Run.Progress, "%s %s at %s started: %s", Stamp(), ref, LiftTag, c)
		r := p.runWithRetries(lf.ctx, &PlannedCheck{Ref: ref, Kind: KindRegression, Cmd: c}, logPath,
			Timeouts{}.Limits(KindRegression, lf.opt.Run.Timeout), nil, 0, inGit)
		r.Task, r.Kind = ref, KindRegression
		lf.tagRuns = append(lf.tagRuns, r)
		lf.tagLogs = append(lf.tagLogs, logPath)
		lf.res.RegressionAtTag = &lf.tagRuns[0]
		if lf.ctx.Err() != nil {
			return cleanup, &FailError{Code: CodeRunInterrupted, Msg: "interrupted while the regression ran at " + LiftTag + " — nothing was written; lift again"}
		}
	}
	return cleanup, nil
}

// movePin moves the project's ape pin to the running version (L1), and
// reports whether .bingo/ goes in the lift commit. When bingo fails, .bingo/
// goes back to the tag's and the note names the command.
func (lf *lifter) movePin() bool {
	a := lf.res.Ape
	if a == nil || a.MovePin == nil {
		return false
	}
	to := "v" + strings.TrimPrefix(a.Version, "v")
	if err := a.MovePin(lf.ctx); err != nil {
		// bingo may have half-written .bingo/: back to the tag's.
		_, _ = git(lf.ctx, lf.root, "checkout", "--", ".bingo")
		a.PinNote = "could not move the pin (" + err.Error() + "): after the lift, run " + a.PinCommand +
			" and commit .bingo/; until then a bare ape here runs " + a.Pinned + ", so run v1 commands with APE_TOOLCHAIN=local"
		return false
	}
	a.PinMoved = true
	a.PinNote = "the lift commit moves " + a.PinFile + " from " + a.Pinned + " to " + to
	lf.res.CommitMessage = strings.Replace(lf.res.CommitMessage, "- lays down the v1 skeleton",
		"- moves the ape pin ("+a.PinFile+") from "+a.Pinned+" to "+to+"\n- lays down the v1 skeleton", 1)
	return true
}

// judgeLiftRegression compares the regression command at the lift commit
// (after) with its run at the tag (before, nil when it did not run);
// beforeOut and afterOut are their outputs, read when a run is red.
func judgeLiftRegression(before, after *CheckResult, beforeOut, afterOut string) (verdict, note string, fresh []string) {
	tag := "not run"
	if before != nil {
		tag = fmt.Sprintf("exit %d", before.Exit)
	}
	afterCannot, why := liftCannotRun(after, afterOut)
	beforeCannot, _ := liftCannotRun(before, beforeOut)
	switch {
	case after.Exit == 0:
		if before != nil && before.Exit != 0 {
			return LiftRegressionGreen, fmt.Sprintf("green at the lift commit (red at %s, %s: check that run's log for a flaky test)", LiftTag, tag), nil
		}
		return LiftRegressionGreen, "green at the lift commit, as at " + LiftTag, nil
	case afterCannot && (before == nil || before.Exit != 0):
		return LiftRegressionCannotRun, fmt.Sprintf("cannot run here (exit %d at the lift commit%s, %s at %s): a command it runs is missing or not executable on this machine — not a red; run it where its tools are, or set `regression:` to what can run; output in %s", after.Exit, why, tag, LiftTag, after.Log), nil
	case before == nil:
		return LiftRegressionNoBaseline, fmt.Sprintf("red at the lift commit (exit %d), with no run at %s to compare: output in %s", after.Exit, LiftTag, after.Log), nil
	case beforeCannot:
		return LiftRegressionNoBaseline, fmt.Sprintf("red at the lift commit (exit %d); at %s it could not run (%s), so nothing says whose red it is: output in %s", after.Exit, LiftTag, tag, after.Log), nil
	case before.Exit != 0:
		return judgeRedAtTag(before, after, tag, beforeOut, afterOut)
	default:
		return LiftRegressionRedAfterLift, fmt.Sprintf("LIFT FAILURE: red at the lift commit (exit %d), green at %s — the lift broke it (an edited file a test or CI pins, a removed path code still reads); output in %s. Fix it in a commit of its own, or `git reset --hard %s` and lift again with --exclude", after.Exit, LiftTag, after.Log, LiftTag), nil
	}
}

// judgeRedAtTag judges a run red at the tag and at the lift commit (R4):
// by the failing test names when the run after the lift reports them,
// else by the outputs' hashes, tool-install lines and durations left out
// (R13). Only the same failures (or fewer), or the same output, are
// called pre-existing.
func judgeRedAtTag(before, after *CheckResult, tag, beforeOut, afterOut string) (verdict, note string, fresh []string) {
	newNames, named := newFailures(beforeOut, afterOut)
	switch {
	case len(newNames) > 0:
		shown := newNames[:min(len(newNames), liftNewFailuresShown)]
		list := strings.Join(shown[:min(len(shown), 5)], ", ")
		if len(newNames) > 5 {
			list += fmt.Sprintf(", … (%d in all, in regression_new_failures)", len(newNames))
		}
		return LiftRegressionRedAtTagNewFailures, fmt.Sprintf("LIFT FAILURE: red at the lift commit (exit %d) and at %s (%s), but %d failing test(s) the run at %s did not have: %s — the lift broke them (code reading a removed path, an edited file a test pins). Fix them in a commit of their own; the rest are pre-existing. Output in %s and %s",
			after.Exit, LiftTag, tag, len(newNames), LiftTag, list, after.Log, before.Log), shown
	case named || before.OutputSHA256 == after.OutputSHA256 ||
		(beforeOut+afterOut != "" && liftComparisonHash(beforeOut) == liftComparisonHash(afterOut)):
		return LiftRegressionRedAtTag, fmt.Sprintf("red at the lift commit (exit %d) and at %s (%s), failing as it did there: a pre-existing failure for apex-plan to roadmap, not a lift failure; output in %s and %s", after.Exit, LiftTag, tag, after.Log, before.Log), nil
	default:
		return LiftRegressionRedAtTag, fmt.Sprintf("red at the tag; the output changed after the lift (exit %d at the lift commit, %s at %s), and no failing test names could be read to compare, so nothing says the red is only the tag's: diff %s with %s before calling it the tag's", after.Exit, tag, LiftTag, after.Log, before.Log), nil
	}
}

// liftWrite writes a file, keeping an existing file's mode (a script
// stays executable).
func liftWrite(file, content string) error {
	st, statErr := os.Stat(file)
	if err := writeFileAtomic(file, content); err != nil {
		return err
	}
	if statErr == nil {
		return os.Chmod(file, st.Mode().Perm())
	}
	return nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSuffix(s, "/")
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// commitMessage is the lift commit's message: the subject other code
// finds the v1 epoch by, a summary, and the old → new ADR table.
func (lf *lifter) commitMessage() string {
	r := lf.res
	var b strings.Builder
	fmt.Fprintf(&b, "%s from APEX v0 %s\n\n", LiftSubjectPrefix, r.Version)
	fmt.Fprintf(&b, "The v0 state is archived at %s (%s). This commit:\n", LiftTag, short(r.TagSHA))
	fams := map[string]int{}
	var order []string
	for _, d := range r.Deleted {
		if fams[d.Family] == 0 {
			order = append(order, d.Family)
		}
		fams[d.Family]++
	}
	for _, f := range order {
		fmt.Fprintf(&b, "- deletes %s (%d path(s))\n", f, fams[f])
	}
	fmt.Fprintf(&b, "- converts %d ADR(s) into %s/decisions/, renumbered from ADR-0001\n", len(r.ADRs), lf.devRel)
	stripped, cited := 0, 0
	for _, c := range r.Changed {
		stripped += c.IDsRemoved
		cited += c.ADRsRewritten
	}
	fmt.Fprintf(&b, "- rewrites %d ADR citation(s) and strips v0 ids from %d comment(s), in %d file(s)\n", cited, stripped, len(r.Changed))
	if r.Gates.Regression != "" {
		fmt.Fprintf(&b, "- carries %d gate command(s) from %s into AGENTS.md and regression:\n", len(r.Gates.Commands), r.Gates.Source)
	}
	b.WriteString("- lays down the v1 skeleton (ape project init --brownfield)\n")
	fmt.Fprintf(&b, "- writes %s for apex-plan and the person (its full lists in %s, not committed)\n", r.Disposition, r.Report)
	if len(r.ADRs) > 0 {
		b.WriteString("\nADRs, old -> new:\n")
		for i := range r.ADRs {
			a := &r.ADRs[i]
			from := ""
			if a.Canon {
				from = " (canon)"
			}
			fmt.Fprintf(&b, "  %s -> %s%s  %s\n", a.Old, a.New, from, path.Base(a.Path))
		}
	}
	return b.String()
}
