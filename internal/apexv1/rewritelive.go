package apexv1

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// v1.4 (D526, B8): the check kind `(live)` is `(full)`'s v1.2 name. v1.3
// accepted it silently; v1.4 warns (check.live-deprecated) and gives the
// rewrite a framework migration runs, `ape project rewrite-live`, in one
// commit a person reviews. It is removed no earlier than v1.5.
//
// What the rewrite touches, and what it leaves:
//
//   - the specs (development/specs/**/spec.md): every `check (live):`;
//   - a draft change's change.md: its delta's `check (live):` lines and its
//     tasks' `— check (live):`. A started change's plan is frozen at start
//     (change.plan-drift), so it is left as it is: its delta reaches the
//     specs as `(full)` when it is archived (MergeDelta);
//   - the open gate folders (development/gates/**): a `# scenario (live):`
//     comment line. When the file is one an approval recorded and it is as
//     approved, the approval's SHA-256 is recorded again in the same
//     commit, so the rewrite is no gate drift (milestone.gate-changed);
//   - nothing archived: the archive is history.
//
// Inside fenced code blocks of a markdown file nothing is rewritten: an
// example is not a check.

// Codes of the rewrite.
const (
	CodeLiveDeprecated = "check.live-deprecated"
	CodeLiveLeft       = "project.live-left"
	CodeReadFailed     = "project.read-failed"
)

// namedFiles is how many files a notice names.
const namedFiles = 4

// RewriteLiveSubject is the rewrite's commit subject. Provenance exempts a
// commit with it from spec.edited only when its diff is the rewrite alone.
const RewriteLiveSubject = "apex: rewrite (live) as (full)"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeLiveDeprecated, 0, "person", "a notice, never a finding (project check's and change check's notices; the exit is unchanged): a check line still uses `(live)`, `(full)`'s v1.2 name, deprecated since v1.4 and removed no earlier than v1.5 (D526) — in a spec, a draft change, an open gate's `# scenario (live):` comment, or a started change; `ape project rewrite-live` rewrites all but the started changes in one commit, and those reach the specs as `(full)` when they are archived"},
		CodeDoc{CodeReadFailed, 2, "stop", "`ape project rewrite-live` (and its --check): a spec, change or gate file could not be read — the check itself failed (exit 2, never read as \"not applied\"); fix the file's permissions and run it again"},
		CodeDoc{CodeLiveLeft, 1, "person", "`ape project rewrite-live --check` (the framework migration's check: 0 applied, 1 not applied): a spec, a draft change or an open gate still uses `(live)`; `ape project rewrite-live` rewrites them (a started change's are not counted: its archive writes `(full)`)"},
	)
}

var (
	// liveCheckLineRe is a scenario's check line, `- check (live): …`.
	liveCheckLineRe = regexp.MustCompile(`^(\s*(?:[-*]\s+)?check\s*\()\s*live\s*(\)\s*:)`)
	// liveTaskRe is a task line's check, `— check (live): …` (or a
	// regression's).
	liveTaskRe = regexp.MustCompile(`((?:^|\s)[—–-]+\s*(?:check|regression)\s*\()\s*live\s*(\)\s*:)`)
	// liveGateRe is a gate script's scenario comment, `# scenario (live): …`.
	liveGateRe = regexp.MustCompile(`^(\s*#+\s*scenario\s*\()\s*live\s*(\)\s*:)`)
	fenceRe    = regexp.MustCompile("^\\s*(```|~~~)")
)

// rewriteLiveMarkdown is doc, a spec or a change.md, with each `(live)`
// check tag written `(full)` outside fenced code, and how many it wrote.
func rewriteLiveMarkdown(doc string) (out string, n int) {
	lines := strings.SplitAfter(doc, "\n")
	fenced := false
	for i, l := range lines {
		if fenceRe.MatchString(l) {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, re := range []*regexp.Regexp{liveCheckLineRe, liveTaskRe} {
			if re.MatchString(l) {
				l = re.ReplaceAllString(l, "${1}full${2}")
				lines[i] = l
				n++
				break
			}
		}
	}
	return strings.Join(lines, ""), n
}

// rewriteLiveGate is doc, a gate folder's file, with each `# scenario
// (live):` comment written `(full)`, and how many it wrote.
func rewriteLiveGate(doc string) (out string, n int) {
	lines := strings.SplitAfter(doc, "\n")
	for i, l := range lines {
		if liveGateRe.MatchString(l) {
			lines[i] = liveGateRe.ReplaceAllString(l, "${1}full${2}")
			n++
		}
	}
	return strings.Join(lines, ""), n
}

// RewriteLiveFile is one file the rewrite writes, or leaves: its
// project-relative path, what it is (spec, change, gate), how many tags,
// and for one left, why.
type RewriteLiveFile struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Why   string `json:"why,omitempty"`

	content string
}

