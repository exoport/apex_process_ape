// Package apexdoc shards a Markdown document into section files and
// assembles them back, and surveys a set of source documents.
//
// It replaces `shard-doc.py` and `analyze_sources.py`. Neither carried the
// PyYAML hazard that justified the other retirements — both are
// stdlib-only — so the case here is consolidation and testability: one
// implementation, and a Go test suite that runs in the same `make test` as
// everything else instead of a pytest invocation nobody remembers.
//
// The compatibility surface that matters is the SLUG. A slug differing by
// one character produces a different filename, and `assemble` then cannot
// find its own shards — so slugify is reproduced exactly, with a golden.
package apexdoc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// IndexFileName is the listing `shard` always writes.
//
// This is a CONTRACT, not a convenience: apex-shard-doc/SKILL.md:111
// states that explode "always produces an index.md that lists and links
// all generated section files; its absence indicates the command did not
// complete successfully", and the skill's Step 4 verifies it. A
// replacement that split and rewrote links perfectly but omitted index.md
// would pass a round-trip test and then fail its real caller.
const IndexFileName = "index.md"

// sectionsHeading is the index's machine-read marker. `assemble` locates
// the file list by it, so it is part of the format rather than decoration.
const sectionsHeading = "## Sections"

// DefaultLevel is the heading depth sections are split at.
const DefaultLevel = 2

