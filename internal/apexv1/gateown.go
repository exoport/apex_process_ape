package apexv1

import (
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
