package apexv1

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RK-6 R1 (D311, D313, Q146): the approval hashed the gate command's
// scripts only, so a file a gate script reads — atomix M4's
// scripts/checks/workflows-sagas.checks, the checks its gate runs — could
// change after the approval with no drift, whoever changed it. A gate's
// OWN files are recorded too (approval.gate_own_files), and drift and
// `ape change verify`'s gate guard (R2) compare them:
//
//   - every regular file under the milestone's gate folders,
//     <dev>/gates/<M>-*/, and under <dev>/gates/lib/ (D155: the gate
//     script, the checks it calls, its stub — no work lives there);
//   - every file a recorded gate script sources (". <path>" or "source
//     <path>"), literal or relative to the script ("$(dirname "$0")/x");
//   - every literal relative path a recorded gate script names that is a
//     regular file under that script's own folder, when the folder is
//     neither the project root nor the development folder.
//
// Never the files a gate checks: a gate names its work products (README.md,
// docs/…, CHANGELOG.md), which the milestone's work edits — rc.1 hashed
// those and every edit read as drift (D53). One level: the files found are
// not read in turn.

var (
	// sourceRe is a script line that sources a file.
	sourceRe = regexp.MustCompile(`(?m)^\s*(?:\.|source)\s+(\S.*)$`)
	// scriptDirRe is a script's own folder written as an expansion, its
	// quotes removed: $(dirname $0)/ or ${0%/*}/.
	scriptDirRe = regexp.MustCompile(`^(?:\$\(dirname \$0\)|\$\{0%/\*\})/`)
	// pathWordRe is a literal relative path in a script: a word with a
	// slash, made of path characters.
	pathWordRe = regexp.MustCompile(`[A-Za-z0-9_.][A-Za-z0-9_./-]*/[A-Za-z0-9_./-]+`)
)

// gateOwnFiles are m's gate's own files (above), each with its content's
// SHA-256, less the scripts gate_files records.
func (p *Project) gateOwnFiles(m *Milestone) map[string]string {
	out := map[string]string{}
	add := func(rel string) {
		if sum, ok := p.fileSHA256(rel); ok {
			out[rel] = sum
		}
	}
	folders := p.milestoneGateFolders(m.ID)
	if lib := filepath.Join(p.GatesDir(), "lib"); dirExists(lib) {
		folders = append(folders, lib)
	}
	for _, dir := range folders {
		_ = filepath.WalkDir(dir, func(abs string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				add(p.Rel(abs))
			}
			return nil
		})
	}
	scripts := gateScripts(m.Gate)
	for _, s := range scripts {
		data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(s)))
		if err != nil {
			continue
		}
		dir := path.Dir(s)
		text := string(data)
		for _, mm := range sourceRe.FindAllStringSubmatch(text, -1) {
			arg := strings.NewReplacer(`"`, "", `'`, "").Replace(mm[1])
			w := ""
			if loc := scriptDirRe.FindStringIndex(arg); loc != nil {
				if f := strings.Fields(arg[loc[1]:]); len(f) > 0 {
					w = path.Join(dir, f[0])
				}
			} else if f := strings.Fields(arg); len(f) > 0 {
				w = f[0]
			}
			if rel, ok := repoPath(w); ok {
				add(rel)
			}
		}
		if dir == "." || dir == p.DevRel {
			continue
		}
		for _, w := range pathWordRe.FindAllString(text, -1) {
			rel, ok := repoPath(strings.TrimRight(w, "."))
			if ok && strings.HasPrefix(rel, dir+"/") {
				add(rel)
			}
		}
	}
	for _, s := range scripts {
		delete(out, s)
	}
	return out
}

// gateOwnDrift lists the recorded own files of m's gate that are gone or
// changed. A file the record does not hold is not drift (created since,
// or recorded before rc.7o): the next re-approval freezes it.
func (p *Project) gateOwnDrift(a *MilestoneApproval) []string {
	names := make([]string, 0, len(a.GateOwnFiles))
	for n := range a.GateOwnFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		switch is, exists := p.fileSHA256(n); {
		case !exists:
			out = append(out, n+" is gone")
		case is != a.GateOwnFiles[n]:
			out = append(out, n+" changed")
		}
	}
	return out
}

// gateOwnUnrecorded reports an own file of m's gate that a's record does
// not hold.
func (p *Project) gateOwnUnrecorded(m *Milestone, a *MilestoneApproval) bool {
	for n := range p.gateOwnFiles(m) {
		if _, ok := a.GateOwnFiles[n]; !ok {
			return true
		}
	}
	return false
}

// CodeTouchesGate: a change's own commits edit a recorded gate file of its
// milestone (RK-6 R2) — verify and archive refuse it, action replan.
const CodeTouchesGate = "change.touches-gate"