var (
	nonWordRe   = regexp.MustCompile(`[^\w\s-]`)
	spaceRe     = regexp.MustCompile(`[\s_]+`)
	multiDashRe = regexp.MustCompile(`-+`)

	// mdLinkRe matches `[text](url)` and `![alt](url)`, tolerating one
	// level of nested brackets in the label.
	mdLinkRe = regexp.MustCompile(`(!?\[(?:[^\[\]]|\[[^\]]*\])*\])\(([^)]+)\)`)
	// mdRefRe matches a reference definition at the start of a line.
	mdRefRe = regexp.MustCompile(`(?m)^(\s*\[[^\]]+\]:\s+)(\S+)(.*)`)
	// htmlSrcRe matches src="url", src='url' and bare src=url.
	htmlSrcRe = regexp.MustCompile(`(?i)(src=)(["']?)([^"'>\s]+)(["']?)`)
	// uriSchemeRe matches any URI scheme prefix.
	uriSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+\-.]*:`)
	// sectionEntryRe parses `- [Title](file.md)` out of the index.
	sectionEntryRe = regexp.MustCompile(`(?m)^\s*-\s+\[.*?\]\(([^)]+)\)`)
)

// Slugify converts heading text to a filename component, reproducing
// shard-doc.py's slugify exactly: lowercase, drop non-word characters
// except hyphens, collapse whitespace and underscores to hyphens, collapse
// runs of hyphens, trim them, and fall back to "section".
func Slugify(text string) string {
	s := strings.ToLower(text)
	s = nonWordRe.ReplaceAllString(s, "")
	s = spaceRe.ReplaceAllString(s, "-")
	s = multiDashRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "section"
	}
	return s
}

// Duplicate is one pair of headings that slugify to the same value.
type Duplicate struct {
	Slug       string `json:"slug"        yaml:"slug"`
	First      string `json:"first"       yaml:"first"`
	FirstLine  int    `json:"first_line"  yaml:"first_line"`
	Second     string `json:"second"      yaml:"second"`
	SecondLine int    `json:"second_line" yaml:"second_line"`
}

// VerifyResult is the payload of `ape doc verify`.
type VerifyResult struct {
	File       string      `json:"file"       yaml:"file"`
	Level      int         `json:"level"      yaml:"level"`
	Headings   int         `json:"headings"   yaml:"headings"`
	Duplicates []Duplicate `json:"duplicates" yaml:"duplicates"`
}

// OK reports whether the document is safe to shard.
func (r *VerifyResult) OK() bool { return len(r.Duplicates) == 0 }

// Verify scans a document for duplicate heading slugs at the given level.
//
// This is a GATE, not a report: its caller relies on a non-zero exit to
// stop before writing anything. Downstream skills reference shard files by
// exact slug, so a deduplicated `foo-2.md` sibling would silently break
// them — the source document has to be fixed instead.
func Verify(path string, level int) (*VerifyResult, error) {
	if level <= 0 {
		level = DefaultLevel
	}
	data, err := os.ReadFile(path) //nolint:gosec // the operator names the document
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	res := &VerifyResult{File: path, Level: level}
	prefix := strings.Repeat("#", level) + " "
	type seen struct {
		text string
		line int
	}
	first := map[string]seen{}
	for i, line := range strings.Split(string(data), "\n") {
		stripped := strings.TrimRight(line, "\r")
		if !strings.HasPrefix(stripped, prefix) {
			continue
		}
		res.Headings++
		text := strings.TrimSpace(stripped[len(prefix):])
		slug := Slugify(text)
		if prev, ok := first[slug]; ok {
			res.Duplicates = append(res.Duplicates, Duplicate{
				Slug: slug, First: prev.text, FirstLine: prev.line,
				Second: text, SecondLine: i + 1,
			})
			continue
		}
		first[slug] = seen{text: text, line: i + 1}
	}
	return res, nil
}

// Section is one shard.
type Section struct {
	Heading  string `json:"heading"  yaml:"heading"`
	FileName string `json:"file"     yaml:"file"`
}

// ShardResult is the payload of `ape doc shard`.
type ShardResult struct {
	Source   string    `json:"source"   yaml:"source"`
	Dir      string    `json:"dir"      yaml:"dir"`
	Level    int       `json:"level"    yaml:"level"`
	Sections []Section `json:"sections" yaml:"sections"`
	Index    string    `json:"index"    yaml:"index"`
	// WholeDocument records the no-headings case: the entire document was
	// written as index.md rather than split.
	WholeDocument bool `json:"whole_document" yaml:"whole_document"`
}

// ShardOptions controls the split.
type ShardOptions struct {
	Level int
	// Numbered prefixes each filename with a zero-padded ordinal.
	Numbered bool
}

// Shard splits a document at the given heading level, rewrites relative
// links for the extra directory depth, and writes the index.
func Shard(source, dir string, opts ShardOptions) (*ShardResult, error) {
	level := opts.Level
	if level <= 0 {
		level = DefaultLevel
	}
	data, err := os.ReadFile(source) //nolint:gosec // the operator names the document
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", source, err)
	}
	res := &ShardResult{Source: source, Dir: dir, Level: level}

	preamble, sections := splitSections(string(data), level)
	if len(sections) == 0 {
		// No headings at that level: the whole document becomes index.md,
		// so `assemble` still has something to work from.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		res.Index = filepath.Join(dir, IndexFileName)
		res.WholeDocument = true
		if err := os.WriteFile(res.Index, data, 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
			return nil, fmt.Errorf("write %s: %w", res.Index, err)
		}
		return res, nil
	}

	// Refuse on duplicate slugs rather than writing `foo-2.md` siblings
	// that hardcoded consumers cannot distinguish.
	verify, err := Verify(source, level)
	if err != nil {
		return nil, err
	}
	if !verify.OK() {
		return nil, &DuplicateSlugError{File: source, Level: level, Duplicates: verify.Duplicates}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	generated := map[string]bool{}
	for i, sec := range sections {
		slug := Slugify(sec.title)
		name := slug + ".md"
		if opts.Numbered {
			name = fmt.Sprintf("%02d-%s.md", i+1, slug)
		}
		body := rewriteForward(sec.heading+"\n"+sec.body, nil)
		body = strings.TrimRight(body, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
		res.Sections = append(res.Sections, Section{Heading: sec.title, FileName: name})
		generated[name] = true
	}

	index := buildIndex(preamble, res.Sections, generated)
	res.Index = filepath.Join(dir, IndexFileName)
	if err := os.WriteFile(res.Index, []byte(index), 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
		return nil, fmt.Errorf("write %s: %w", res.Index, err)
	}
	return res, nil
}

// DuplicateSlugError refuses a shard that would produce indistinguishable
// filenames.
type DuplicateSlugError struct {
	File       string
	Level      int
	Duplicates []Duplicate
}

func (e *DuplicateSlugError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "duplicate H%d slugs in %s — refusing to shard, because consumers reference "+
		"shard files by exact slug and cannot distinguish a deduplicated sibling. Fix the source first:",
		e.Level, e.File)
	for _, d := range e.Duplicates {
		fmt.Fprintf(&b, "\n  - slug %q: %q (line %d) and %q (line %d)",
			d.Slug, d.First, d.FirstLine, d.Second, d.SecondLine)
	}
	return b.String()
}

type rawSection struct {
	heading string
	title   string
	body    string
}

// splitSections divides a document into the preamble above the first
// heading at level, and one section per heading.
func splitSections(content string, level int) (preamble string, sections []rawSection) {
	prefix := strings.Repeat("#", level) + " "
	lines := strings.SplitAfter(content, "\n")

	var (
		pre     strings.Builder
		body    strings.Builder
		heading string
		found   bool
	)
	flush := func() {
		if !found {
			return
		}
		title := strings.TrimSpace(strings.TrimRight(heading, "\r")[len(prefix):])
		sections = append(sections, rawSection{
			heading: strings.TrimRight(heading, "\n\r"),
			title:   title,
			body:    body.String(),
		})
		body.Reset()
	}
	for _, line := range lines {
		stripped := strings.TrimRight(line, "\n\r")
		if strings.HasPrefix(stripped, prefix) {
			if !found {
				preamble = pre.String()
				found = true
			} else {
				flush()
			}
			heading = line
			continue
		}
		if found {
			body.WriteString(line)
		} else {
			pre.WriteString(line)
		}
	}
	flush()
	if !found {
		preamble = pre.String()
	}
	return preamble, sections
}

// buildIndex renders index.md: the preamble, then the section listing.
func buildIndex(preamble string, sections []Section, generated map[string]bool) string {
	rewritten := rewriteForward(preamble, generated)
	var parts []string
	if strings.TrimSpace(rewritten) != "" {
		parts = append(parts, strings.TrimRight(rewritten, "\n"))
	}
	var list strings.Builder
	for i, sec := range sections {
		if i > 0 {
			list.WriteString("\n")
		}
		fmt.Fprintf(&list, "- [%s](%s)", sec.Heading, sec.FileName)
	}
	parts = append(parts, sectionsHeading+"\n\n"+list.String())
	return strings.Join(parts, "\n\n") + "\n"
}

// AssembleResult is the payload of `ape doc assemble`.
type AssembleResult struct {
	Dir      string   `json:"dir"                yaml:"dir"`
	Output   string   `json:"output"             yaml:"output"`
	Sections int      `json:"sections"           yaml:"sections"`
	Missing  []string `json:"missing,omitempty"  yaml:"missing,omitempty"`
}

// Assemble concatenates the shards back into one document, reversing the
// link rewrite. index.md is read for the ordering and its preamble, and is
// never itself treated as a shard.
func Assemble(dir, output string) (*AssembleResult, error) {
	indexPath := filepath.Join(dir, IndexFileName)
	indexData, err := os.ReadFile(indexPath) //nolint:gosec // path derived from the operator's directory
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", indexPath, err)
	}
	res := &AssembleResult{Dir: dir, Output: output}

	preamble, list := splitIndex(string(indexData))
	parent := filepath.Dir(strings.TrimRight(dir, string(filepath.Separator)))

	var parts []string
	if strings.TrimSpace(preamble) != "" {
		parts = append(parts, rewriteBackward(strings.TrimRight(preamble, "\n"), parent))
	}
	for _, name := range list {
		body, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))) //nolint:gosec // name comes from the index this directory owns
		if readErr != nil {
			res.Missing = append(res.Missing, name)
			continue
		}
		parts = append(parts, rewriteBackward(strings.TrimRight(string(body), "\n"), parent))
		res.Sections++
	}

	assembled := strings.Join(parts, "\n\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(output), err)
	}
	if err := os.WriteFile(output, []byte(assembled), 0o644); err != nil { //nolint:gosec // documentation is world-readable by design
		return nil, fmt.Errorf("write %s: %w", output, err)
	}
	return res, nil
}

// splitIndex separates the index's preamble from its section list.
func splitIndex(content string) (preamble string, files []string) {
	idx := strings.Index(content, sectionsHeading)
	if idx < 0 {
		return strings.TrimRight(content, "\n"), nil
	}
	preamble = strings.TrimRight(content[:idx], "\n")
	for _, m := range sectionEntryRe.FindAllStringSubmatch(content[idx:], -1) {
		files = append(files, m[1])
	}
	return preamble, files
}

// isRelative reports whether a URL needs depth adjustment: not an anchor,
// not absolute, not already parent-relative, and not carrying a scheme.
func isRelative(url string) bool {
	if url == "" {
		return false
	}
	path := strings.Trim(strings.Fields(url)[0], `"'`)
	switch {
	case path == "",
		strings.HasPrefix(path, "#"),
		strings.HasPrefix(path, "//"),
		strings.HasPrefix(path, "/"),
		strings.HasPrefix(path, "../"),
		uriSchemeRe.MatchString(path):
		return false
	}
	return true
}

