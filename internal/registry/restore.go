package registry

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"gopkg.in/yaml.v3"
)

// RestoreChange is one record whose frontmatter was rebuilt, or would be.
type RestoreChange struct {
	Family string `json:"family"           yaml:"family"`
	ID     string `json:"id"               yaml:"id"`
	File   string `json:"file"             yaml:"file"`
	Fields int    `json:"fields"           yaml:"fields"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// RestoreSkip is an unreadable record RestoreHeaders could not repair.
type RestoreSkip struct {
	Family string `json:"family" yaml:"family"`
	File   string `json:"file"   yaml:"file"`
	Reason string `json:"reason" yaml:"reason"`
}

// RestoreResult reports what RestoreHeaders did, or would do.
type RestoreResult struct {
	Changes []RestoreChange `json:"changes" yaml:"changes"`
	Skipped []RestoreSkip   `json:"skipped" yaml:"skipped"`
	Check   bool            `json:"check"   yaml:"check"`
}

// Changed reports whether anything is repairable.
func (r *RestoreResult) Changed() bool { return len(r.Changes) > 0 }

// indexOnlyKeys are index bookkeeping, not record frontmatter. `file` and
// `slug` describe where the index found the record; the record does not
// restate them.
var indexOnlyKeys = map[string]bool{"file": true, "slug": true}

// RestoreHeaders rebuilds the frontmatter of records that have none, from
// the index entry that already describes them.
//
// This is `sync` run in the other direction, and it exists because the two
// findings `registry.record_unparseable` and `registry.phantom_entry`
// arrive together and used to have no non-destructive remedy: the record
// claims no id, so sync read its entry as a phantom and offered to delete
// the only surviving copy of the record's id, title, type, status, version
// and dates. Writing them back where they belong dissolves both findings
// and loses nothing.
//
// It writes ONLY into a record with no frontmatter block at all. A record
// whose frontmatter merely fails to parse is left alone: overwriting a
// header someone authored, to make a checker happy, is a different and
// much worse operation than giving a headerless file the header the index
// says it always had. Everything written is copied verbatim from the entry
// — including sequence values like `tags`, which `Entry.Fields` drops and
// this reads off the YAML node instead.
func RestoreHeaders(cfg *apexcfg.Resolved, opts SyncOptions) (*RestoreResult, error) {
	families, err := selectFamilies(opts.Only)
	if err != nil {
		return nil, err
	}
	result := &RestoreResult{Check: opts.Check, Changes: []RestoreChange{}, Skipped: []RestoreSkip{}}
	for _, family := range families {
		if !opts.IgnoreExt && !family.Enabled(cfg.Ext) {
			continue
		}
		dir := family.Dir(cfg.Paths)
		if dir == "" || !dirExists(dir) {
			continue
		}
		if err := restoreFamily(cfg.Root, dir, family, opts, result); err != nil {
			return nil, err
		}
	}
	sort.Slice(result.Changes, func(i, j int) bool { return result.Changes[i].File < result.Changes[j].File })
	sort.Slice(result.Skipped, func(i, j int) bool { return result.Skipped[i].File < result.Skipped[j].File })
	return result, nil
}

func restoreFamily(root, dir string, family Family, opts SyncOptions, out *RestoreResult) error {
	records, err := ListRecords(dir)
	if err != nil {
		return err
	}
	var unreadable []Record
	for _, rec := range records {
		if rec.ParseErr != nil {
			unreadable = append(unreadable, rec)
		}
	}
	if len(unreadable) == 0 {
		return nil
	}

	idx, err := LoadIndex(dir, family)
	if err != nil {
		return err
	}
	if idx.Missing {
		for _, rec := range unreadable {
			out.Skipped = append(out.Skipped, RestoreSkip{
				Family: family.Name, File: rec.Name,
				Reason: "no index.yaml, so nothing describes this record",
			})
		}
		return nil
	}
	entries, _ := idx.Entries()

	for _, rec := range unreadable {
		body, readErr := os.ReadFile(rec.Path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", rec.Path, readErr)
		}
		// A UTF-8 BOM ahead of the delimiter still means "this file has a
		// frontmatter block", so strip it before deciding.
		if bytes.HasPrefix(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), []byte("---")) {
			out.Skipped = append(out.Skipped, RestoreSkip{
				Family: family.Name, File: rec.Name,
				Reason: "has a frontmatter block that does not parse — repair it by hand, " +
					"this command only gives a headerless record the header its index entry states",
			})
			continue
		}
		entry, node, found := entryForFile(idx, entries, family, rec.Name)
		if !found {
			out.Skipped = append(out.Skipped, RestoreSkip{
				Family: family.Name, File: rec.Name,
				Reason: "no index entry names this file, so nothing states what its id or title is",
			})
			continue
		}
		fm, count, buildErr := frontmatterFromEntry(node, entry.ID, root, dir, rec.Name)
		if buildErr != nil {
			return buildErr
		}
		out.Changes = append(out.Changes, RestoreChange{
			Family: family.Name, ID: entry.ID, File: rec.Name, Fields: count,
			Detail: "rebuilt from the index entry",
		})
		if opts.Check {
			continue
		}
		if err := atomicfile.Write(rec.Path, append(fm, body...)); err != nil {
			return err
		}
	}
	return nil
}

// entryForFile finds the index entry whose file: names this record. Only
// the file field can match: the record itself claims no id, which is the
// whole problem.
func entryForFile(idx *Index, entries []Entry, family Family, name string) (Entry, *yaml.Node, bool) {
	if !family.HasFileField {
		return Entry{}, nil, false
	}
	for _, e := range entries {
		if e.File == name {
			return e, idx.entryNode(e), true
		}
	}
	return Entry{}, nil, false
}

// frontmatterFromEntry renders an index entry as a record frontmatter
// block, preserving key order and every value shape the entry carries.
func frontmatterFromEntry(node *yaml.Node, id, root, dir, name string) (block []byte, fieldCount int, err error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, 0, fmt.Errorf("index entry for %s is not a mapping", name)
	}
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	fields := 0
	haveID := false
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if indexOnlyKeys[key] {
			continue
		}
		if key == "id" {
			haveID = true
		}
		out.Content = append(out.Content, node.Content[i], node.Content[i+1])
		fields++
	}
	if !haveID {
		// A mapping-shaped index keys its entries by id instead of carrying
		// an `id:` field, but the record must state its own id — that is the
		// field whose absence started all of this. Prepended, so the restored
		// header reads like every hand-authored one.
		out.Content = append([]*yaml.Node{scalar("id"), scalar(id)}, out.Content...)
		fields++
	}
	// `output_document` is the record's own self-reference; every
	// well-formed record in these families carries it and no index does.
	rel := filepath.ToSlash(filepath.Join(relOrDir(root, dir), name))
	out.Content = append(out.Content, scalar("output_document"), scalar(rel))
	fields++

	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return nil, 0, fmt.Errorf("render frontmatter for %s: %w", name, err)
	}
	if err := enc.Close(); err != nil {
		return nil, 0, fmt.Errorf("render frontmatter for %s: %w", name, err)
	}
	buf.WriteString("---\n\n")
	return buf.Bytes(), fields, nil
}

// relOrDir renders the family directory the way a record's
// output_document states it: relative to the project root.
//
// Derived from the root rather than by looking for a path segment named
// "development". That segment is not a constant — `development_folder` is
// a config variable a project may rename — so the search returned the
// ABSOLUTE directory for any project that had, writing a machine-specific
// path into a committed record. It also matched a coincidental ancestor:
// a project under `/home/u/development/proj` resolved to
// `development/proj/...`, rooted at the wrong directory entirely.
//
// filepath.Rel answers exactly the question being asked, and the resolved
// config has carried Root all along. The old fallback stays for the case
// Rel genuinely cannot express (a different volume on Windows).
func relOrDir(root, dir string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(dir)
}