// refuseGateTouch is RK-6 R2: a change whose diff from its base_sha to
// HEAD edits a recorded gate file — a script gate_files holds, or an own
// file gate_own_files holds — of the milestone it belongs to (a tweak:
// of every approved milestone) is refused. Only a person or apex-plan
// changes a gate, on the base branch, and a person approves it again; a
// builder that edits the gate weakens what judges its own work.
func (p *Project) refuseGateTouch(ctx context.Context, ci ChangeInfo) error {
	if ci.State == nil || ci.Doc == nil || ci.State.BaseSHA == "" {
		return nil
	}
	rm := p.roadmap()
	if rm == nil {
		return nil
	}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return err
	}
	owner := map[string]string{}
	for i := range rm.Milestones {
		m := &rm.Milestones[i]
		if m.Status != MilestoneApproved || ci.Doc.Meta.Milestone != m.ID && ci.Doc.Meta.Milestone != MilestoneNone {
			continue
		}
		if rec := recs[m.ID]; rec != nil && rec.Approval != nil {
			for _, files := range []map[string]string{rec.Approval.GateFiles, rec.Approval.GateOwnFiles} {
				for f := range files {
					owner[f] = m.ID
				}
			}
		}
	}
	if len(owner) == 0 {
		return nil
	}
	out, err := git(ctx, p.Root, "diff", "--name-only", "--no-renames", ci.State.BaseSHA, "HEAD")
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "cannot read the change's diff from its base_sha: " + err.Error()}
	}
	var touched, files []string
	for f := range strings.SplitSeq(out, "\n") {
		if m, ok := owner[f]; ok {
			touched = append(touched, f+" ("+m+"'s gate)")
			files = append(files, f)
		}
	}
	if len(touched) == 0 {
		return nil
	}
	return &FailError{Code: CodeTouchesGate, Msg: "change " + ci.ID + " edits a gate a person approved: " + strings.Join(limit(touched, 8), ", ") +
		" — only a person or apex-plan changes a gate, and a person approves it again: restore them on " + ChangeBranch(ci.ID) +
		" (`git checkout " + ci.State.BaseSHA + " -- " + strings.Join(limit(files, 8), " ") + "`, committed with trailer `Change: " + ci.ID + "`); if the gate itself is wrong, say so (`gate defect: <evidence>`)"}
}

// D319 (v1.1 W8), the D313a residual: a gate script that reads a check
// list kept outside the gate's own files — eval's toy fed
// `while read …; done < scripts/checks/m1.checks` from a gate under
// <dev>/gates/ — is not recorded at approval, by design (D313a records
// only the gate's own files, never what it checks), so an edit to the list
// is no gate drift and no change.touches-gate. project check names it, as
// the notice gate.data-outside: a file an open milestone's gate script
// reads as its input — an input redirection, `< <path>` — that exists and
// is none of the gate's own files. A path the script only names (a work
// product it greps or tests) is not read as input, and is never named.

// CodeGateDataOutside: project check's notice for a gate script's input
// file outside the gate's own files.
const CodeGateDataOutside = "gate.data-outside"

// inputRedirectRe is an input redirection in a shell script and its word:
// `<` not part of `<<`, `<<<`, `<(`, `<&` or `<>`.
var inputRedirectRe = regexp.MustCompile(`(?:^|[^<])<[ \t]*("[^"\n]*"|'[^'\n]*'|[^\s"'<>(&;|)][^\s<>&;|()]*)`)

// gateDataNotices are project check's gate.data-outside notices: for each
// open milestone (proposed or approved), each regular file under the
// project one of its gate scripts reads by an input redirection that is not
// one of the gate's own files (gateOwnFiles) nor a gate script.
func (p *Project) gateDataNotices() []Diagnostic {
	rm := p.roadmap()
	if rm == nil {
		return nil
	}
	gates := p.Rel(p.GatesDir()) + "/"
	var out []Diagnostic
	for i := range rm.Milestones {
		m := &rm.Milestones[i]
		if m.Status != MilestoneProposed && m.Status != MilestoneApproved {
			continue
		}
		scripts := gateScripts(m.Gate)
		if len(scripts) == 0 {
			continue
		}
		own := p.gateOwnFiles(m)
		for _, s := range scripts {
			own[s] = ""
		}
		when := "with the milestone's next re-approval (the gate script changes, and a person approves it again)"
		if m.Status == MilestoneProposed {
			when = "before it is approved"
		}
		for _, s := range scripts {
			data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(s)))
			if err != nil {
				continue
			}
			seen := map[string]bool{}
			for n, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				for _, mm := range inputRedirectRe.FindAllStringSubmatch(line, -1) {
					rel, ok := scriptWordPath(s, mm[1])
					if _, mine := own[rel]; !ok || mine || seen[rel] {
						continue
					}
					if _, exists := p.fileSHA256(rel); !exists {
						continue
					}
					seen[rel] = true
					out = append(out, notice(Diagnostic{Code: CodeGateDataOutside, File: s, Line: n + 1, Message: m.ID + "'s gate script " + s + " reads " + rel +
						" (`< " + rel + "`), which is not one of the gate's own files, so the approval does not record it: an edit to it is no gate drift and no change.touches-gate (D313a)" +
						" — if it is the gate's own check list, move it into " + gates + m.ID + "-<slug>/ and read it from there " + when + "; if it is a work product the gate checks, leave it"}))
				}
			}
		}
	}
	return out
}

// scriptWordPath is a redirection's word in the script at script as a
// project-relative path: its quotes removed, the script's own folder
// ($(dirname "$0")/x, ${0%/*}/x) joined with script's; false for any other
// expansion or a path outside the project.
func scriptWordPath(script, word string) (string, bool) {
	w := strings.NewReplacer(`"`, "", `'`, "").Replace(word)
	if loc := scriptDirRe.FindStringIndex(w); loc != nil {
		w = path.Join(path.Dir(script), w[loc[1]:])
	}
	return repoPath(w)
}