// RewriteLiveResult is what `ape project rewrite-live` reports.
type RewriteLiveResult struct {
	// Rewritten are the files written (under --check: the files to write).
	Rewritten []RewriteLiveFile `json:"rewritten"`
	// Left are the started changes whose tags stay until their archive.
	Left []RewriteLiveFile `json:"left"`
	// Approvals are the approvals whose gate files were recorded again.
	Approvals []string `json:"approvals,omitempty"`
	Commit    string   `json:"commit,omitempty"`
}

// liveTargets are the files holding `(live)` tags: those the rewrite
// writes (with their new content) and the started changes it leaves.
func (p *Project) liveTargets() (rewrite, left []RewriteLiveFile, err error) {
	add := func(abs, kind string, rw func(string) (string, int)) error {
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		out, n := rw(string(data))
		if n > 0 {
			rewrite = append(rewrite, RewriteLiveFile{Path: p.Rel(abs), Kind: kind, Count: n, content: out})
		}
		return nil
	}
	walk := func(dir string, visit func(path string) error) error {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				return visit(path)
			}
			return nil
		})
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := walk(p.SpecsDir(), func(path string) error {
		if filepath.Base(path) != "spec.md" {
			return nil
		}
		return add(path, "spec", rewriteLiveMarkdown)
	}); err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(p.ChangesDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		data, err := os.ReadFile(p.ChangeMD(id))
		if err != nil {
			continue
		}
		out, n := rewriteLiveMarkdown(string(data))
		if n == 0 {
			continue
		}
		f := RewriteLiveFile{Path: p.Rel(p.ChangeMD(id)), Kind: "change", Count: n, content: out}
		if st, err := LoadState(p.StateFile(id)); err == nil && st.Status != StatusDraft {
			f.Why, f.content = "change "+id+" is "+st.Status+": its plan is frozen at start, and its archive writes (full)", ""
			left = append(left, f)
			continue
		}
		rewrite = append(rewrite, f)
	}
	seen := map[string]bool{}
	gate := func(path string) error {
		st, err := os.Stat(path)
		if err != nil {
			return nil //nolint:nilerr // a file gone since the walk is no gate file to rewrite
		}
		if !st.Mode().IsRegular() || st.Size() > 1<<20 || seen[path] {
			return nil
		}
		seen[path] = true
		return add(path, "gate", rewriteLiveGate)
	}
	if err := walk(p.GatesDir(), gate); err != nil {
		return nil, nil, err
	}
	// An approved milestone's recorded gate files outside <dev>/gates/
	// (a gate approved before rc.6 may run scripts/…, Q73).
	files, _ := p.gateFrozen(MilestoneNone)
	for f := range files {
		_ = gate(filepath.Join(p.Root, filepath.FromSlash(f)))
	}
	sort.Slice(rewrite, func(i, j int) bool { return rewrite[i].Path < rewrite[j].Path })
	return rewrite, left, nil
}

// liveNotices are project check's check.live-deprecated notices: one, when
// any file still uses (live).
func (p *Project) liveNotices() []Diagnostic {
	rewrite, left, err := p.liveTargets()
	if err != nil || len(rewrite)+len(left) == 0 {
		return nil
	}
	n := 0
	var where []string
	for _, f := range append(append([]RewriteLiveFile(nil), rewrite...), left...) {
		n += f.Count
		where = append(where, f.Path)
	}
	fix := "`ape project rewrite-live` rewrites them in one commit"
	if len(left) > 0 {
		fix += " (a started change's stay: its archive writes `(full)`)"
	}
	return []Diagnostic{notice(Diagnostic{Code: CodeLiveDeprecated, Message: strconv.Itoa(n) + " check line(s) use `(live)` (" + strings.Join(limit(where, namedFiles), ", ") +
		"): `(live)` is `(full)`'s v1.2 name, deprecated since v1.4 and removed no earlier than v1.5 — " + fix})}
}

