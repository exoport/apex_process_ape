package apexv1

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The assessor's batch folder (PLAN-V15-ASSESSOR step 3; research's
// review, D1 and R1). The coordinator builds it in its own state dir, and
// the assessor session (step 4) reads it and writes notice.json there,
// nothing else: so everything the one question needs is in it, and nothing
// that could reach past it.
//
//   - The files are a `git archive` of the trunk remote's main: no .git, no
//     hot/ (no credential), and no symlink — a committed `x -> ~/.ssh/…`
//     would let a Read leave the folder, so links are dropped at extraction
//     (R1), as are hard links and anything not a regular file or directory.
//   - input.md holds the batch's events, every open milestone's plan (its
//     roadmap section, gate paths, its changes' touches: and paths:) and
//     each archived change's diff.
//   - A diff comes only from a commit on the project's milestone/<M> (D1):
//     an event names its commit, but an event is anyone's to publish, so
//     a commit that is not on milestone/<M> is never read, and the input
//     says so.
const (
	// AssessInput and AssessNotice are the folder's two files of its own.
	AssessInput  = "input.md"
	AssessNotice = "notice.json"
	// AssessInputCap bounds input.md; AssessDiffCap one change's diff.
	// Past either the text is cut, and says so.
	AssessInputCap = 256 << 10
	AssessDiffCap  = 64 << 10
)

