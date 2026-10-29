package apexv1

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
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
//     <path>"), literal or written from the script's own folder (below);
//   - every relative path a recorded gate script names that is a regular
//     file under an allowed folder: the gate script's own folder, and in a
//     sourced file that file's folder too — when the folder is neither the
//     project root nor the development folder.
//
// Never the files a gate checks: a gate names its work products (README.md,
// docs/…, CHANGELOG.md), which the milestone's work edits — rc.1 hashed
// those and every edit read as drift (D53).
//
// v1.2 F4 (D433, D434) closes the forms v1.1 missed:
//
//   - the script's own folder written as an expansion: $(dirname "$0"),
//     ${0%/*}, $(cd "$(dirname "$0")" && pwd) (pwd -P too), realpath and
//     readlink -f around either, and the BASH_SOURCE / ${BASH_SOURCE[0]}
//     forms; and a variable assigned one (here=$(cd "$(dirname "$0")" &&
//     pwd)), then used as $here/x, ${here}/x or "$here"/x. $V/../x is the
//     parent folder's x: the folder rule applies to the folder it resolves
//     to (the project root's README.md stays out);
//   - a sourced file is read in turn, the same rules inside it, up to
//     three files deep and never twice: $0 there is still the gate script,
//     BASH_SOURCE the sourced file; a variable set on one side of a source
//     is seen on the other, as the shell does;
//   - a path with a glob (scripts/checks/*.checks) under an allowed folder
//     that matches a file at approval: each match is frozen, and the
//     pattern is recorded (approval.gate_globs), so a file that matches it
//     later is drift like an edited one;
//   - a declaration, in a gate script or a file it sources:
//     "# ape: gate-files <path|glob>…" — each repo-relative path or glob is
//     the gate's own, wherever it lives (the author asked: no folder rule;
//     a declared glob is recorded even with no match yet).
//
// v1.4 D581 (FINDING-C1-1): a gate that keeps scratch output under its own
// folder — ledger's lib wrote <gate>/.work/io/err, its .gitignore holding
// development/gates/*/.work/ — had that output frozen, and the green run
// that rewrote it refused the close as milestone.gate-changed. A file git
// ignores is the gate's scratch, not its own (gateScratch), unless it can
// change what the gate does: a file a gate script sources, one a
// declaration names, or a script — an executable bit, a #! line, or a
// script's extension (scriptExts). A tracked file, or an untracked one git
// does not ignore, is frozen as before, wherever the scan found it.

var (
	// sourceRe is a script line that sources a file.
	sourceRe = regexp.MustCompile(`(?m)^\s*(?:\.|source)\s+(\S.*)$`)
	// scriptDirRe is a script's own folder written as an expansion, its
	// quotes removed: $(dirname $0)/ or ${0%/*}/.
	scriptDirRe = regexp.MustCompile(`^(?:\$\(dirname \$0\)|\$\{0%/\*\})/`)
	// pathWordRe is a literal relative path in a script: a word with a
	// slash, made of path characters.
	pathWordRe = regexp.MustCompile(`[A-Za-z0-9_.][A-Za-z0-9_./-]*/[A-Za-z0-9_./-]+`)
	// globWordRe is a literal relative path with a glob in a script.
	globWordRe = regexp.MustCompile(`[A-Za-z0-9_.*?\[][A-Za-z0-9_./*?\[\]-]*/[A-Za-z0-9_./*?\[\]-]+`)
	// declaredRe is the declaration "# ape: gate-files <path|glob>…".
	declaredRe = regexp.MustCompile(`^\s*#\s*ape:\s*gate-files\s+(.+)$`)
	// shellAssignRe is a shell variable assignment, its value one word.
	shellAssignRe = regexp.MustCompile(`^\s*(?:(?:export|local|readonly|declare|typeset)(?:\s+-[A-Za-z]+)*\s+)?([A-Za-z_][A-Za-z0-9_]*)=([^\s;&|]*)`)
	// shellVarRe is a variable's expansion, $NAME or ${NAME}.
	shellVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	// anyExpansionRe is an expansion with what follows it in its word:
	// blanked before literal words are read.
	anyExpansionRe = regexp.MustCompile(`\$(?:\{[^}\s]*\}|\([^)\s]*\)|[A-Za-z_0-9@*#?$!-]+)[A-Za-z0-9_./*?\[\]-]*`)
)

