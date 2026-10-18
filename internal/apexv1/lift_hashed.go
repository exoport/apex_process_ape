package apexv1

import (
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A byte-identity perimeter (L6): radix pins deploy and module files in a
// lock file of SHA-256s its CI checks, and tenax has tests that hash their
// own package's sources. The lift edits comments in those files, and the
// guard goes red after the lift. The dry run lists the edited files that a
// test, a CI script or a lock file seems to hash, so the person knows the
// perimeter to --exclude before lifting. It is a heuristic and says so: it
// finds the files a hashing file names, or that sit beside a hashing test
// that globs or names them; it cannot see a hash computed elsewhere.
//
// R1 (the rc.3 dry runs): radix listed 228 files against 2 real, most of
// them "hashed" by DR evidence documents that quote a sha256 beside the
// paths they record. Documents and evidence never hash anything, so they
// are no hashers; what a lock file names comes first, being the likeliest
// real perimeter; and the list is capped, its total counted.
//
// R6 (the rc.4 dry runs): the totals hardly fell — tenax 63 against 14
// real, 19 of them "hashed" by a test with "byte-identical" only in its
// prose comments and a ReadDir over its folder's .go files — and the cap
// pushed 4 real ones past 50, in a dry run whose "and 12 more (in the
// report)" named a file a dry run never writes. A hash word or call
// counts only outside comments; what a hasher names comes before what
// only its folder ties to one; and a dry run lists every one: only the
// result of a real lift, and its disposition file, keep the cap.

// LiftHashed is one edited file and what seems to hash it.
type LiftHashed struct {
	File string   `json:"file"`
	By   []string `json:"by"`
}

var (
	// hashWordRe is a file that computes or records a content hash: the
	// coreutils tools (sha256sum, md5sum, b2sum, shasum) count too — a
	// CI script hashing with them names no "sha256" on a word boundary.
	hashWordRe = regexp.MustCompile(`(?i)sha-?(?:1|224|256|384|512)\b|sha(?:1|256|512)\.|\bmd5\b|\b(?:sha(?:1|224|256|384|512)?|md5|b2)sum\b|checksum|fingerprint|byte[-_ ]?identi(?:cal|ty)|bytes?[-_ ]?(?:unchanged|equal)`)
	// hashCallRe is code that hashes, where no hash word need appear: a
	// Go hash package (sha256 itself is a hash word), Python's hashlib,
	// Node's createHash.
	hashCallRe = regexp.MustCompile(`\bcrypto/(?:md5|sha\w*|blake2\w*|hmac)\b|\bhashlib\b|\bcreateHash\(`)
	// ciPathRe is where CI guards and their scripts live.
	ciPathRe = regexp.MustCompile(`(?:^|/)(?:\.github|\.gitlab|\.circleci|ci|scripts|tools)/|(?:^|/)(?:Makefile|[^/]*\.mk|\.gitlab-ci\.ya?ml|Jenkinsfile)$`)
	// testPathRe is a test file in the languages the lift tokenizes.
	testPathRe = regexp.MustCompile(`_test\.go$|(?:^|/)test_[^/]*\.py$|_test\.py$|\.(?:test|spec)\.[cm]?[jt]sx?$|_test\.dart$|(?:^|/)tests?/`)
	// evidenceDirRe is a folder of evidence anywhere in the tree
	// (radix's tools/dr/evidence/).
	evidenceDirRe = regexp.MustCompile(`(?i)(?:^|/)evidence/`)
)

// hashedCap bounds edited_but_hashed in a real lift's result and in the
// disposition file; the report has every one, edited_but_hashed_total
// the count. A dry run, which writes no report, lists them all.
const hashedCap = 50

// isRecord reports a document or evidence file — prose, data dumps,
// logs, or anything under an evidence folder (the configured
// evidence_folder, or any folder named evidence): it may quote a hash
// beside a path, but it hashes nothing.
func (lf *lifter) isRecord(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".md", ".markdown", ".json", ".jsonl", ".log", ".txt":
		return true
	}
	if evidenceDirRe.MatchString(rel) {
		return true
	}
	ev := lf.evidenceRel()
	return ev != "" && strings.HasPrefix(rel, ev+"/")
}

// evidenceRel is the project's evidence_folder, relative to the root; ""
// when unset, outside the tree, or still holding a {variable}.
func (lf *lifter) evidenceRel() string {
	if lf.layout == nil || lf.layout.Cfg == nil {
		return ""
	}
	ev := strings.TrimSpace(lf.layout.Cfg.EvidenceFolder)
	if ev == "" || strings.Contains(ev, "{") {
		return ""
	}
	if filepath.IsAbs(ev) {
		return lf.rel(ev)
	}
	ev = path.Clean(filepath.ToSlash(ev))
	if ev == "." || strings.HasPrefix(ev, "..") {
		return ""
	}
	return ev
}

// hasher is a kept file that hashes: its lines, and the same lines with
// their comments blanked — where its hash words and calls count.
type hasher struct {
	rel         string
	lock        bool
	lines, code []string
}

