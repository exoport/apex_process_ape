package apexv1

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// D141, from atomix and radix on rc.3–rc.5: code, tests and CI that read
// the v0 state the lift deletes go red once it is gone — atomix's
// TestKnownUnwiredList and TestAlertPackLevelsCarryEvidenceCitations read
// the tracker, radix's adr_release_identifier guard the ADR folder. The
// references-to-removed-paths summary counted them among every mention in
// a comment, so a reader was one line among a hundred. They are a class of
// their own: a kept code, test, script, Makefile or CI file — not a
// document, not evidence, not the v0 files themselves — that names a path
// the lift deletes outside its comments, with the paths it reads. Each is
// re-pointed to v1 state, or retired, in the first change after the plan.

// LiftV0Reader is one kept file that reads v0 state the lift deletes, and
// the deleted paths it names.
type LiftV0Reader struct {
	File  string   `json:"file"`
	Reads []string `json:"reads"`
}

// codeExtRe is a source file in a language the lift does not tokenize:
// a path string in it is code all the same.
var codeExtRe = regexp.MustCompile(`(?i)\.(?:rs|java|kt|kts|scala|groovy|gradle|rb|php|swift|cs|fs|ps1|psm1|lua|pl|pm|r|jl|ex|exs|erl|hs|ml|nim|zig|tf|hcl|nix|bat|cmd|fish)$`)

// codeNameRe is a build or CI file known by its name.
var codeNameRe = regexp.MustCompile(`(?:^|/)(?:Dockerfile[^/]*|Containerfile|Jenkinsfile|[Jj]ustfile|Taskfile\.ya?ml|Rakefile|Vagrantfile|Earthfile)$`)

// isCodeFile reports a code, test, script, Makefile or CI file: one the
// lift tokenizes, one under a CI or test path, a source file of another
// language, or a build file known by its name.
func isCodeFile(rel, src string) bool {
	return liftLangOf(rel, []byte(src[:min(len(src), 256)])) != langNone ||
		ciPathRe.MatchString(rel) || testPathRe.MatchString(rel) || codeExtRe.MatchString(rel) || codeNameRe.MatchString(rel)
}

// readerCandidate reports a kept file the reader class looks in: code
// (isCodeFile), outside the development folder and docs/, not a record or
// evidence file, and not the install's ledger.
func (lf *lifter) readerCandidate(rel, src string) bool {
	if rel == frameworkYAML || strings.HasPrefix(rel, lf.devRel+"/") || strings.HasPrefix(rel, "docs/") || lf.isRecord(rel) {
		return false
	}
	return isCodeFile(rel, src)
}

// deletedFiles are the tracked files under the paths the lift deletes,
// the ADR records it keeps aside.
func (lf *lifter) deletedFiles() []string {
	if len(lf.deletes) == 0 {
		return nil
	}
	out, err := gitRaw(lf.ctx, lf.root, append([]string{"ls-files", "-z", "--"}, lf.deletes...)...)
	if err != nil {
		return nil
	}
	var files []string
	for f := range strings.SplitSeq(out, "\x00") {
		if f != "" && lf.isDeleted(f) {
			files = append(files, f)
		}
	}
	return files
}

// v0Readers lists the kept code files that name a path the lift deletes
// outside their comments (codeText): by its repo-relative path (./ and
// ../ aside, or relative to the file's folder), a path under a deleted
// folder, or the base name of a deleted file when it has an extension and
// no other file — deleted or kept — has it. Files and their reads by path.
func (lf *lifter) v0Readers() []LiftV0Reader {
	deleted := lf.deletedFiles()
	if len(deleted) == 0 && len(lf.untracked) == 0 {
		return nil
	}
	// A base name names one deleted file only when it is unique: among
	// the deleted files, and not also a kept file's (a test reading its
	// own README.md reads no ADR folder's).
	byBase := map[string]string{}
	seen := map[string]int{}
	for _, f := range deleted {
		b := path.Base(f)
		seen[b]++
		byBase[b] = f
	}
	for _, f := range lf.files {
		seen[path.Base(f)]++
	}
	for b := range byBase {
		if seen[b] > 1 || path.Ext(b) == "" || path.Ext(b) == b {
			delete(byBase, b)
		}
	}
	var out []LiftV0Reader
	for _, rel := range lf.files {
		src, _ := lf.current(rel)
		if !lf.readerCandidate(rel, src) {
			continue
		}
		reads := map[string]bool{}
		for _, tok := range pathTokens(codeText(rel, src)) {
			if r := lf.readsDeleted(rel, tok, byBase); r != "" {
				reads[r] = true
			}
		}
		if len(reads) == 0 {
			continue
		}
		rs := make([]string, 0, len(reads))
		for r := range reads {
			rs = append(rs, r)
		}
		sort.Strings(rs)
		out = append(out, LiftV0Reader{File: rel, Reads: rs})
	}
	return out
}

// readsDeleted is the deleted path tok names in rel, "" when none.
func (lf *lifter) readsDeleted(rel, tok string, byBase map[string]string) string {
	tok = strings.TrimRight(tok, ".")
	tok = strings.TrimSuffix(tok, "/")
	if tok == "" {
		return ""
	}
	var tries []string
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") {
		// Relative to the file's folder (a test runs there), else to the
		// root with the leading ./ and ../ dropped.
		if j := path.Join(path.Dir(rel), tok); !strings.HasPrefix(j, "..") {
			tries = append(tries, j)
		}
		t := tok
		for strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") {
			t = t[strings.IndexByte(t, '/')+1:]
		}
		tries = append(tries, t)
	} else {
		tries = append(tries, tok)
	}
	for _, t := range tries {
		if t == "" || t == "." {
			continue
		}
		if lf.isDeleted(t) {
			return t
		}
		for _, u := range lf.untracked {
			if t == u || strings.HasPrefix(t, u+"/") {
				return t
			}
		}
		if f, ok := byBase[t]; ok {
			return f
		}
	}
	return ""
}

// pathTokens are the path-shaped words of code: each maximal run of path
// bytes with a / or a . in it. A $VAR/ or ${VAR}/ before a path is the
// root a script joins it to, and so is a ) or a quote before its /
// ($(ROOT)/development/…, root + "/development/…"): dropped.
func pathTokens(code string) []string {
	var out []string
	for i := 0; i < len(code); {
		if !isPathByte(code[i]) {
			i++
			continue
		}
		j := i
		for j < len(code) && isPathByte(code[j]) {
			j++
		}
		tok := code[i:j]
		var prev byte
		if i > 0 {
			prev = code[i-1]
		}
		switch {
		case prev == '$':
			// $ROOT/development/…: the variable is the first segment.
			if k := strings.IndexByte(tok, '/'); k >= 0 {
				tok = tok[k+1:]
			} else {
				tok = ""
			}
		case strings.HasPrefix(tok, "/") && strings.IndexByte("})\"'`", prev) >= 0:
			tok = strings.TrimLeft(tok, "/")
		}
		if strings.ContainsAny(tok, "/.") {
			out = append(out, tok)
		}
		i = j
	}
	return out
}