// The markers gateOwnScan rewrites a line's script references to: the gate
// script's path ($0), the read file's path (BASH_SOURCE), and a folder,
// \x01D<n>\x02, the scan's n-th.
const (
	markGate   = "\x01P0\x02"
	markSource = "\x01PS\x02"
)

var (
	// markX is a marker and an optional relative path after it.
	markX = `\x01(P0|PS|D\d+)\x02((?:/[A-Za-z0-9_.-]+)*)`
	// dirnameRe: $(dirname X), `dirname X`.
	dirnameRe = regexp.MustCompile(`\$\(\s*dirname\s+(?:--\s+)?` + markX + `/?\s*\)|` + "`" + `\s*dirname\s+(?:--\s+)?` + markX + `/?\s*` + "`")
	// realpathRe: $(realpath X), $(readlink -f X) — the same path.
	realpathRe = regexp.MustCompile(`\$\(\s*(?:realpath|readlink)(?:\s+-[A-Za-z-]+)*\s+(?:--\s+)?` + markX + `/?\s*\)`)
	// cdPwdRe: $(cd X && pwd), pwd -P, ; for &&, cd -P / cd --.
	cdPwdRe = regexp.MustCompile(`\$\(\s*cd\s+(?:-[PLe]+\s+|--\s+)*` + markX + `/?(?:\s*[0-9&]?>>?\s*[^\s&;]+)*\s*(?:&&|;)\s*pwd(?:\s+-[PL])?\s*\)`)
	// markWordRe is a folder marker and the path after it in a word.
	markWordRe = regexp.MustCompile(`\x01D(\d+)\x02(/[A-Za-z0-9_./*?\[\]-]*)?`)
	markAnyRe  = regexp.MustCompile(`\x01[^\x02]*\x02[A-Za-z0-9_./*?\[\]-]*`)
)

// maxSourceDepth is how many files deep gateOwnScan reads sourced files.
const maxSourceDepth = 3

// gateOwn is what gateOwnScan finds: the gate's own files, each with its
// content's SHA-256, and the globs that name some of them; keep are the
// files sourced or declared, frozen even when git ignores them (D581).
type gateOwn struct {
	files map[string]string
	globs map[string]bool
	keep  map[string]bool
}

// gateOwnFiles are m's gate's own files (above), each with its content's
// SHA-256, less the scripts gate_files records.
func (p *Project) gateOwnFiles(m *Milestone) map[string]string {
	return p.gateOwnScan(m).files
}

// gateOwnScan is gateOwnFiles with the globs recorded beside them.
func (p *Project) gateOwnScan(m *Milestone) gateOwn {
	own := gateOwn{files: map[string]string{}, globs: map[string]bool{}, keep: map[string]bool{}}
	folders := p.milestoneGateFolders(m.ID)
	if lib := filepath.Join(p.GatesDir(), "lib"); dirExists(lib) {
		folders = append(folders, lib)
	}
	for _, dir := range folders {
		_ = filepath.WalkDir(dir, func(abs string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				own.add(p, p.Rel(abs))
			}
			return nil
		})
	}
	scripts := gateScripts(m.Gate)
	for _, s := range scripts {
		sc := &ownScan{p: p, own: own, gate: s, vars: map[string]string{}, seen: map[string]bool{}, marks: map[string]int{}}
		sc.read(s, 0)
	}
	for _, s := range scripts {
		delete(own.files, s)
	}
	for f := range p.gateScratch(slices.Collect(maps.Keys(own.files)), own.keep) {
		delete(own.files, f)
	}
	return own
}