// normalisePath strips an explicit ./ prefix before depth adjustment.
func normalisePath(path string) string {
	return strings.TrimPrefix(path, "./")
}

// prependParent adds ../ to a URL, preserving any title suffix.
func prependParent(url string) string {
	parts := strings.SplitN(url, " ", 2)
	out := "../" + normalisePath(parts[0])
	if len(parts) > 1 {
		out += " " + parts[1]
	}
	return out
}

// rewriteForward prepends ../ to relative links, because a shard sits one
// directory deeper than the document it came from. skip leaves links to
// generated sibling files alone.
func rewriteForward(content string, skip map[string]bool) string {
	content = mdLinkRe.ReplaceAllStringFunc(content, func(m string) string {
		label, url, ok := splitMDLink(m)
		if !ok {
			return m
		}
		if skip[strings.Fields(url)[0]] {
			return m
		}
		if isRelative(url) {
			url = prependParent(url)
		}
		return label + "(" + url + ")"
	})
	content = mdRefRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := mdRefRe.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		url := sub[2]
		if skip[url] {
			return m
		}
		if isRelative(url) {
			url = "../" + normalisePath(url)
		}
		return sub[1] + url + sub[3]
	})
	return htmlSrcRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := htmlSrcRe.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		url := sub[3]
		if skip[url] {
			return m
		}
		if isRelative(url) {
			url = "../" + normalisePath(url)
		}
		return sub[1] + sub[2] + url + sub[4]
	})
}

