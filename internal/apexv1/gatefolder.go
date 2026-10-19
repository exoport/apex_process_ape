package apexv1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Gate folders (D155). A milestone's gate lives in a folder of its own,
// <dev>/gates/<M>-<slug>/ (its gate script, the checks it calls, its
// stub), beside a shared <dev>/gates/lib/; apex-plan writes it there.
// The tree keeps only the gates of open milestones: `milestone close`
// moves the milestone's gate folders to <dev>/archive/milestones/<M>/gate/
// in the close commit, beside the close evidence, and records the move
// (gate_archived) so the approval's gate_files, which keep the paths as
// approved, still verify: the move leaves every file's content, and so
// its SHA-256, as it was. A gate approved before rc.6 may run scripts
// elsewhere (scripts/…): that is a notice, not a finding (Q73), since
// moving an approved gate's script is gate drift — they move with the
// milestone's next re-approval. Gates outside <dev>/gates/ never move at
// close (framework Q83).

// GatesDir is <dev>/gates/, the gate folders of the open milestones.
func (p *Project) GatesDir() string {
	return filepath.Join(p.Dev(), "gates")
}

// MilestoneGateArchiveDir is where close moves a milestone's gate
// folders: <dev>/archive/milestones/<M>/gate/, beside its evidence/.
func (p *Project) MilestoneGateArchiveDir(id string) string {
	return filepath.Join(p.ArchiveDir(), "milestones", id, "gate")
}

// GateArchive is one gate folder close moved (D155 Q74), as repo-relative
// slash paths: From, the folder under <dev>/gates/, To, where it went.
type GateArchive struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ArchivedPath is rel, a gate file path as approved, where it is since
// close moved its folder: the first gate_archived entry whose From is rel
// or a folder above it maps the prefix; any other path is unchanged.
func (r *MilestoneRecord) ArchivedPath(rel string) string {
	if r == nil {
		return rel
	}
	for _, a := range r.GateArchived {
		if rel == a.From {
			return a.To
		}
		if rest, ok := strings.CutPrefix(rel, a.From+"/"); ok {
			return a.To + "/" + rest
		}
	}
	return rel
}

// ArchivedGateFiles is the approval's gate_files keyed by where each file
// is now (ArchivedPath), with the SHA-256 it was approved with: what a
// verifier of a closed milestone's gate compares the files against.
// Empty without an approval.
func (r *MilestoneRecord) ArchivedGateFiles() map[string]string {
	out := map[string]string{}
	if r == nil || r.Approval == nil {
		return out
	}
	for rel, sum := range r.Approval.GateFiles {
		out[r.ArchivedPath(rel)] = sum
	}
	return out
}

// milestoneGateFolders are the folders under <dev>/gates/ whose name is
// <M>-*, sorted: the milestone's gate folders. lib/ is no milestone's.
func (p *Project) milestoneGateFolders(id string) []string {
	matches, _ := filepath.Glob(filepath.Join(p.GatesDir(), id+"-*"))
	var out []string
	for _, m := range matches {
		if dirExists(m) {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// gateArchivePlan is where close moves the milestone's gate folders: one
// folder's contents become gate/, several go to gate/<folder>/ each. It
// refuses when gate/ exists already (milestone.gate-archive-exists), and
// is empty when the milestone has no gate folder.
func (p *Project) gateArchivePlan(id string) ([]GateArchive, error) {
	folders := p.milestoneGateFolders(id)
	if len(folders) == 0 {
		return nil, nil
	}
	dst := p.MilestoneGateArchiveDir(id)
	if fileOrDirExists(dst) {
		return nil, &FailError{Code: CodeGateArchiveExists, Msg: id + " has gate folders to archive, but " + p.Rel(dst) + "/ exists already" +
			" — move it away (or merge the folders into it by hand), then close again"}
	}
	out := make([]GateArchive, 0, len(folders))
	for _, f := range folders {
		to := dst
		if len(folders) > 1 {
			to = filepath.Join(dst, filepath.Base(f))
		}
		out = append(out, GateArchive{From: p.Rel(f), To: p.Rel(to)})
	}
	return out, nil
}

// archiveGateFolders moves each planned folder, and returns the paths the
// close commit records the moves by: every To, and each From git tracks
// (its files' deletion; an untracked one has none). The move is a
// rename on disk, which git records as a rename with the content, and so
// each file's SHA-256, unchanged; an untracked file in the folder goes
// with it and is committed at its new place.
func (p *Project) archiveGateFolders(ctx context.Context, plan []GateArchive) ([]string, error) {
	var paths []string
	for _, a := range plan {
		from := filepath.Join(p.Root, filepath.FromSlash(a.From))
		to := filepath.Join(p.Root, filepath.FromSlash(a.To))
		tracked, _ := git(ctx, p.Root, "ls-files", "--", a.From)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return paths, err
		}
		if err := os.Rename(from, to); err != nil {
			return paths, fmt.Errorf("moving the gate folder %s to %s: %w", a.From, a.To, err)
		}
		paths = append(paths, a.To)
		if tracked != "" {
			paths = append(paths, a.From)
		}
	}
	return paths, nil
}

// gateNotices are `project check`'s notices for gates outside
// <dev>/gates/ (Q73): an open milestone (proposed or approved) whose gate
// runs a script file — gateScripts, a regular file under the root — that
// is not under it. A gate with no script file (a bare command such as
// `make e2e`) has none.
func (p *Project) gateNotices() []Diagnostic {
	rm := p.roadmap()
	if rm == nil {
		return nil
	}
	gates := p.Rel(p.GatesDir()) + "/"
	file := p.Rel(p.RoadmapMD())
	var out []Diagnostic
	for i := range rm.Milestones {
		m := &rm.Milestones[i]
		if m.Status != MilestoneProposed && m.Status != MilestoneApproved {
			continue
		}
		var outside []string
		for _, rel := range gateScripts(m.Gate) {
			if _, ok := p.fileSHA256(rel); ok && !strings.HasPrefix(rel, gates) {
				outside = append(outside, rel)
			}
		}
		if len(outside) == 0 {
			continue
		}
		when := "with the milestone's next re-approval (moving an approved gate's script is gate drift)"
		if m.Status == MilestoneProposed {
			when = "before it is approved"
		}
		out = append(out, notice(Diagnostic{Code: CodeGateOutsideGates, File: file, Line: m.Line, Message: m.ID + " gate runs scripts outside " + gates + ": " +
			strings.Join(outside, ", ") + " — move them into " + gates + m.ID + "-<slug>/ " + when}))
	}
	return out
}

// notice is d as a notice: its Message, in the "<what> — <fix>" shape,
// split, with the code's published action.
func notice(d Diagnostic) Diagnostic {
	d.Severity = SeverityNotice
	d.Message, d.Fix = splitFix(d.Message)
	d.Action = ActionFor(d.Code)
	return d
}

// Notices are `ape project check`'s notices (D155 Q73): what is worth a
// person's attention but is not a finding, so it never fails the check.
func (p *Project) Notices() []Diagnostic {
	return append(append(append(p.gateNotices(), p.preCommitNotices(context.Background())...), p.promptHookNotices()...), p.operatorRowNotices()...)
}
