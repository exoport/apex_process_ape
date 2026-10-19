package apexv1

import (
	"path"
	"regexp"
	"slices"
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
// no other file — deleted or kept — has it; either where it is not a
// message's text (readsAt). Files and their reads by path.
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
		var frames []callFrame
		framed := false
		for _, tok := range pathTokenSpans(code) {
			r, byName := lf.readsDeleted(rel, tok.s, byBase)
			if r == "" {
				continue
			}
			if !framed {
				frames, framed = callFrames(code), true
			}
			if readsAt(code, tok, byName, frames) {
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
//
// R31, the same projects on rc.6c: so does a whole path — radix's
// `echo "(development/planning/data-architecture.md …"`, tenax's
// t.Fatalf("… development/…") — and a message string a call opens on one
// line and carries on the next. A path in a message's text reads nothing;
// one passed to a reading call or command on its line still reads.
var (
	// nameUseRe is a call or command that reads, lists or joins a path.
	nameUseRe = regexp.MustCompile(`(?i)\b\w*(?:open|read|load|glob|join|stat|exists?|isfile|path|dir|walk)\w*\s*\(` +
		`|(?:^|[\s;|&(` + "`" + `])(?:cat|find|ls|source|head|tail|grep|sed|awk|yq|jq|cp|mv|diff|wc|tee)\s` +
		`|\btest\s+-[a-z]\s|\[\[?\s+-[a-z]\s|\$\((?:wildcard|shell|file)\b|^\s*-?include\s`)
	// nameMsgRe is a command or call whose argument is a message: echo,
	// printf, a test's or a logger's message, an error's text.
	nameMsgRe = regexp.MustCompile(`(?:^|[\s;|&(` + "`" + `])(?:echo|printf)\b` +
		`|\.(?:Errorf|Fatalf|Logf|Log|Error|Fatal|Skipf|Skip)\s*\(` +
		`|\bfmt\.(?:Errorf|Printf|Println|Fprintf|Fprintln)\s*\(|\b(?:log|logging|console)\.\w+\s*\(|\bprint\s*\(`)
	// nameAssertRe is a testify call: its message argument is prose, its
	// path argument a path.
	nameAssertRe = regexp.MustCompile(`\b(?:require|assert)\.\w+\s*\(`)
	// redirectRe is a shell redirection right before the path: < reads
	// it, > and >> write it, and either needs its folder.
	redirectRe = regexp.MustCompile(`(?:^|[^<>=-])[<>]{1,2}\s*["']?$`)
	// echoWordRe is an echo or a printf command (not a printf( call).
	echoWordRe = regexp.MustCompile(`(?:^|[\s;|&(` + "`" + `])(echo|printf)\b\s*([^\s(]|$)`)
)

// readsAt reports the deleted path at tok in code as read, byName when
// tok is a base name alone. The nearer of a reading call or command
// (nameUseRe, a redirection) and a message (nameMsgRe) before it on its
// line decides. With neither, a call left open on an earlier line that
// tok sits in (frames) decides — the innermost reading call or message,
// so a message string carried onto the next line reads nothing. With
// none, a whole path reads; a base name reads only where its string is
// itself a path — the whole of a quoted string, or joined to a root
// ($ROOT/x.md). A testify call's argument is a message only when it is not
// such a string.
//
// R32, from tenax on rc.6d: before all of that, a path inside a sentence
// in a quoted string — quarantinedFlake(t, "… see development/deferred/.")
// — reads nothing, whatever call takes the string: ape cannot know a
// project's own helper is a message. A quoted path reads only where the
// string is the path (no whitespace in it), is joined to a root, or is
// itself a command that reads it (sh -c "cat development/…"). An
// unquoted path is unchanged.
func readsAt(code string, tok pathTok, byName bool, frames []callFrame) bool {
	lineStart := strings.LastIndexByte(code[:tok.at], '\n') + 1
	if lit, prose := literalAt(code, lineStart, tok); prose && !tok.joined {
		use := lastUseInString(lit)
		if redirectRe.MatchString(lit) {
			use = len(lit)
		}
		return use > lastMatch(nameMsgRe, lit)
	}
	before := code[lineStart:tok.at]
	pathShaped := tok.joined
	if tok.at > 0 && tok.end < len(code) {
		q := code[tok.at-1]
		pathShaped = pathShaped || strings.IndexByte("\"'`", q) >= 0 && code[tok.end] == q
	}
	last := func(re *regexp.Regexp) int { return lastMatch(re, before) }
	use, msg := last(nameUseRe), last(nameMsgRe)
	if redirectRe.MatchString(before) {
		use = len(before)
	}
	if !pathShaped {
		msg = max(msg, last(nameAssertRe))
	}
	switch {
	case use > msg:
		return true
	case msg >= 0:
		return false
	}
	for _, f := range slices.Backward(frames) {
		if f.open >= lineStart || f.close <= tok.at {
			continue
		}
		switch {
		case f.kind == callUse:
			return true
		case f.kind == callMsg, f.kind == callAssert && !pathShaped:
			return false
		}
	}
	return pathShaped || !byName
}

// lastMatch is where re's last match in s starts, -1 when none.
func lastMatch(re *regexp.Regexp, s string) int {
	m := re.FindAllStringIndex(s, -1)
	if len(m) == 0 {
		return -1
	}
	return m[len(m)-1][0]
}

// lastUseInString is lastMatch of nameUseRe in a quoted string's text,
// skipping a call shape with a space before its ( — a sentence's
// "under load (see …)" is no load( call.
func lastUseInString(lit string) int {
	at := -1
	for _, m := range nameUseRe.FindAllStringIndex(lit, -1) {
		if t := lit[m[0]:m[1]]; len(t) > 1 && t[len(t)-1] == '(' && (t[len(t)-2] == ' ' || t[len(t)-2] == '\t') {
			continue
		}
		at = m[0]
	}
	return at
}

// literalAt is the quoted string on its line that tok sits in (R32): lit
// its text from the opening quote up to tok, and prose when the whole
// string holds whitespace — a sentence, not a path. Line by line, as the
// rest of the class: a string opened on an earlier line is not one here
// (callFrames decides that case), and one left open runs to the line's
// end. A \ escapes the next byte except in a ` string.
func literalAt(code string, lineStart int, tok pathTok) (lit string, prose bool) {
	var q byte
	open := -1
	for i := lineStart; i < tok.at; i++ {
		switch c := code[i]; {
		case q == 0:
			if c == '"' || c == '\'' || c == '`' {
				q, open = c, i+1
			}
		case c == '\\' && q != '`':
			i++
		case c == q:
			q = 0
		}
	}
	if q == 0 {
		return "", false
	}
	end := tok.end
	for end < len(code) && code[end] != '\n' && code[end] != q {
		if code[end] == '\\' && q != '`' {
			end++
		}
		end++
	}
	end = min(end, len(code))
	return code[open:tok.at], strings.ContainsAny(code[open:end], " \t")
}

// The kinds of a callFrame.
const (
	callOther = iota
	callUse
	callMsg
	callAssert
)

// callFrame is a call's argument list, code[open:close] (close the end
// of code when it never closes), or, echo, an echo's words up to the end
// of its command; kind says what the call is (nameUseRe, nameMsgRe,
// nameAssertRe).
type callFrame struct {
	open, close, kind int
	echo              bool
}

// callFrames are the calls of code, outer before inner (by open), with
// what each is: what tells a path inside a message carried onto the next
// line (R31). A single pass of bytes: quoted text is skipped — a ` or a
// triple quote across lines, a " or a ' to its line's end unless an
// echo's string carries on — and an echo ends at its line's end (no
// trailing \), a ;, a | or a &.
func callFrames(code string) []callFrame {
	echoAt := map[int]bool{}
	for _, m := range echoWordRe.FindAllStringSubmatchIndex(code, -1) {
		echoAt[m[3]] = true // the end of the word
	}
	var frames []callFrame
	var stack []int // indexes into frames
	var quote string
	// pop closes the echoes open on top of the stack at i, or the call
	// on top of it.
	pop := func(i int, echo bool) {
		for len(stack) > 0 {
			f := &frames[stack[len(stack)-1]]
			if f.echo != echo {
				return
			}
			f.close = i
			stack = stack[:len(stack)-1]
			if !echo {
				return
			}
		}
	}
	inEcho := func() bool {
		return len(stack) > 0 && frames[stack[len(stack)-1]].echo
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if quote != "" {
			switch {
			case c == '\\' && quote != "`":
				i++
			case strings.HasPrefix(code[i:], quote):
				i += len(quote) - 1
				quote = ""
			case c == '\n' && (quote == `"` || quote == "'") && !inEcho():
				quote = ""
				pop(i, true)
			}
			continue
		}
		if echoAt[i] {
			frames = append(frames, callFrame{open: i, close: len(code), kind: callMsg, echo: true})
			stack = append(stack, len(frames)-1)
		}
		switch c {
		case '"', '\'':
			quote = string(c)
			if strings.HasPrefix(code[i:], strings.Repeat(quote, 3)) {
				quote = strings.Repeat(quote, 3)
				i += 2
			}
		case '`':
			quote = "`"
		case '\\':
			i++
		case '\n', ';', '|', '&':
			if c != '\n' || i == 0 || code[i-1] != '\\' {
				pop(i, true)
			}
		case '(':
			frames = append(frames, callFrame{open: i + 1, close: len(code), kind: callKind(code[max(strings.LastIndexByte(code[:i], '\n')+1, i-callKindWindow) : i+1])})
			stack = append(stack, len(frames)-1)
		case ')':
			pop(i, true)
			pop(i, false)
		}
	}
	return frames
}

// callKindWindow is how far before a ( callKind looks for the call's
// name, on its line.
const callKindWindow = 80

// callKind is what the call whose text ends in prefix's ( is.
func callKind(prefix string) int {
	ends := func(re *regexp.Regexp) bool {
		for _, m := range re.FindAllStringIndex(prefix, -1) {
			if m[1] == len(prefix) {
				return true
			}
		}
		return false
	}
	switch {
	case ends(nameMsgRe):
		return callMsg
	case ends(nameAssertRe):
		return callAssert
	case ends(nameUseRe):
		return callUse
	}
	return callOther
}

// pathTokens are the path-shaped words of code: each maximal run of path
// bytes with a / or a . in it. A $VAR/ or ${VAR}/ before a path is the
// root a script joins it to, and so is a ) or a quote before its /
// ($(ROOT)/development/…, root + "/development/…"): dropped.
func pathTokens(code string) []string {
	spans := pathTokenSpans(code)
	if len(spans) == 0 {
		return nil
	}
	out := make([]string, 0, len(spans))
	for _, t := range spans {
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
