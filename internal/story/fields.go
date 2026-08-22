// Package story projects and verifies story frontmatter.
//
// Two operations, both bounded. `Fields` answers "what do these 465
// stories say in four frontmatter keys" by reading at most 8 KiB per file
// and never opening a body — the alternative, which
// `apex-feature-refresh` currently instructs, is "load each story file
// completely": 23.5 MB and 5.9M tokens against a 1M ceiling, to read four
// fields totalling 186 KB. `Verify` asserts three classes of invariant
// over the same frontmatter, and nothing beyond them.
package story

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// FrontmatterCap bounds the read per file. Story frontmatter never
// approaches 8 KiB; the cap is what turns a 25 MB corpus scan into a
// 67 KB one. A file whose block is not closed within the cap is reported,
// never silently treated as if the read had been complete.
const FrontmatterCap = 8 << 10

// IDKey is the frontmatter key that makes a Markdown file a story. It is
// also the discriminator that lets the deferred-work store live anywhere
// without poisoning story discovery.
const IDKey = "story_id"

// Fields is one story's projection: its path plus the selected keys.
type Fields struct {
	// Path is relative to the implementation folder, so output does not
	// depend on where the project is checked out.
	Path    string         `json:"path"     yaml:"path"`
	StoryID string         `json:"story_id" yaml:"story_id"`
	Values  map[string]any `json:"values"   yaml:"values"`
}