// changeLiveNotices are change check's check.live-deprecated notice for
// change id's own change.md.
func (p *Project) changeLiveNotices(id string) []Diagnostic {
	data, err := os.ReadFile(p.ChangeMD(id))
	if err != nil {
		return nil
	}
	if _, n := rewriteLiveMarkdown(string(data)); n > 0 {
		return []Diagnostic{notice(Diagnostic{Code: CodeLiveDeprecated, File: p.Rel(p.ChangeMD(id)), Message: strconv.Itoa(n) +
			" check line(s) use `(live)`, deprecated since v1.4 and removed no earlier than v1.5 — write `(full)` (`ape project rewrite-live` rewrites a draft's)"})}
	}
	return nil
}

// RewriteLiveCheck is `ape project rewrite-live --check`: what the rewrite
// would write, and project.live-left when it would write anything.
func (p *Project) RewriteLiveCheck() (*RewriteLiveResult, error) {
	rewrite, left, err := p.liveTargets()
	res := &RewriteLiveResult{Rewritten: orEmpty(rewrite), Left: orEmpty(left)}
	if err != nil {
		return res, &FailError{Code: CodeReadFailed, Msg: "cannot read the project's specs, changes or gates: " + err.Error()}
	}
	if len(rewrite) == 0 {
		return res, nil
	}
	n := 0
	var where []string
	for _, f := range rewrite {
		n += f.Count
		where = append(where, f.Path)
	}
	return res, &FailError{Code: CodeLiveLeft, Msg: strconv.Itoa(n) + " `(live)` tag(s) left in " + strings.Join(limit(where, 8), ", ") +
		" — `ape project rewrite-live` rewrites them as `(full)` in one commit"}
}

func orEmpty(files []RewriteLiveFile) []RewriteLiveFile {
	if files == nil {
		return []RewriteLiveFile{}
	}
	return files
}

// RewriteLive is `ape project rewrite-live`: every `(live)` tag of the
// specs, the draft changes and the open gates written `(full)`, the
// approvals of the gate files it rewrote recorded again, in one commit.
// Refused on a change branch and with uncommitted changes in a file it
// would write.
func (p *Project) RewriteLive(ctx context.Context) (*RewriteLiveResult, error) {
	res := &RewriteLiveResult{Rewritten: []RewriteLiveFile{}, Left: []RewriteLiveFile{}}
	if !p.isWorkTree(ctx) {
		return res, &FailError{Code: "git.not-a-repo", Msg: "not a git repository — the rewrite is one commit"}
	}
	if err := p.refuseOnChangeBranch(ctx, "the (live) rewrite"); err != nil {
		return res, err
	}
	rewrite, left, err := p.liveTargets()
	res.Left = orEmpty(left)
	if err != nil {
		return res, &FailError{Code: CodeReadFailed, Msg: "cannot read the project's specs, changes or gates: " + err.Error()}
	}
	if len(rewrite) == 0 {
		return res, nil
	}
	paths := make([]string, len(rewrite))
	for i, f := range rewrite {
		paths[i] = f.Path
	}
	if dirty, err := gitRaw(ctx, p.Root, append([]string{"status", "--porcelain=v1", "--"}, paths...)...); err != nil {
		return res, &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	} else if strings.TrimSpace(dirty) != "" {
		return res, &FailError{Code: "tree.dirty", Msg: "the rewrite is one commit, and a file it would write has uncommitted changes (" +
			strings.Join(limit(strings.Fields(dirty), 8), " ") + ") — commit or discard them first, then run it again"}
	}
	before := map[string]string{}
	for _, f := range rewrite {
		abs := filepath.Join(p.Root, filepath.FromSlash(f.Path))
		if sum, ok := p.fileSHA256(f.Path); ok {
			before[f.Path] = sum
		}
		if err := writeFileAtomic(abs, f.content); err != nil {
			p.undoRewrite(ctx, paths)
			return res, &FailError{Code: CodeCommitFailed, Msg: "cannot write " + f.Path + ": " + err.Error() + " — nothing was committed and the files are as they were"}
		}
	}
	commitPaths := append([]string(nil), paths...)
	if approvals, err := p.rerecordRewrittenGates(before); err != nil {
		p.undoRewrite(ctx, paths)
		return res, &FailError{Code: CodeCommitFailed, Msg: "cannot record the approvals again: " + err.Error() + " — nothing was committed and the files are as they were"}
	} else if len(approvals) > 0 {
		res.Approvals = approvals
		commitPaths = append(commitPaths, p.Rel(p.MilestonesJSON()))
	}
	n := 0
	for _, f := range rewrite {
		n += f.Count
	}
	sha, err := p.commitRecord(ctx, RewriteLiveSubject, strconv.Itoa(n)+" `(live)` tag(s) written `(full)` in "+strconv.Itoa(len(rewrite))+
		" file(s) (D526): nothing else in them changed.", commitPaths...)
	if err != nil {
		p.undoRewrite(ctx, commitPaths)
		return res, &FailError{Code: CodeCommitFailed, Msg: "the rewrite's commit failed: " + err.Error() + " — nothing was committed and the files are as they were"}
	}
	res.Rewritten, res.Commit = rewrite, sha
	return res, nil
}

