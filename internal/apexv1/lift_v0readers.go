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
// no other file — deleted or kept — has it, where the line reads it
// (readsByName). Files and their reads by path.
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
		code := codeText(rel, src)
		for _, tok := range pathTokenSpans(code) {
			if r, byName := lf.readsDeleted(rel, tok.s, byBase); r != "" && (!byName || readsByName(code, tok)) {
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

// readsDeleted is the deleted path tok names in rel, "" when none, and
// whether it names it by its base name alone.
func (lf *lifter) readsDeleted(rel, tok string, byBase map[string]string) (string, bool) {
	tok = strings.TrimRight(tok, ".")
	tok = strings.TrimSuffix(tok, "/")
	if tok == "" {
		return "", false
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
			return t, false
		}
		for _, u := range lf.untracked {
			if t == u || strings.HasPrefix(t, u+"/") {
				return t, false
			}
		}
		if f, ok := byBase[t]; ok {
			return f, true
		}
	}
	return "", false
}

// R25, from mdpdf, radix, tenax and cortex on rc.6: a base name alone is
// a word in prose as often as a path — `echo "… team-memory.md …"`,
// t.Errorf("… (project-context.md stable exit-code map)") read nothing.
// It reads a file only where its line is shaped for it. Line by line, as
// the rest of the class: a weird case is missed rather than parsed.
var (
	// nameUseRe is a call or command that reads, lists or joins a path.
	nameUseRe = regexp.MustCompile(`(?i)\b\w*(?:open|read|load|glob|join|stat|exists?|isfile|path)\w*\s*\(` +
		`|(?:^|[\s;|&(` + "`" + `])(?:cat|find|ls|source|head|tail|grep|sed|awk|yq|jq|cp|mv|diff|wc)\s` +
		`|\btest\s+-[a-z]\s|\[\[?\s+-[a-z]\s|\$\((?:wildcard|shell|file)\b|^\s*-?include\s`)
	// nameMsgRe is a command or call whose argument is a message: echo,
	// printf, a test's or a logger's message, an error's text.
	nameMsgRe = regexp.MustCompile(`(?:^|[\s;|&(` + "`" + `])(?:echo|printf)\b` +
		`|\.(?:Errorf|Fatalf|Logf|Log|Error|Fatal|Skipf|Skip)\s*\(` +
		`|\bfmt\.(?:Errorf|Printf|Println|Fprintf|Fprintln)\s*\(|\b(?:log|logging|console)\.\w+\s*\(|\bprint\s*\(`)
	// nameAssertRe is a testify call: its message argument is prose, its
	// path argument a path.
	nameAssertRe = regexp.MustCompile(`\b(?:require|assert)\.\w+\s*\(`)
)

// readsByName reports a deleted file's base name at tok in code as read:
// the nearer of a reading call or command (nameUseRe) and a message
// (nameMsgRe) before it on its line decides; with neither, a string that
// is itself a path — the whole of a quoted string, or joined to a root
// ($ROOT/x.md) — reads it. A testify call's argument is a message only
// when it is not such a string.
func readsByName(code string, tok pathTok) bool {
	before := code[strings.LastIndexByte(code[:tok.at], '\n')+1 : tok.at]
	pathShaped := tok.joined
	if tok.at > 0 && tok.end < len(code) {
		q := code[tok.at-1]
		pathShaped = pathShaped || strings.IndexByte("\"'`", q) >= 0 && code[tok.end] == q
	}
	last := func(re *regexp.Regexp) int {
		m := re.FindAllStringIndex(before, -1)
		if len(m) == 0 {
			return -1
		}
		return m[len(m)-1][0]
	}
	use, msg := last(nameUseRe), last(nameMsgRe)
	if !pathShaped {
		msg = max(msg, last(nameAssertRe))
	}
	switch {
	case use > msg:
		return true
	case msg >= 0:
		return false
	}
	return pathShaped
}

// pathTokens are the path-shaped words of code: each maximal run of path
// bytes with a / or a . in it. A $VAR/ or ${VAR}/ before a path is the
// root a script joins it to, and so is a ) or a quote before its /
// ($(ROOT)/development/…, root + "/development/…"): dropped.
func pathTokens(code string) []string {
	var out []string
	for _, t := range pathTokenSpans(code) {
		out = append(out, t.s)
	}
	return out
}

// pathTok is one of pathTokens: s in code[at:end], joined when a root
// before it was dropped.
type pathTok struct {
	s       string
	at, end int
	joined  bool
}

// pathTokenSpans is pathTokens with where each one is.
func pathTokenSpans(code string) []pathTok {
	var out []pathTok
	for i := 0; i < len(code); {
		if !isPathByte(code[i]) {
			i++
			continue
		}
		j := i
		for j < len(code) && isPathByte(code[j]) {
			j++
		}
		tok, joined := code[i:j], false
		var prev byte
		if i > 0 {
			prev = code[i-1]
		}
		switch {
		case prev == '$':
			// $ROOT/development/…: the variable is the first segment.
			if k := strings.IndexByte(tok, '/'); k >= 0 {
				tok, joined = tok[k+1:], true
			} else {
				tok = ""
			}
		case strings.HasPrefix(tok, "/") && strings.IndexByte("})\"'`", prev) >= 0:
			tok, joined = strings.TrimLeft(tok, "/"), true
		}
		if strings.ContainsAny(tok, "/.") {
			out = append(out, pathTok{s: tok, at: i, end: j, joined: joined})
		}
		i = j
	}
	return out
}