// scriptExts are the extensions that make an ignored file a script a gate
// may run (gateScratch): `bash x.sh` needs neither an executable bit nor a
// #! line.
var scriptExts = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".ksh": true, ".py": true, ".pl": true, ".rb": true,
	".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".awk": true, ".sed": true, ".php": true, ".lua": true, ".tcl": true,
	// D582: run through a tool rather than an interpreter, with no x bit or
	// #! line all the same: `go run x.go`, `make -f x.mk`, `pwsh x.ps1`.
	".go": true, ".mk": true, ".ps1": true,
}

// scriptNames are the basenames that make an ignored file a script with no
// extension at all: `make -C <dir>` runs the Makefile it finds there (D582).
var scriptNames = map[string]bool{"makefile": true, "gnumakefile": true}

// gateScratch are the files among names that are a gate's scratch (D581):
// git ignores them (a tracked file never is), keep does not hold them, and
// none is a script — an executable bit, a #! line or a script's extension.
// Empty outside a git repository. It takes no context: approval and drift,
// which read it, take none either, and check-ignore is local and quick.
//
//nolint:contextcheck,nolintlint // contextcheck reads a declaration's directive to stop the chain; nolintlint cannot see that
func (p *Project) gateScratch(names []string, keep map[string]bool) map[string]bool {
	out := map[string]bool{}
	if len(names) == 0 {
		return out
	}
	for _, f := range gitIgnored(context.Background(), p.Root, false, names...) {
		if !keep[f] && !p.isScript(f) {
			out[f] = true
		}
	}
	return out
}

// isScript reports a file a gate could run: an executable bit, a #! line
// or a script's extension (scriptExts).
func (p *Project) isScript(rel string) bool {
	if scriptExts[strings.ToLower(path.Ext(rel))] || scriptNames[strings.ToLower(path.Base(rel))] {
		return true
	}
	abs := filepath.Join(p.Root, filepath.FromSlash(rel))
	if st, err := os.Stat(abs); err == nil && st.Mode().Perm()&0o111 != 0 {
		return true
	}
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 2)
	n, _ := f.Read(head)
	return n == 2 && string(head) == "#!"
}

func (o gateOwn) add(p *Project, rel string) bool {
	sum, ok := p.fileSHA256(rel)
	if ok {
		o.files[rel] = sum
	}
	return ok
}