// hashers are the kept text files that hash: lock files, and tests and
// CI files with a hash word or call outside their comments — never a
// document or evidence file. A lock file counts whatever its extension
// (numbering.lock.json is one), unless it sits in an evidence folder.
func (lf *lifter) hashers() []hasher {
	var out []hasher
	for _, rel := range lf.files {
		lock := isLockFile(rel)
		if lf.isRecord(rel) && (!lock || evidenceDirRe.MatchString(rel)) {
			continue
		}
		if !lock && !testPathRe.MatchString(rel) && !ciPathRe.MatchString(rel) {
			continue
		}
		src, ok := lf.texts[rel]
		if !ok {
			continue
		}
		code := codeText(rel, src)
		if lock || isHashy(code) {
			out = append(out, hasher{rel: rel, lock: lock, lines: strings.Split(src, "\n"), code: strings.Split(code, "\n")})
		}
	}
	return out
}

// How sure a hashed listing is, in its order (R6): a lock file names the
// file; a hasher names it — by path near a hash, or a test beside it by
// its quoted name; a hasher names only its folder, or a test beside it
// globs or lists the folder.
const (
	hashedByLock = iota
	hashedByName
	hashedByFolder
)

// hashed lists the edited files a hasher names near a hash — by path, or
// by a glob or quoted name of their folder — or, for a test beside them,
// by base name or a glob of their kind. The files a lock file names come
// first, then those a hasher names, then those only their folder ties to
// a hasher; each part by path.
func (lf *lifter) hashed() []LiftHashed {
	hs := lf.hashers()
	if len(hs) == 0 {
		return nil
	}
	var out []LiftHashed
	rank := map[string]int{}
	for _, c := range lf.res.Changed {
		f := c.File
		dir, base, ext := path.Dir(f), path.Base(f), path.Ext(f)
		var folder []string
		if dir != "." {
			folder = []string{dir + "/*", `"` + dir + `"`, "'" + dir + "'", `"` + dir + `/"`}
		}
		var by []string
		best := hashedByFolder
		for i := range hs {
			h := &hs[i]
			if h.rel == f {
				continue
			}
			if h.lock {
				if nearHash(h.lines, h.code, append([]string{f}, folder...), true) {
					by, best = append(by, h.rel), hashedByLock
				}
				continue
			}
			beside := path.Dir(h.rel) == dir && testPathRe.MatchString(h.rel)
			code := strings.Join(h.code, "\n")
			switch {
			case nearHash(h.lines, h.code, []string{f}, false) || beside && strings.Contains(code, `"`+base+`"`):
				best = min(best, hashedByName)
			case nearHash(h.lines, h.code, folder, false) || beside && (strings.Contains(code, "*"+ext) || strings.Contains(code, "ReadDir(")):
			default:
				continue
			}
			by = append(by, h.rel)
		}
		if len(by) > 0 {
			out = append(out, LiftHashed{File: f, By: by})
			rank[f] = best
		}
	}
	lf.hashedRank = rank
	sort.Slice(out, func(i, j int) bool {
		if a, b := rank[out[i].File], rank[out[j].File]; a != b {
			return a < b
		}
		return out[i].File < out[j].File
	})
	return out
}

// codeText is src with its comments blanked, newlines kept so its lines
// still line up: a hash word in prose ("byte-identical" in a test's doc
// comment) hashes nothing (R6). The lift's tokenizer finds the comments;
// in a file it cannot read, a line opening with # or // is one.
func codeText(rel, src string) string {
	if lang := liftLangOf(rel, []byte(src[:min(len(src), 256)])); lang != langNone {
		if code, err := blankComments(lang, src); err == nil {
			return code
		}
	}
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// blankComments is src with every comment lang's tokenizer finds blanked,
// newlines kept; an error is the tokenizer's.
func blankComments(lang liftLang, src string) (string, error) {
	segs, err := tokenize(lang, src)
	if err != nil {
		return "", err
	}
	b := []byte(src)
	for _, s := range segs {
		if s.Kind != segComment {
			continue
		}
		for i := s.Start; i < s.End; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	return string(b), nil
}

// isHashy reports a hash word or a hash call in s.
func isHashy(s string) bool { return hashWordRe.MatchString(s) || hashCallRe.MatchString(s) }

// hashNear is how many lines around a path's mention a hash word may sit.
const hashNear = 3

// nearHash reports a line naming one of names within hashNear lines of a
// hash word or call outside a comment (code is lines, comments blanked)
// — anywhere in a lock file, which is all hashes.
func nearHash(lines, code, names []string, lock bool) bool {
	for i, l := range lines {
		named := false
		for _, n := range names {
			if namesPath(l, n) {
				named = true
				break
			}
		}
		if !named {
			continue
		}
		if lock {
			return true
		}
		for j := max(0, i-hashNear); j <= min(len(lines)-1, i+hashNear); j++ {
			if isHashy(code[j]) {
				return true
			}
		}
	}
	return false
}

func isLockFile(rel string) bool {
	base := path.Base(rel)
	return strings.Contains(base, ".lock.") || strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, "-lock.yaml")
}

// namesPath reports p in src as a path of its own (./p included), not
// the tail of a longer one.
func namesPath(src, p string) bool {
	for i := 0; ; {
		j := strings.Index(src[i:], p)
		if j < 0 {
			return false
		}
		at := i + j
		if at == 0 || !isPathByte(src[at-1]) || at >= 2 && src[at-2:at] == "./" && (at == 2 || !isPathByte(src[at-3])) {
			return true
		}
		i = at + 1
	}
}

func isPathByte(c byte) bool {
	return c == '/' || c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
