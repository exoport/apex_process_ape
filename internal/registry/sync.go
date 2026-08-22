package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// SyncChange is one edit `sync` would make or made.
type SyncChange struct {
	Action string `json:"action"           yaml:"action"` // add | remove | fix-file
	Family string `json:"family"           yaml:"family"`
	ID     string `json:"id"               yaml:"id"`
	File   string `json:"file,omitempty"   yaml:"file,omitempty"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// SyncResult reports what reconciling did (or would do under --check).
type SyncResult struct {
	Families []SyncFamilyResult `json:"families" yaml:"families"`
	Changes  []SyncChange       `json:"changes"  yaml:"changes"`
	Check    bool               `json:"check"    yaml:"check"`
}

// SyncFamilyResult is the per-family outcome of a reconcile.
type SyncFamilyResult struct {
	Family  string `json:"family"           yaml:"family"`
	Index   string `json:"index,omitempty"  yaml:"index,omitempty"`
	Skipped bool   `json:"skipped"          yaml:"skipped"`
	Reason  string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Written bool   `json:"written"          yaml:"written"`
	Entries int    `json:"entries"          yaml:"entries"`
}

// Changed reports whether the reconcile found anything to do.
func (r *SyncResult) Changed() bool { return len(r.Changes) > 0 }

// SyncOptions selects what to reconcile.
type SyncOptions struct {
	Only []string
	// Check makes this a dry run: report the diff, write nothing. This is
	// the semantic the parent `sync` command's --check flag has always
	// declared.
	Check       bool
	IgnoreExt   bool
	GeneratedAt string
}

// Sync reconciles each family's index.yaml against its records on disk:
// records with no entry are added, entries whose id no record claims are
// removed, and an entry whose `file:` does not resolve is repointed at
// the record that claims its id.
//
// It is the repair for D2's findings and nothing more — it does not
// invent titles, statuses or any other field beyond what a new record's
// own frontmatter states.
func Sync(cfg *apexcfg.Resolved, opts SyncOptions) (*SyncResult, error) {
	families, err := selectFamilies(opts.Only)
	if err != nil {
		return nil, err
	}
	// Non-nil so an in-sync run marshals as `"changes": []`, not `null`.
	result := &SyncResult{Check: opts.Check, Changes: []SyncChange{}}
	for _, family := range families {
		famResult, changes, syncErr := syncFamily(cfg, family, opts)
		if syncErr != nil {
			return nil, syncErr
		}
		result.Families = append(result.Families, famResult)
		result.Changes = append(result.Changes, changes...)
	}
	return result, nil
}

func syncFamily(cfg *apexcfg.Resolved, family Family, opts SyncOptions) (SyncFamilyResult, []SyncChange, error) {
	res := SyncFamilyResult{Family: family.Name}
	dir := family.Dir(cfg.Paths)
	switch {
	case !opts.IgnoreExt && !family.Enabled(cfg.Ext):
		res.Skipped, res.Reason = true, "ext_"+family.Name+" is false"
		return res, nil, nil
	case dir == "":
		res.Skipped, res.Reason = true, "the folder this family lives under is not configured"
		return res, nil, nil
	}
	if !dirExists(dir) {
		res.Skipped, res.Reason = true, "directory does not exist"
		return res, nil, nil
	}

	records, err := ListRecords(dir)
	if err != nil {
		return res, nil, err
	}
	idx, err := LoadIndex(dir, family)
	if err != nil {
		return res, nil, err
	}
	res.Index = idx.Path
	if idx.Missing {
		idx.root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		idx.entries = nil
	}
	if idx.entries == nil {
		idx.entries = newEntriesNode(family.Shape)
		setMappingValue(idx.root, family.Name, idx.entries)
	}

	entries, _ := idx.Entries()
	indexed := make(map[string]Entry, len(entries))
	for _, e := range entries {
		indexed[e.ID] = e
	}
	onDisk := make(map[string]Record, len(records))
	for _, rec := range records {
		if rec.ParseErr == nil && rec.ID != "" {
			onDisk[rec.ID] = rec
		}
	}

	changes := make([]SyncChange, 0, len(records)+len(entries))

	// Repoint entries whose file: no longer resolves but whose id is on
	// disk. Done before removals so a renamed record is repaired rather
	// than dropped and re-added.
	for _, e := range entries {
		if !family.HasFileField {
			// This family's index schema has no `file` property. Adding one
			// would make every entry fail the framework's own schema.
			break
		}
		rec, present := onDisk[e.ID]
		if !present {
			continue
		}
		if e.File == rec.Name {
			continue
		}
		// An EMPTY file: is unresolved, not resolved. Without this guard the
		// stat below runs on filepath.Join(dir, "") — the directory itself —
		// which always succeeds, so the one entry that most needs repairing
		// is the one entry sync would skip.
		if e.File != "" {
			if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(e.File))); statErr == nil {
				continue
			}
		}
		setMappingValue(idx.entryNode(e), "file", scalar(rec.Name))
		changes = append(changes, SyncChange{
			Action: "fix-file", Family: family.Name, ID: e.ID, File: rec.Name,
			Detail: "was " + e.File,
		})
	}

	// Remove phantom entries.
	for _, e := range entries {
		if _, present := onDisk[e.ID]; present {
			continue
		}
		removeEntry(idx, family.Shape, e.ID)
		changes = append(changes, SyncChange{
			Action: "remove", Family: family.Name, ID: e.ID, File: e.File,
			Detail: "no record on disk claims this id",
		})
	}

	// Add orphan records, in name order so a first-ever sync produces a
	// stable index.
	for _, rec := range records {
		if rec.ParseErr != nil || rec.ID == "" {
			continue
		}
		if _, present := indexed[rec.ID]; present {
			continue
		}
		fields, err := readRecordFields(rec.Path)
		if err != nil {
			return res, nil, err
		}
		addEntry(idx, family, rec, fields)
		changes = append(changes, SyncChange{
			Action: "add", Family: family.Name, ID: rec.ID, File: rec.Name,
			Detail: "record on disk was absent from the index",
		})
	}

	if len(changes) == 0 {
		after, _ := idx.Entries()
		res.Entries = len(after)
		return res, nil, nil
	}

	if opts.GeneratedAt != "" {
		setMappingValue(idx.root, generatedAtKey, scalar(opts.GeneratedAt))
	}
	after, _ := idx.Entries()
	res.Entries = len(after)

	if opts.Check {
		return res, changes, nil
	}
	if err := idx.Save(); err != nil {
		return res, nil, err
	}
	res.Written = true
	return res, changes, nil
}

func newEntriesNode(shape Shape) *yaml.Node {
	if shape == ShapeMapping {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
}

// indexedFields are the record frontmatter fields an index entry carries.
// Deliberately short: `sync` copies what the record already states and
// invents nothing. Anything else an index holds is authored by the skill
// that owns the document.
var indexedFields = []string{"title", "status", "type", "epic", "capability"}

// readRecordFields reads the frontmatter fields a new index entry needs.
func readRecordFields(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, recordCap)
	n, readErr := f.Read(buf)
	if n == 0 && readErr != nil {
		return nil, fmt.Errorf("read %s: %w", path, readErr)
	}
	fm, _, err := frontmatter.Split(buf[:n])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(fm, &raw); err != nil {
		return nil, fmt.Errorf("%s: frontmatter is not valid YAML: %w", path, err)
	}
	out := map[string]string{}
	for _, key := range indexedFields {
		if v, ok := raw[key]; ok && v != nil {
			out[key] = fmt.Sprintf("%v", v)
		}
	}
	return out, nil
}

func addEntry(idx *Index, family Family, rec Record, fields map[string]string) {
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if family.Shape == ShapeList {
		setMappingValue(body, "id", scalar(rec.ID))
	}
	for _, key := range indexedFields {
		if v, ok := fields[key]; ok {
			setMappingValue(body, key, scalar(v))
		}
	}
	if family.HasFileField {
		setMappingValue(body, "file", scalar(rec.Name))
	}

	if family.Shape == ShapeMapping {
		idx.entries.Content = append(idx.entries.Content, scalar(rec.ID), body)
		return
	}
	idx.entries.Content = append(idx.entries.Content, body)
}

func removeEntry(idx *Index, shape Shape, id string) {
	if idx.entries == nil {
		return
	}
	if shape == ShapeMapping {
		for i := 0; i+1 < len(idx.entries.Content); i += 2 {
			if idx.entries.Content[i].Value == id {
				idx.entries.Content = append(
					idx.entries.Content[:i], idx.entries.Content[i+2:]...,
				)
				return
			}
		}
		return
	}
	for i, node := range idx.entries.Content {
		if entry, err := entryFromMapping(node, ""); err == nil && entry.ID == id {
			idx.entries.Content = append(idx.entries.Content[:i], idx.entries.Content[i+1:]...)
			return
		}
	}
}

// UpdateResult reports a field-delta application.
type UpdateResult struct {
	Index   string `json:"index"   yaml:"index"`
	Family  string `json:"family"  yaml:"family"`
	Entries int    `json:"entries" yaml:"entries"`
	Fields  int    `json:"fields"  yaml:"fields"`
}

// Update applies per-entry field deltas to a family's index and refreshes
// `generated_at` — the contract of `render-index-update.py`, kept
// verbatim: only entries already listed may be updated, an unknown id
// fails BEFORE anything is written, and the rendered result is
// round-trip parsed before it replaces the file.
func Update(cfg *apexcfg.Resolved, family Family, updates map[string]map[string]string, generatedAt string) (*UpdateResult, error) {
	dir := family.Dir(cfg.Paths)
	if dir == "" {
		return nil, fmt.Errorf("the folder %s records live under is not configured", family.Name)
	}
	idx, err := LoadIndex(dir, family)
	if err != nil {
		return nil, err
	}
	if idx.Missing {
		return nil, fmt.Errorf("%s does not exist", idx.Path)
	}
	fields, err := idx.ApplyDeltas(updates, generatedAt)
	if err != nil {
		return nil, err
	}
	if err := idx.Save(); err != nil {
		return nil, err
	}
	return &UpdateResult{
		Index:   idx.Path,
		Family:  family.Name,
		Entries: len(updates),
		Fields:  fields,
	}, nil
}

// ParseUpdates decodes the `{"<id>": {"<field>": "<value>"}}` JSON shape
// the framework's update skills pipe in. Values are coerced to strings
// because an index field is text: a status of `no` or an id of `0001`
// must not become a bool or an int on the way through.
func ParseUpdates(data []byte) (map[string]map[string]string, error) {
	var raw map[string]map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("updates must be a JSON object of {id: {field: value}}: %w", err)
	}
	out := make(map[string]map[string]string, len(raw))
	for id, deltas := range raw {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("updates contain an empty entry id")
		}
		fields := make(map[string]string, len(deltas))
		for key, value := range deltas {
			fields[key] = fmt.Sprintf("%v", value)
		}
		out[id] = fields
	}
	return out, nil
}