// undoRewrite puts paths back as HEAD has them.
func (p *Project) undoRewrite(ctx context.Context, paths []string) {
	_, _ = git(ctx, p.Root, append([]string{"reset", "-q", "--"}, paths...)...)
	_, _ = git(ctx, p.Root, append([]string{"checkout", "-q", "--"}, paths...)...)
}

// rerecordRewrittenGates records again, in each approved milestone's
// approval, the SHA-256 of a gate file the rewrite changed, when the file
// was as approved (before holds its SHA-256 before the rewrite); a file
// that had drifted already keeps its record, so its drift stays found.
// It returns the milestones whose approval changed.
func (p *Project) rerecordRewrittenGates(before map[string]string) ([]string, error) {
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return nil, err
	}
	var changed []string
	for id, rec := range recs {
		if rec == nil || rec.Approval == nil {
			continue
		}
		a, touched := rec.Approval, false
		for _, set := range []map[string]string{a.GateFiles, a.GateOwnFiles} {
			for f, sum := range set {
				old, ok := before[f]
				if !ok || old != sum {
					continue
				}
				if now, ok := p.fileSHA256(f); ok && now != sum {
					set[f], touched = now, true
				}
			}
		}
		if touched {
			a.GateFilesSHA256 = gateFilesDigest(a.GateFiles)
			changed = append(changed, id)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	sort.Strings(changed)
	return changed, p.saveMilestoneRecords(recs)
}

// liveRewriteCommit reports ape's (live) rewrite commit: its subject, and
// a diff that is the rewrite alone.
func (p *Project) liveRewriteCommit(ctx context.Context, subject, sha string) bool {
	return subject == RewriteLiveSubject && p.pureLiveRewrite(ctx, sha)
}

// pureLiveRewrite reports whether commit sha's diff to its parent is the
// (live) rewrite and nothing else: every removed line, rewritten, is the
// added line beside it.
func (p *Project) pureLiveRewrite(ctx context.Context, sha string) bool {
	out, err := gitRaw(ctx, p.Root, "show", "--format=", "--no-color", "--no-renames", "-U0", sha)
	if err != nil {
		return false
	}
	var minus, plus []string
	for l := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(l, "---"), strings.HasPrefix(l, "+++"):
		case strings.HasPrefix(l, "-"):
			minus = append(minus, l[1:])
		case strings.HasPrefix(l, "+"):
			plus = append(plus, l[1:])
		}
	}
	if len(minus) == 0 || len(minus) != len(plus) {
		return false
	}
	for i := range minus {
		if !strings.Contains(minus[i], "live") {
			// milestones.json: an approval's recorded SHA-256 (the
			// rewrite records the gate files again) — a hex digest only.
			if !shaLineChange(minus[i], plus[i]) {
				return false
			}
			continue
		}
		md, _ := rewriteLiveMarkdown(minus[i])
		gt, _ := rewriteLiveGate(minus[i])
		if md != plus[i] && gt != plus[i] {
			return false
		}
	}
	return true
}

// shaHexRe is a SHA-256 hex digest.
var shaHexRe = regexp.MustCompile(`[0-9a-f]{64}`)

// shaLineChange reports two lines that differ only in their SHA-256 hex
// digests.
func shaLineChange(a, b string) bool {
	return a != b && shaHexRe.ReplaceAllString(a, "") == shaHexRe.ReplaceAllString(b, "")
}
