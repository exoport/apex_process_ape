package registry

import (
	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"gopkg.in/yaml.v3"
)

// BackfillFill is one existing entry that gained fields.
type BackfillFill struct {
	Family string   `json:"family"         yaml:"family"`
	ID     string   `json:"id"             yaml:"id"`
	File   string   `json:"file,omitempty" yaml:"file,omitempty"`
	Fields []string `json:"fields"         yaml:"fields"`
}

// BackfillGap is an existing entry still short of a required field after
// the backfill, because its record has no value to copy.
type BackfillGap struct {
	Family string   `json:"family"           yaml:"family"`
	ID     string   `json:"id"               yaml:"id"`
	File   string   `json:"file,omitempty"   yaml:"file,omitempty"`
	Fields []string `json:"fields,omitempty" yaml:"fields,omitempty"`
	Reason string   `json:"reason"           yaml:"reason"`
}

// BackfillResult reports what a backfill did (or would do under Check).
type BackfillResult struct {
	Families []SyncFamilyResult `json:"families" yaml:"families"`
	Fills    []BackfillFill     `json:"fills"    yaml:"fills"`
	// Gaps are NOT pending work: nothing on disk can close them, so a
	// re-run finds the same set. That is why they never make --check exit
	// 1 — a migration whose check can never pass is one that re-runs on
	// every update.
	Gaps  []BackfillGap `json:"gaps"  yaml:"gaps"`
	Check bool          `json:"check" yaml:"check"`
}

// Pending reports whether there is anything to fill.
func (r *BackfillResult) Pending() bool { return len(r.Fills) > 0 }

// Backfill completes the index entries that already exist: a field the
// family's index schema requires that is ABSENT from an entry is copied in
// from the entry's record, by the same rules Sync uses for a new entry.
//
// It is narrow on purpose, because it runs unattended as a framework
// migration on `ape framework update`:
//
//   - no adds, no removes, no repoints — the set of entries and every
//     `file:` stay exactly as they were; that is Sync's job, and a
//     structural change is not one a migration makes in a user's project
//     without being asked;
//   - a value that is present is never touched, even an empty one — an
//     entry that says something is authored, and a wrong value is a
//     judgment for the skill that owns the record;
//   - `file:` is never filled — an entry with no file is a
//     registry.file_unresolved finding, and repointing is Sync's;
//   - an entry whose record is absent or unreadable is reported as a gap
//     and left alone.
func Backfill(cfg *apexcfg.Resolved, opts SyncOptions) (*BackfillResult, error) {
	families, err := selectFamilies(opts.Only)
	if err != nil {
		return nil, err
	}
	result := &BackfillResult{Check: opts.Check, Fills: []BackfillFill{}, Gaps: []BackfillGap{}}
	for _, family := range families {
		famResult, fills, gaps, err := backfillFamily(cfg, family, opts)
		if err != nil {
			return nil, err
		}
		result.Families = append(result.Families, famResult)
		result.Fills = append(result.Fills, fills...)
		result.Gaps = append(result.Gaps, gaps...)
	}
	return result, nil
}

func backfillFamily(cfg *apexcfg.Resolved, family Family, opts SyncOptions) (SyncFamilyResult, []BackfillFill, []BackfillGap, error) {
	res := SyncFamilyResult{Family: family.Name}
	dir := family.Dir(cfg.Paths)
	switch {
	case !opts.IgnoreExt && !family.Enabled(cfg.Ext):
		res.Skipped, res.Reason = true, "ext_"+family.Name+" is false"
		return res, nil, nil, nil
	case dir == "":
		res.Skipped, res.Reason = true, reasonDirUnconfigured
		return res, nil, nil, nil
	}
	if !dirExists(dir) {
		res.Skipped, res.Reason = true, reasonDirMissing
		return res, nil, nil, nil
	}
	idx, err := LoadIndex(dir, family)
	if err != nil {
		return res, nil, nil, err
	}
	res.Index = idx.Path
	if idx.Missing || idx.entries == nil {
		// Creating an index is an add; sync does that, not this.
		res.Skipped, res.Reason = true, "no index to backfill"
		return res, nil, nil, nil
	}
	records, err := ListRecords(dir)
	if err != nil {
		return res, nil, nil, err
	}
	onDisk := make(map[string]Record, len(records))
	for _, rec := range records {
		if rec.ParseErr == nil && rec.ID != "" {
			onDisk[rec.ID] = rec
		}
	}

	entries, _ := idx.Entries()
	res.Entries = len(entries)
	var fills []BackfillFill
	var gaps []BackfillGap
	for _, e := range entries {
		node := idx.entryNode(e)
		if node == nil || node.Kind != yaml.MappingNode {
			continue
		}
		absent := absentRequired(node, family.entryFields)
		if len(absent) == 0 {
			continue
		}
		rec, ok := onDisk[e.ID]
		if !ok {
			gaps = append(gaps, BackfillGap{
				Family: family.Name, ID: e.ID, File: e.File, Fields: absent,
				Reason: "no readable record on disk claims this id — `ape registry verify` says why",
			})
			continue
		}
		values, err := readEntryValues(rec.Path, rec.Name, family.entryFields)
		if err != nil {
			return res, nil, nil, err
		}
		var filled, unfilled []string
		for _, key := range absent {
			v, has := values.Nodes[key]
			if !has {
				unfilled = append(unfilled, key)
				continue
			}
			insertInOrder(node, family.entryFields, key, v)
			filled = append(filled, key)
		}
		if len(filled) > 0 {
			fills = append(fills, BackfillFill{Family: family.Name, ID: e.ID, File: rec.Name, Fields: filled})
		}
		if len(unfilled) > 0 {
			gaps = append(gaps, BackfillGap{
				Family: family.Name, ID: e.ID, File: rec.Name, Fields: unfilled,
				Reason: "the record states no value for these",
			})
		}
	}

	if len(fills) == 0 || opts.Check {
		return res, fills, gaps, nil
	}
	if opts.GeneratedAt != "" {
		setMappingValue(idx.root, generatedAtKey, scalar(opts.GeneratedAt))
	}
	if err := idx.Save(); err != nil {
		return res, nil, nil, err
	}
	res.Written = true
	return res, fills, gaps, nil
}

// absentRequired lists the required, non-file fields entry has no key for.
func absentRequired(entry *yaml.Node, fields []entryField) []string {
	var out []string
	for _, f := range fields {
		if !f.Required || f.IsFile {
			continue
		}
		if mappingValue(entry, f.Key) == nil {
			out = append(out, f.Key)
		}
	}
	return out
}

// insertInOrder adds key to entry right after the nearest field that
// precedes it in the family's table and is already present, so a filled
// entry reads in schema order where its existing order allows. With no
// such neighbour it goes after `id`, or first.
func insertInOrder(entry *yaml.Node, fields []entryField, key string, value *yaml.Node) {
	at := -1
	if i := keyIndex(entry, "id"); i >= 0 {
		at = i
	}
	for _, f := range fields {
		if f.Key == key {
			break
		}
		if i := keyIndex(entry, f.Key); i >= 0 {
			at = i
		}
	}
	pos := 0
	if at >= 0 {
		pos = at + 2 // past the key node and its value
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	content := make([]*yaml.Node, 0, len(entry.Content)+2)
	content = append(content, entry.Content[:pos]...)
	content = append(content, k, value)
	content = append(content, entry.Content[pos:]...)
	entry.Content = content
}

// keyIndex returns the Content index of key's key node, or -1.
func keyIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}
