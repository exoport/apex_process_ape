package apexv1

import (
	"path"
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

// LiftHashed is one edited file and what seems to hash it.
type LiftHashed struct {
	File string   `json:"file"`
	By   []string `json:"by"`
}

var (
	// hashWordRe is a file that computes or records a content hash.
	hashWordRe = regexp.MustCompile(`(?i)sha-?(?:1|224|256|384|512)\b|sha(?:1|256|512)\.|\bmd5\b|checksum|fingerprint|byte[-_ ]?identi(?:cal|ty)|bytes?[-_ ]?(?:unchanged|equal)`)
	// ciPathRe is where CI guards and their scripts live.
	ciPathRe = regexp.MustCompile(`(?:^|/)(?:\.github|\.gitlab|\.circleci|ci|scripts|tools)/|(?:^|/)(?:Makefile|[^/]*\.mk|\.gitlab-ci\.ya?ml|Jenkinsfile)$`)
	// testPathRe is a test file in the languages the lift tokenizes.
	testPathRe = regexp.MustCompile(`_test\.go$|(?:^|/)test_[^/]*\.py$|_test\.py$|\.(?:test|spec)\.[cm]?[jt]sx?$|_test\.dart$|(?:^|/)tests?/`)
)

// hashers are the kept text files that hash: lock files, and tests and
// CI files that mention a hash.
func (lf *lifter) hashers() []string {
	var out []string
	for _, rel := range lf.files {
		lock := isLockFile(rel)
		if !lock && !testPathRe.MatchString(rel) && !ciPathRe.MatchString(rel) {
			continue
		}
		if src, ok := lf.texts[rel]; ok && (lock || hashWordRe.MatchString(src)) {
			out = append(out, rel)
		}
	}
	return out
}

// hashed lists the edited files a hasher names near a hash — by path, or
// by a glob or quoted name of their folder — or, for a test beside them,
// by base name or a glob of their kind.
func (lf *lifter) hashed() []LiftHashed {
	hs := lf.hashers()
	if len(hs) == 0 {
		return nil
	}
	lines := map[string][]string{}
	for _, h := range hs {
		lines[h] = strings.Split(lf.texts[h], "\n")
	}
	var out []LiftHashed
	for _, c := range lf.res.Changed {
		f := c.File
		dir, base := path.Dir(f), path.Base(f)
		names := []string{f}
		if dir != "." {
			names = append(names, dir+"/*", `"`+dir+`"`, "'"+dir+"'", `"`+dir+`/"`)
		}
		var by []string
		for _, h := range hs {
			if h == f {
				continue
			}
			src := lf.texts[h]
			beside := path.Dir(h) == dir && testPathRe.MatchString(h) &&
				(strings.Contains(src, `"`+base+`"`) || strings.Contains(src, "*"+path.Ext(base)) || strings.Contains(src, "ReadDir("))
			if beside || nearHash(lines[h], names, isLockFile(h)) {
				by = append(by, h)
			}
		}
		if len(by) > 0 {
			out = append(out, LiftHashed{File: f, By: by})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// hashNear is how many lines around a path's mention a hash word may sit.
const hashNear = 3

// nearHash reports a line naming one of names within hashNear lines of a
// hash word — anywhere in a lock file, which is all hashes.
func nearHash(lines, names []string, lock bool) bool {
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
			if hashWordRe.MatchString(lines[j]) {
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