// rewriteBackward strips ../ from links whose target actually exists in
// parent. The existence test is what lets a `../` link that predated the
// shard survive unchanged.
func rewriteBackward(content, parent string) string {
	strip := func(path string) string {
		if !strings.HasPrefix(path, "../") {
			return path
		}
		candidate := path[3:]
		// The existence test has to ignore a #fragment or ?query, or a link
		// like `../architecture.md#goals` never resolves and keeps its ../
		// forever — which breaks the round trip for exactly the links a
		// sharded document uses most. shard-doc.py stats the whole string
		// and so has this gap; a replacement that round-trips more
		// faithfully is strictly better, and changes nothing about the
		// shard files themselves.
		target := candidate
		if i := strings.IndexAny(target, "#?"); i >= 0 {
			target = target[:i]
		}
		if target == "" {
			return path
		}
		if _, err := os.Stat(filepath.Join(parent, filepath.FromSlash(target))); err == nil {
			return candidate
		}
		return path
	}
	content = mdLinkRe.ReplaceAllStringFunc(content, func(m string) string {
		label, url, ok := splitMDLink(m)
		if !ok {
			return m
		}
		parts := strings.SplitN(url, " ", 2)
		out := strip(parts[0])
		if len(parts) > 1 {
			out += " " + parts[1]
		}
		return label + "(" + out + ")"
	})
	content = mdRefRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := mdRefRe.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		return sub[1] + strip(sub[2]) + sub[3]
	})
	return htmlSrcRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := htmlSrcRe.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		return sub[1] + sub[2] + strip(sub[3]) + sub[4]
	})
}

// splitMDLink pulls the label and url out of a matched inline link.
func splitMDLink(m string) (label, url string, ok bool) {
	sub := mdLinkRe.FindStringSubmatch(m)
	if sub == nil {
		return "", "", false
	}
	return sub[1], sub[2], true
}

// SortedSectionNames is a helper for callers that want a stable listing.
func SortedSectionNames(sections []Section) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		out = append(out, s.FileName)
	}
	sort.Strings(out)
	return out
}