// FieldsResult is the payload of `ape story fields`.
type FieldsResult struct {
	Stories []Fields `json:"stories" yaml:"stories"`
	// Warnings names files that could not be projected — one line each,
	// never a silent skip.
	Warnings []string      `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Trailer  FieldsTrailer `json:"trailer"            yaml:"trailer"`
}

// FieldsTrailer is the accounting a caller needs to trust the answer:
// "field absent everywhere" and "field never looked for" are different
// results, and only the trailer distinguishes them.
type FieldsTrailer struct {
	FilesScanned    int            `json:"files_scanned"           yaml:"files_scanned"`
	StoriesMatched  int            `json:"stories_matched"         yaml:"stories_matched"`
	Fields          []string       `json:"fields"                  yaml:"fields"`
	PerFieldPresent map[string]int `json:"per_field_present_count" yaml:"per_field_present_count"`
	// BytesRead is what the cap bought, and the number the mechanism test
	// asserts against files_scanned × 8 KiB.
	BytesRead int `json:"bytes_read" yaml:"bytes_read"`
}

// ParseSelect splits and validates the --select list.
func ParseSelect(arg string) ([]string, error) {
	out := make([]string, 0, strings.Count(arg, ",")+1)
	seen := map[string]bool{}
	for part := range strings.SplitSeq(arg, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, errors.New("--select needs at least one frontmatter key")
	}
	return out, nil
}

// Head is a story file's frontmatter, parsed once and reused by both
// Fields and Verify.
type Head struct {
	// Path is relative to the scan root.
	Path string
	Abs  string
	// Raw is the decoded frontmatter mapping. nil when Err is set.
	Raw map[string]any
	// Err is why this file could not be projected: no frontmatter, invalid
	// YAML, or a block that does not close within FrontmatterCap.
	Err error
	// BytesRead is what it cost to find out.
	BytesRead int
}

// IsStory reports whether the file claims a story_id.
func (h Head) IsStory() bool {
	if h.Err != nil || h.Raw == nil {
		return false
	}
	v, ok := h.Raw[IDKey]
	return ok && v != nil && fmt.Sprintf("%v", v) != ""
}

// StoryID returns the claimed id as a string.
func (h Head) StoryID() string {
	if h.Raw == nil {
		return ""
	}
	if v, ok := h.Raw[IDKey]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// ScanHeads walks root for `.md` files and reads each one's frontmatter,
// capped. Non-story files are returned too — the caller decides — because
// "how many files did you look at" is part of the trailer.
func ScanHeads(root string) ([]Head, error) {
	var heads []Head
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip the derived output tree and anything hidden: a scan of
			// story frontmatter has no business in `_output/` or `.git/`.
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "_output") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		heads = append(heads, readHead(path, filepath.ToSlash(rel)))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i].Path < heads[j].Path })
	return heads, nil
}

// chunkSize is how much readHead pulls per syscall while hunting for the
// closing delimiter. Story frontmatter is typically a few hundred bytes,
// so one chunk almost always suffices — reading the full FrontmatterCap
// every time would cost 8 KiB per file whether it needed it or not, which
// is the difference between a 377x reduction and a 5x one.
const chunkSize = 1 << 10

// readHead reads only as far as the closing `---`, and never past
// FrontmatterCap.
func readHead(abs, rel string) Head {
	h := Head{Path: rel, Abs: abs}
	f, err := os.Open(abs)
	if err != nil {
		h.Err = err
		return h
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 0, chunkSize)
	chunk := make([]byte, chunkSize)
	var (
		fm       []byte
		gotBlock bool
		hitEOF   bool
	)
	for len(buf) < FrontmatterCap && !hitEOF {
		n, readErr := f.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			h.BytesRead += n
		}
		if readErr != nil {
			hitEOF = true
		}
		// Only the delimiter search needs re-running per chunk; a
		// successful split ends the read immediately.
		var splitErr error
		fm, _, splitErr = frontmatter.Split(buf)
		if splitErr == nil {
			gotBlock = true
			break
		}
		// A document that does not even OPEN with a delimiter can be
		// rejected after the first chunk — no point reading 8 KiB of a
		// retrospective to learn it is not a story.
		if len(buf) > len(delimiterProbe) && !opensWithDelimiter(buf) {
			h.Err = frontmatter.ErrNoFrontmatter
			return h
		}
	}
	if !gotBlock {
		if h.BytesRead == 0 {
			h.Err = frontmatter.ErrNoFrontmatter
			return h
		}
		if len(buf) >= FrontmatterCap {
			h.Err = fmt.Errorf("frontmatter block does not close within the first %d bytes", FrontmatterCap)
			return h
		}
		h.Err = frontmatter.ErrNoFrontmatter
		return h
	}
	var raw map[string]any
	if err := yaml.Unmarshal(fm, &raw); err != nil {
		h.Err = fmt.Errorf("frontmatter is not valid YAML: %w", err)
		return h
	}
	if raw == nil {
		raw = map[string]any{}
	}
	h.Raw = raw
	return h
}

// delimiterProbe is the shortest prefix that can decide "this file does
// not open a frontmatter block": the delimiter plus its newline, allowing
// for a BOM and a CR.
var delimiterProbe = []byte("\xEF\xBB\xBF---\r\n")

// opensWithDelimiter reports whether data could still be a record: it
// starts with `---` on its own line, modulo a BOM.
func opensWithDelimiter(data []byte) bool {
	d := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !bytes.HasPrefix(d, []byte("---")) {
		return false
	}
	rest := d[3:]
	rest = bytes.TrimLeft(rest, " \t\r")
	return len(rest) == 0 || rest[0] == '\n'
}

// Project builds the FieldsResult for the selected keys.
func Project(root string, selected []string) (*FieldsResult, error) {
	heads, err := ScanHeads(root)
	if err != nil {
		return nil, err
	}
	res := &FieldsResult{
		Trailer: FieldsTrailer{
			Fields:          selected,
			PerFieldPresent: make(map[string]int, len(selected)),
		},
	}
	// Seed every requested field at zero, so a field present in no story
	// reports `0` rather than being absent from the payload. "Nobody has
	// it" is a true answer to a legitimate question.
	for _, key := range selected {
		res.Trailer.PerFieldPresent[key] = 0
	}
	for _, h := range heads {
		res.Trailer.FilesScanned++
		res.Trailer.BytesRead += h.BytesRead
		if h.Err != nil {
			// A .md with no frontmatter at all is simply not a story —
			// retrospectives, epic briefs and the deferred-work stub all
			// live under implementation_folder. Only a file that MEANT to
			// carry data and failed (malformed YAML, a block past the cap,
			// an I/O error) is worth a warning; one such file loses that
			// file and nothing else.
			if !errors.Is(h.Err, frontmatter.ErrNoFrontmatter) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", h.Path, h.Err))
			}
			continue
		}
		if !h.IsStory() {
			continue
		}
		res.Trailer.StoriesMatched++
		values := make(map[string]any, len(selected))
		for _, key := range selected {
			v, ok := h.Raw[key]
			if !ok || v == nil {
				continue
			}
			values[key] = v
			res.Trailer.PerFieldPresent[key]++
		}
		res.Stories = append(res.Stories, Fields{
			Path:    h.Path,
			StoryID: h.StoryID(),
			Values:  values,
		})
	}
	return res, nil
}