// AssessEvent is one trigger of the batch, as the coordinator received
// it: its message id and the payload's fields (apexv1.Trigger).
type AssessEvent struct {
	MsgID     string   `json:"msg_id"`
	Event     string   `json:"event"`
	Milestone string   `json:"milestone"`
	Change    string   `json:"change,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Gate      string   `json:"gate,omitempty"`
}

// AssessRequest is one batch of one project.
type AssessRequest struct {
	Batch   string
	Project string
	Events  []AssessEvent
	// InputCap and DiffCap override the caps (tests); 0 is the default.
	InputCap, DiffCap int
}

// AssessFolder is what BuildAssessFolder made.
type AssessFolder struct {
	Dir string `json:"dir"`
	// Main is the trunk's commit the snapshot is.
	Main string `json:"main"`
	// Open are the open milestones input.md lists.
	Open []string `json:"open"`
	// Dropped are the snapshot's entries not extracted: links, other
	// non-regular files, hot/, and a file of the folder's own name.
	Dropped []string `json:"dropped,omitempty"`
	// Diffs maps each change-archived event's change to the commit its
	// diff was read from, "" when none was (DiffNotes says why).
	Diffs     map[string]string `json:"diffs,omitempty"`
	DiffNotes map[string]string `json:"diff_notes,omitempty"`
	// Truncated: input.md, or a diff in it, was cut to its cap.
	Truncated bool `json:"truncated,omitempty"`
}

var (
	assessMilestoneRe = regexp.MustCompile(`^M\d+$`)
	assessCommitRe    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// BuildAssessFolder makes dir, which must not exist, the batch folder of
// req: the snapshot of the trunk remote's main, fetched first, and
// input.md. p is the coordinator's checkout of the project; nothing in its
// working tree changes, only its remote-tracking refs. A failed fetch of
// main is an error when no main was ever fetched; a milestone branch that
// cannot be fetched is read as last fetched, or not at all, and the input
// says which.
func (p *Project) BuildAssessFolder(ctx context.Context, dir string, req AssessRequest) (*AssessFolder, error) {
	b := &assessBuild{p: p, req: req, remote: p.trunkRemote(ctx), inCap: req.InputCap, diffCap: req.DiffCap}
	if b.inCap <= 0 {
		b.inCap = AssessInputCap
	}
	if b.diffCap <= 0 {
		b.diffCap = AssessDiffCap
	}
	if err := b.snapshot(ctx, dir); err != nil {
		return nil, err
	}
	b.fetchMilestones(ctx)
	b.readDiffs(ctx)

	w := &b.w
	fmt.Fprintf(w, "# Assessor batch %s, project %s\n\n", req.Batch, req.Project)
	fmt.Fprintf(w, "This folder is the project's `main` at %s, files only: no `.git`, no `hot/`, and no symbolic link (dropped at extraction).", b.out.Main)
	if len(b.out.Dropped) > 0 {
		fmt.Fprintf(w, " Not extracted: %s.", strings.Join(limit(b.out.Dropped, 20), ", "))
	}
	w.WriteString(" Paths below are relative to this folder.\n\n")
	for _, n := range b.notes {
		fmt.Fprintf(w, "- Note: %s\n", n)
	}
	if len(b.notes) > 0 {
		w.WriteString("\n")
	}
	b.writeEvents()
	b.writeMilestones(ctx)
	if len(b.diffs) > 0 {
		w.WriteString("## Diffs\n\n")
	}
	for _, d := range b.diffs {
		fmt.Fprintf(w, "### %s (%s on `%s`)\n\n%s\n\n", d.change, short(d.sha), MilestoneBranch(d.ms), fenced("diff", d.text))
	}

	text := w.String()
	if len(text) > b.inCap {
		text = text[:b.inCap] + fmt.Sprintf("\n\n[cut: input.md is %d bytes; the first %d are shown]\n", len(text), b.inCap)
		b.out.Truncated = true
	}
	if err := os.WriteFile(filepath.Join(dir, AssessInput), []byte(text), 0o600); err != nil {
		return nil, err
	}
	return b.out, nil
}

// assessBuild is one BuildAssessFolder.
type assessBuild struct {
	p              *Project
	req            AssessRequest
	remote         string
	inCap, diffCap int
	out            *AssessFolder
	snap           *Project
	rm             *Roadmap
	recs           MilestoneRecords
	notes          []string
	// withEvents are the milestones the batch has events of; have, those
	// whose milestone branch the remote has.
	withEvents, have map[string]bool
	diffs            []assessDiff
	w                strings.Builder
}

type assessDiff struct{ change, ms, sha, text string }

func (b *assessBuild) ref(branch string) string { return "refs/remotes/" + b.remote + "/" + branch }

// snapshot fetches main and extracts it into dir (R1), and reads its
// roadmap and milestone records.
func (b *assessBuild) snapshot(ctx context.Context, dir string) error {
	p := b.p
	if err := p.fetchCoordinated(ctx, "main"); err != nil {
		b.notes = append(b.notes, fmt.Sprintf("fetching main from %s failed (%v): this is main as last fetched", b.remote, err))
	}
	mainSHA, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", b.ref("main")+"^{commit}")
	if err != nil || mainSHA == "" {
		return &FailError{Code: CodeFetchFailed, Msg: "the assessor's batch needs main from " + b.remote + ", and this checkout has none fetched"}
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	b.out = &AssessFolder{Dir: dir, Main: mainSHA, Diffs: map[string]string{}, DiffNotes: map[string]string{}}
	tarball, err := gitRaw(ctx, p.Root, "archive", "--format=tar", mainSHA)
	if err != nil {
		return fmt.Errorf("git archive of main %s: %w", short(mainSHA), err)
	}
	if b.out.Dropped, err = extractSnapshot(dir, []byte(tarball)); err != nil {
		return err
	}
	if b.snap, err = Open(dir); err != nil {
		return err
	}
	b.snap.live = dir
	b.rm = &Roadmap{}
	if doc, err := os.ReadFile(b.snap.RoadmapMD()); err == nil {
		b.rm = ParseRoadmap(b.snap.Rel(b.snap.RoadmapMD()), string(doc))
	}
	b.recs, _ = b.snap.LoadMilestoneRecords()
	return nil
}

// fetchMilestones fetches the branch of every open milestone and of every
// milestone the batch has events of.
func (b *assessBuild) fetchMilestones(ctx context.Context) {
	b.withEvents, b.have = map[string]bool{}, map[string]bool{}
	branches := map[string]bool{}
	for _, e := range b.req.Events {
		if assessMilestoneRe.MatchString(e.Milestone) {
			b.withEvents[e.Milestone] = true
			branches[e.Milestone] = true
		}
	}
	for i := range b.rm.Milestones {
		if m := &b.rm.Milestones[i]; m.Status == MilestoneApproved {
			b.out.Open = append(b.out.Open, m.ID)
			branches[m.ID] = true
		}
	}
	for _, ms := range slices.Sorted(maps.Keys(branches)) {
		br := MilestoneBranch(ms)
		if err := b.p.fetchCoordinated(ctx, br); err != nil {
			b.notes = append(b.notes, fmt.Sprintf("fetching %s from %s failed: it is read as last fetched, if ever", br, b.remote))
		}
		if _, err := git(ctx, b.p.Root, "rev-parse", "--verify", "--quiet", b.ref(br)+"^{commit}"); err == nil {
			b.have[ms] = true
		}
	}
}

// readDiffs reads each change-archived event's diff from milestone/<M>
// (D1), cut to the diff cap.
func (b *assessBuild) readDiffs(ctx context.Context) {
	out := b.out
	for _, e := range b.req.Events {
		if e.Event != EventChangeArchived || e.Change == "" {
			continue
		}
		sha, why := b.p.assessDiffCommit(ctx, e, b.ref("main"), b.have[e.Milestone], b.ref(MilestoneBranch(e.Milestone)))
		out.Diffs[e.Change] = sha
		if why != "" {
			out.DiffNotes[e.Change] = why
		}
		if sha == "" {
			continue
		}
		text, err := git(ctx, b.p.Root, "diff", "--no-color", "--no-ext-diff", "--no-textconv", sha+"^1", sha)
		if err != nil {
			out.Diffs[e.Change] = ""
			out.DiffNotes[e.Change] = "git diff of " + short(sha) + " failed: " + err.Error()
			continue
		}
		if len(text) > b.diffCap {
			text = text[:b.diffCap] + fmt.Sprintf("\n[cut: the diff is %d bytes; the first %d are shown]\n", len(text), b.diffCap)
			out.Truncated = true
		}
		b.diffs = append(b.diffs, assessDiff{e.Change, e.Milestone, sha, text})
	}
}

func (b *assessBuild) writeEvents() {
	w, out := &b.w, b.out
	w.WriteString("## Events\n\n")
	for i, e := range b.req.Events {
		fmt.Fprintf(w, "### %d. %s: %s", i+1, e.Event, e.Milestone)
		if e.Change != "" {
			fmt.Fprintf(w, ", change %s", e.Change)
		}
		w.WriteString("\n\n")
		fmt.Fprintf(w, "- message id: `%s`\n", e.MsgID)
		if e.Commit != "" {
			fmt.Fprintf(w, "- commit as the event gives it: `%s`\n", e.Commit)
		}
		if len(e.Paths) > 0 {
			fmt.Fprintf(w, "- paths as the event gives them: %s\n", codeList(e.Paths))
		}
		if e.Gate != "" {
			fmt.Fprintf(w, "- gate: `%s`\n", e.Gate)
		}
		if e.Event == EventChangeArchived && e.Change != "" {
			switch sha := out.Diffs[e.Change]; {
			case sha == "":
				fmt.Fprintf(w, "- diff: not read: %s\n", out.DiffNotes[e.Change])
			case out.DiffNotes[e.Change] != "":
				fmt.Fprintf(w, "- diff: under Diffs below, from %s on `%s` (%s)\n", short(sha), MilestoneBranch(e.Milestone), out.DiffNotes[e.Change])
			default:
				fmt.Fprintf(w, "- diff: under Diffs below, from %s on `%s`\n", short(sha), MilestoneBranch(e.Milestone))
			}
		}
		w.WriteString("\n")
	}
}

func (b *assessBuild) writeMilestones(ctx context.Context) {
	w := &b.w
	w.WriteString("## Open milestones\n\n")
	if len(b.out.Open) == 0 {
		w.WriteString("None: main's roadmap has no approved milestone.\n\n")
	}
	for _, id := range b.out.Open {
		m := b.rm.Find(id)
		fmt.Fprintf(w, "### %s: %s\n\n", m.ID, m.Title)
		state := "approved"
		if b.p.claimRecorded(ctx, id, b.ref("main")) {
			state += ", claimed"
		}
		if b.withEvents[id] {
			state += "; this batch has events of its own"
		}
		fmt.Fprintf(w, "- state: %s\n", state)
		if m.Gate != "" {
			fmt.Fprintf(w, "- gate: `%s`\n", m.Gate)
		}
		if gp := assessGatePaths(b.out.Dir, m.Gate, b.recs[id]); len(gp) > 0 {
			fmt.Fprintf(w, "- gate paths: %s\n", codeList(gp))
		}
		w.WriteString("- contracts consumed: none recorded (contracts come in stage 2)\n")
		br := MilestoneBranch(id)
		if b.have[id] {
			chs := b.p.assessChangesOn(ctx, b.ref(br), b.snap, id)
			if len(chs) == 0 {
				fmt.Fprintf(w, "- changes on `%s`: none\n", br)
			}
			for i := range chs {
				fmt.Fprintf(w, "- change %s on `%s`: touches: %s; paths: %s\n", chs[i].ID, br, codeList(chs[i].Touches), codeList(chs[i].Paths))
			}
		} else {
			fmt.Fprintf(w, "- changes: `%s` is not on the remote\n", br)
		}
		fmt.Fprintf(w, "\nIts plan, main's roadmap section:\n\n%s\n\n", fenced("markdown", roadmapSection(b.rm, m)))
	}
}

// assessDiffCommit is the commit an archived change's diff is read from
// (D1): the event's commit when it is on milestone/<M> and not on main,
// else the newest such commit whose Change: trailer names the change, else
// none, with why.
func (p *Project) assessDiffCommit(ctx context.Context, e AssessEvent, mainRef string, haveBranch bool, msRef string) (sha, why string) {
	if !assessMilestoneRe.MatchString(e.Milestone) {
		return "", fmt.Sprintf("the event's milestone %q is not a milestone id", e.Milestone)
	}
	if !haveBranch {
		return "", "`" + MilestoneBranch(e.Milestone) + "` is not on the remote"
	}
	onBranch, err := git(ctx, p.Root, "rev-list", mainRef+".."+msRef)
	if err != nil {
		return "", "listing `" + MilestoneBranch(e.Milestone) + "`'s commits failed"
	}
	commits := strings.Fields(onBranch)
	if assessCommitRe.MatchString(e.Commit) {
		for _, c := range commits {
			if strings.HasPrefix(c, e.Commit) {
				return c, ""
			}
		}
	}
	why = "the event's commit is not on `" + MilestoneBranch(e.Milestone) + "`, and was not read"
	if e.Commit == "" {
		why = "the event names no commit"
	}
	log, err := git(ctx, p.Root, "log", "--format=%H%x1f%(trailers:key=Change,valueonly,separator=%x2C)", mainRef+".."+msRef)
	if err == nil {
		for line := range strings.SplitSeq(log, "\n") {
			h, ids, _ := strings.Cut(line, "\x1f")
			if slices.Contains(strings.Split(strings.TrimSpace(ids), ","), e.Change) {
				return h, why + "; the diff is of the commit there whose Change: trailer names " + e.Change
			}
		}
	}
	return "", why + "; no commit there names change " + e.Change
}

// assessChangesOn are milestone ms's changes as branch holds them: those
// in flight and those archived, by their change.md frontmatter.
func (p *Project) assessChangesOn(ctx context.Context, branch string, snap *Project, ms string) []ChangeMeta {
	var out []ChangeMeta
	for _, dir := range []string{snap.Rel(snap.ChangesDir()), snap.Rel(snap.ArchivedChangesDir())} {
		names, err := git(ctx, p.Root, "ls-tree", "--name-only", branch+":"+dir)
		if err != nil {
			continue
		}
		for name := range strings.SplitSeq(names, "\n") {
			if name == "" || name == "archive" || strings.HasPrefix(name, ".") {
				continue
			}
			rel := path.Join(dir, name, "change.md")
			doc, err := gitRaw(ctx, p.Root, "show", branch+":"+rel)
			if err != nil {
				continue
			}
			c := ParseChange(rel, doc)
			if c.Meta.Milestone == ms {
				out = append(out, c.Meta)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// assessGatePaths are the paths a milestone's gate names: the files and
// globs its approval recorded, and the words of its command that name a
// path in the snapshot.
func assessGatePaths(dir, gate string, rec *MilestoneRecord) []string {
	seen := map[string]bool{}
	if rec != nil && rec.Approval != nil {
		for f := range rec.Approval.GateFiles {
			seen[f] = true
		}
		for f := range rec.Approval.GateOwnFiles {
			seen[f] = true
		}
		for _, g := range rec.Approval.GateGlobs {
			seen[g] = true
		}
	}
	for word := range strings.FieldsSeq(gate) {
		w := strings.TrimSuffix(path.Clean(strings.Trim(word, `"'`)), "/...")
		if w == "." || w == "" || !filepath.IsLocal(w) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(w))); err == nil {
			seen[w] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// roadmapSection is milestone m's section of the roadmap, its heading
// included.
func roadmapSection(r *Roadmap, m *Milestone) string {
	if m.Line < 1 || m.Line > len(r.lines) {
		return ""
	}
	end := len(r.lines)
	for i := m.Line; i < len(r.lines); i++ {
		if strings.HasPrefix(r.lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.TrimRight(strings.Join(r.lines[m.Line-1:end], "\n"), "\n")
}

// extractSnapshot writes the tar stream's regular files and directories
// under dir and returns what it did not: links and other entries (R1),
// hot/ and .git/, and a top-level file named like the folder's own.
func extractSnapshot(dir string, data []byte) (dropped []string, err error) {
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return dropped, nil
		}
		if err != nil {
			return dropped, err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "pax_global_header" || h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		top, _, _ := strings.Cut(name, "/")
		switch {
		case !filepath.IsLocal(name):
			dropped = append(dropped, name+" (outside the folder)")
			continue
		case top == "hot" || top == ".git":
			if name == top {
				dropped = append(dropped, top+"/")
			}
			continue
		case name == AssessInput || name == AssessNotice:
			dropped = append(dropped, name+" (the folder's own file)")
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o700); err != nil {
				return dropped, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return dropped, err
			}
			content, err := io.ReadAll(tr)
			if err != nil {
				return dropped, err
			}
			if err := os.WriteFile(p, content, 0o600); err != nil {
				return dropped, err
			}
		case tar.TypeSymlink:
			dropped = append(dropped, name+" (a symbolic link)")
		default:
			dropped = append(dropped, name+" (not a regular file)")
		}
	}
}

// fenced is s in a code fence longer than any backtick run in it, so a
// diff or a plan cannot close the fence early.
func fenced(lang, s string) string {
	n := 3
	run := 0
	for _, r := range s {
		if r == '`' {
			run++
			n = max(n, run+1)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", n)
	return f + lang + "\n" + strings.TrimRight(s, "\n") + "\n" + f
}

// codeList is xs as `a`, `b`; "none" for none.
func codeList(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + x + "`"
	}
	return strings.Join(out, ", ")
}