// sortedGlobs are o's globs, sorted.
func (o gateOwn) sortedGlobs() []string {
	out := make([]string, 0, len(o.globs))
	for g := range o.globs {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// ownScan reads one gate script and the files it sources.
type ownScan struct {
	p    *Project
	own  gateOwn
	gate string            // the gate script: $0 in every file read
	vars map[string]string // variables holding a folder → that folder
	seen map[string]bool   // files read
	dirs []string          // the folders the markers stand for
	// marks are the folders' marker numbers.
	marks map[string]int
}

func (s *ownScan) mark(dir string) string {
	n, ok := s.marks[dir]
	if !ok {
		n = len(s.dirs)
		s.dirs = append(s.dirs, dir)
		s.marks[dir] = n
	}
	return "\x01D" + strconv.Itoa(n) + "\x02"
}

// allowed reports a folder whose files a gate may own: under the project
// and neither its root nor the development folder (D53).
func (s *ownScan) allowed(dir string) bool {
	return dir != "" && dir != "." && dir != s.p.DevRel && dir != ".." && !strings.HasPrefix(dir, "../") && !strings.HasPrefix(dir, "/")
}

// read scans file, a gate script or a file one sources (depth files deep).
func (s *ownScan) read(file string, depth int) {
	if s.seen[file] {
		return
	}
	s.seen[file] = true
	data, err := os.ReadFile(filepath.Join(s.p.Root, filepath.FromSlash(file)))
	if err != nil {
		return
	}
	var allowed []string
	for _, d := range []string{path.Dir(s.gate), path.Dir(file)} {
		if s.allowed(d) && (len(allowed) == 0 || allowed[0] != d) {
			allowed = append(allowed, d)
		}
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if mm := declaredRe.FindStringSubmatch(line); mm != nil {
			s.declared(mm[1])
		}
		red := s.assign(strings.NewReplacer(`"`, "", `'`, "").Replace(line), file)
		if w, ok := s.sourced(red); ok {
			if rel, ok := repoPath(w); ok && s.own.add(s.p, rel) {
				s.own.keep[rel] = true
				if depth < maxSourceDepth {
					s.read(rel, depth+1)
				}
			}
		}
		s.folderWords(red)
		s.literalWords(red, allowed)
	}
}

// assign is line reduced (reduce), each statement's assignment of a
// folder recorded in order — a variable set first on a line is used later
// on it (GATE_DIR=$(dirname "$0"); … < "$GATE_DIR/x") — and an assignment
// of anything else forgetting the variable.
func (s *ownScan) assign(line, file string) string {
	red := s.reduce(line, file)
	for i := 0; ; i++ {
		stmts := statementRe.Split(red, -1)
		if i >= len(stmts) {
			return red
		}
		mm := shellAssignRe.FindStringSubmatch(stmts[i])
		if mm == nil {
			continue
		}
		if dir, ok := s.markedPath(mm[2]); ok && !strings.ContainsAny(dir, "*?[") {
			s.vars[mm[1]] = dir
		} else {
			delete(s.vars, mm[1])
		}
		red = s.reduce(line, file)
	}
}

// markedPath is w, a whole word that is a folder marker and a path after
// it, as that path.
func (s *ownScan) markedPath(w string) (string, bool) {
	mw := markWordRe.FindStringSubmatch(w)
	if len(mw) < 3 || mw[0] != w {
		return "", false
	}
	return path.Join(s.dirs[atoi(mw[1])], mw[2]), true
}

// sourced is the path a reduced line sources, if it sources one.
func (s *ownScan) sourced(red string) (string, bool) {
	mm := sourceRe.FindStringSubmatch(red)
	if mm == nil {
		return "", false
	}
	f := strings.Fields(mm[1])
	if len(f) == 0 {
		return "", false
	}
	if w, ok := s.markedPath(f[0]); ok {
		return w, true
	}
	return f[0], true
}

// folderWords records each path a reduced line writes from a folder
// marker, when the folder it resolves to — $V/../x: V's parent — is one a
// gate may own.
func (s *ownScan) folderWords(red string) {
	for _, mw := range markWordRe.FindAllStringSubmatch(red, -1) {
		dir, rest := s.dirs[atoi(mw[1])], mw[2]
		if rest == "" {
			continue
		}
		base := dir
		for seg := range strings.SplitSeq(strings.TrimPrefix(rest, "/"), "/") {
			if seg != ".." && seg != "." {
				break
			}
			base = path.Join(base, seg)
		}
		if s.allowed(base) {
			s.word(path.Join(dir, rest), false)
		}
	}
}

// literalWords records each literal path and glob a reduced line names
// under one of the allowed folders.
func (s *ownScan) literalWords(red string, allowed []string) {
	under := func(rel string) bool {
		for _, d := range allowed {
			if strings.HasPrefix(rel, d+"/") {
				return true
			}
		}
		return false
	}
	lit := anyExpansionRe.ReplaceAllString(markAnyRe.ReplaceAllString(red, " "), " ")
	for _, w := range pathWordRe.FindAllString(lit, -1) {
		if rel, ok := repoPath(strings.TrimRight(w, ".")); ok && under(rel) {
			s.own.add(s.p, rel)
		}
	}
	for _, w := range globWordRe.FindAllString(lit, -1) {
		if pat, ok := repoGlob(w); ok && under(pat) {
			s.glob(pat, false)
		}
	}
}

// word records a path or glob a script wrote from a folder expansion.
func (s *ownScan) word(w string, declared bool) {
	if strings.ContainsAny(w, "*?[") {
		if pat, ok := repoGlob(w); ok {
			s.glob(pat, declared)
		}
		return
	}
	if rel, ok := repoPath(w); ok && s.own.add(s.p, rel) && declared {
		s.own.keep[rel] = true
	}
}

// declared records the words of a "# ape: gate-files" line.
func (s *ownScan) declared(words string) {
	for w := range strings.FieldsSeq(strings.NewReplacer(`"`, "", `'`, "").Replace(words)) {
		s.word(w, true)
	}
}

// glob freezes pat's matches and records pat: a pattern a script names
// only when it matches a file (a word with a [ or a * is often no glob);
// a declared one always.
func (s *ownScan) glob(pat string, declared bool) {
	matches := globFiles(s.p, pat)
	if len(matches) == 0 && !declared {
		return
	}
	s.own.globs[pat] = true
	for _, rel := range matches {
		if s.own.add(s.p, rel) && declared {
			s.own.keep[rel] = true
		}
	}
}

// globFiles are the regular files under the project root pat matches.
func globFiles(p *Project, pat string) []string {
	abs, err := filepath.Glob(filepath.Join(p.Root, filepath.FromSlash(pat)))
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range abs {
		if st, err := os.Stat(a); err == nil && st.Mode().IsRegular() {
			if rel, ok := repoPath(p.Rel(a)); ok {
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out
}

// repoGlob is a glob word as a clean pattern under the project root, as
// repoPath is a path.
func repoGlob(w string) (string, bool) {
	if !strings.ContainsAny(w, "*?[") {
		return "", false
	}
	if _, err := path.Match(w, ""); err != nil {
		return "", false
	}
	masked := strings.NewReplacer("*", "x", "?", "x", "[", "x", "]", "x").Replace(w)
	if _, ok := repoPath(masked); !ok {
		return "", false
	}
	return path.Clean(w), true
}

// reduce rewrites a line's script references (its quotes removed) to
// markers: $0 and BASH_SOURCE as paths, each expansion of a folder — the
// forms above, and the variables holding one — as that folder's marker.
func (s *ownScan) reduce(line, file string) string {
	gateDir, fileDir := path.Dir(s.gate), path.Dir(file)
	line = strings.NewReplacer(
		"${BASH_SOURCE[0]%/*}", s.mark(fileDir), "${BASH_SOURCE%/*}", s.mark(fileDir), "${0%/*}", s.mark(gateDir),
		"${BASH_SOURCE[0]}", markSource, "${BASH_SOURCE}", markSource, "${0}", markGate,
	).Replace(line)
	line = bashSourceRe.ReplaceAllString(line, markSource)
	line = dollarZeroRe.ReplaceAllString(line, markGate)
	line = shellVarRe.ReplaceAllStringFunc(line, func(v string) string {
		name := strings.Trim(v, "${}")
		if dir, ok := s.vars[name]; ok {
			return s.mark(dir)
		}
		return v
	})
	value := func(kind, rest string) string {
		switch kind {
		case "P0":
			return path.Join(s.gate, rest)
		case "PS":
			return path.Join(file, rest)
		}
		return path.Join(s.dirs[atoi(kind[1:])], rest)
	}
	for range 8 {
		before := line
		line = replaceSubmatch(dirnameRe, line, func(m []string) string {
			kind, rest := m[1], m[2]
			if kind == "" {
				kind, rest = m[3], m[4]
			}
			return s.mark(path.Dir(value(kind, rest)))
		})
		line = replaceSubmatch(realpathRe, line, func(m []string) string {
			if m[1][0] == 'P' && m[2] == "" {
				return "\x01" + m[1] + "\x02"
			}
			if m[1][0] == 'P' {
				return m[0]
			}
			return s.mark(value(m[1], m[2]))
		})
		line = replaceSubmatch(cdPwdRe, line, func(m []string) string {
			if m[1][0] == 'P' {
				return m[0]
			}
			return s.mark(value(m[1], m[2]))
		})
		if line == before {
			break
		}
	}
	return line
}

var (
	// statementRe splits a line into its statements, roughly.
	statementRe  = regexp.MustCompile(`;|&&|\|\|`)
	bashSourceRe = regexp.MustCompile(`\$BASH_SOURCE\b`)
	dollarZeroRe = regexp.MustCompile(`\$0`)
)

func replaceSubmatch(re *regexp.Regexp, s string, f func([]string) string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string { return f(re.FindStringSubmatch(m)) })
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// gateOwnDrift lists the recorded own files of m's gate that are gone or
// changed, and each file a recorded glob matches now that the record does
// not hold (F4: a check list added under scripts/checks/*.checks is new
// gate input). Any other file the record does not hold is not drift
// (created since, or recorded before rc.7o): the next re-approval freezes
// it. An approval since v1.4 (ScratchExcluded) left the gate's scratch
// out when it was recorded, so every file it holds is compared: judging a
// file now would let one edit drop a helper's #! line and its executable
// bit and change it unseen. An older one may hold scratch (D581), judged
// now (gateScratch, m's gate scanned for what it sources and declares) and
// skipped. A new file a recorded glob matches is new gate input, ignored
// or not.
func (p *Project) gateOwnDrift(m *Milestone, a *MilestoneApproval) []string {
	names := make([]string, 0, len(a.GateOwnFiles))
	for n := range a.GateOwnFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	scratch := map[string]bool{}
	if !a.ScratchExcluded && len(names) > 0 && m != nil {
		scratch = p.gateScratch(names, p.gateOwnScan(m).keep)
	}
	var out []string
	for _, n := range names {
		if scratch[n] {
			continue
		}
		switch is, exists := p.fileSHA256(n); {
		case !exists:
			out = append(out, n+" is gone")
		case is != a.GateOwnFiles[n]:
			out = append(out, n+" changed")
		}
	}
	seen := map[string]bool{}
	for _, g := range a.GateGlobs {
		for _, f := range globFiles(p, g) {
			_, own := a.GateOwnFiles[f]
			_, script := a.GateFiles[f]
			if !own && !script && !seen[f] {
				seen[f] = true
				out = append(out, f+" is new (it matches "+g+")")
			}
		}
	}
	return out
}

// gateOwnUnrecorded reports an own file, or a glob, of m's gate that a's
// record does not hold.
func (p *Project) gateOwnUnrecorded(m *Milestone, a *MilestoneApproval) bool {
	own := p.gateOwnScan(m)
	for n := range own.files {
		if _, ok := a.GateOwnFiles[n]; !ok {
			return true
		}
	}
	for g := range own.globs {
		if !slices.Contains(a.GateGlobs, g) {
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
	globOwner := map[string]string{}
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
			for _, g := range rec.Approval.GateGlobs {
				globOwner[g] = m.ID
			}
		}
	}
	if len(owner) == 0 && len(globOwner) == 0 {
		return nil
	}
	out, err := git(ctx, p.Root, "diff", "--name-only", "--no-renames", ci.State.BaseSHA, "HEAD")
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "cannot read the change's diff from its base_sha: " + err.Error()}
	}
	globs := make([]string, 0, len(globOwner))
	for g := range globOwner {
		globs = append(globs, g)
	}
	sort.Strings(globs)
	var touched, files []string
	for f := range strings.SplitSeq(out, "\n") {
		if m, ok := owner[f]; ok {
			touched = append(touched, f+" ("+m+"'s gate)")
			files = append(files, f)
			continue
		}
		// v1.2 F4: a file a recorded glob matches is the gate's input too
		// — a check list a change adds under scripts/checks/*.checks.
		for _, g := range globs {
			if ok, _ := path.Match(g, f); ok && f != "" {
				touched = append(touched, f+" ("+globOwner[g]+"'s gate: it matches "+g+")")
				files = append(files, f)
				break
			}
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
						" — if it is the gate's own check list, move it into " + gates + m.ID + "-<slug>/ and read it from there, or declare it in the script (`# ape: gate-files " + rel + "`), " + when + "; if it is a work product the gate checks, leave it"}))
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
