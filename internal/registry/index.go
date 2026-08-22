package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// IndexFileName is the derived listing every family keeps beside its
// records.
const IndexFileName = "index.yaml"

// generatedAtKey is the top-level timestamp both index shapes carry.
const generatedAtKey = "generated_at"

// Entry is one index entry, flattened from either shape.
type Entry struct {
	ID   string
	File string
	// Fields carries every other scalar field verbatim, so `sync` can
	// rewrite an index without inventing or dropping data it does not
	// understand.
	Fields map[string]string
	// pos is the entry's position in a list-shaped index, used to write
	// back in place.
	pos int
}

// Index is a loaded index.yaml, kept as a yaml.Node tree so a rewrite
// preserves key order and comments. PyYAML drops comments on a
// round trip; yaml.v3 does not, so the Go replacement is strictly less
// destructive than the script it retires.
type Index struct {
	Path    string
	Family  Family
	root    *yaml.Node
	entries *yaml.Node
	// Missing records an index.yaml that does not exist on disk.
	Missing bool
}

// ErrIndexNotMapping reports an index whose root is not a YAML mapping.
var ErrIndexNotMapping = errors.New("index root is not a YAML mapping")

// LoadIndex reads the family's index.yaml from dir. A missing file is not
// an error — it is a state the caller reports as a finding.
func LoadIndex(dir string, family Family) (*Index, error) {
	path := filepath.Join(dir, IndexFileName)
	idx := &Index{Path: path, Family: family}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			idx.Missing = true
			return idx, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	root := documentRoot(&doc)
	if root == nil {
		// An empty file is an empty index, not a broken one.
		idx.root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		return idx, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: %w", path, ErrIndexNotMapping)
	}
	idx.root = root
	idx.entries = mappingValue(root, family.Name)
	return idx, nil
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	if doc.Kind == 0 {
		return nil
	}
	return doc
}

// mappingValue returns the value node for key in a mapping node.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// setMappingValue sets key to value, appending at the end when the key is
// new so existing key order is preserved.
func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// Entries flattens the index into id-ordered entries. A malformed entry
// (not a mapping, or missing an id) is returned in bad so the caller can
// report it rather than silently skipping it.
func (idx *Index) Entries() (entries []Entry, bad []string) {
	if idx.entries == nil {
		return nil, nil
	}
	switch idx.Family.Shape {
	case ShapeList:
		if idx.entries.Kind != yaml.SequenceNode {
			return nil, []string{fmt.Sprintf("index[%q] is not a list", idx.Family.Name)}
		}
		for pos, node := range idx.entries.Content {
			entry, err := entryFromMapping(node, "")
			if err != nil {
				bad = append(bad, fmt.Sprintf("entry at position %d: %v", pos, err))
				continue
			}
			entry.pos = pos
			entries = append(entries, entry)
		}
	case ShapeMapping:
		if idx.entries.Kind != yaml.MappingNode {
			return nil, []string{fmt.Sprintf("index[%q] is not a mapping", idx.Family.Name)}
		}
		for i := 0; i+1 < len(idx.entries.Content); i += 2 {
			id := idx.entries.Content[i].Value
			entry, err := entryFromMapping(idx.entries.Content[i+1], id)
			if err != nil {
				bad = append(bad, fmt.Sprintf("entry %q: %v", id, err))
				continue
			}
			entry.pos = i + 1
			entries = append(entries, entry)
		}
	}
	return entries, bad
}

func entryFromMapping(node *yaml.Node, idFromKey string) (Entry, error) {
	if node.Kind != yaml.MappingNode {
		return Entry{}, errors.New("not a mapping")
	}
	e := Entry{ID: idFromKey, Fields: map[string]string{}}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if value.Kind != yaml.ScalarNode {
			continue
		}
		switch key {
		case "id":
			if e.ID == "" {
				e.ID = value.Value
			}
		case "file":
			e.File = value.Value
		}
		e.Fields[key] = value.Value
	}
	if e.ID == "" {
		return Entry{}, errors.New("missing 'id' field")
	}
	return e, nil
}

// ApplyDeltas sets per-entry field values, exactly as
// `render-index-update.py` does: only entries already present may be
// updated, and an unknown id is an error raised BEFORE anything is
// written. Creating an index entry is the job of the skill that authors
// the document it points at.
func (idx *Index) ApplyDeltas(updates map[string]map[string]string, generatedAt string) (fields int, err error) {
	if idx.Missing || idx.root == nil {
		return 0, fmt.Errorf("%s does not exist", idx.Path)
	}
	entries, _ := idx.Entries()
	byID := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	var unknown []string
	for id := range updates {
		if _, ok := byID[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		sortStrings(unknown)
		return 0, fmt.Errorf("unknown %s IDs (not in index): %s",
			idx.Family.Name, strings.Join(unknown, ", "))
	}
	for id, deltas := range updates {
		target := idx.entryNode(byID[id])
		if target == nil {
			return 0, fmt.Errorf("entry %q has no node in %s", id, idx.Path)
		}
		for _, key := range sortedKeys(deltas) {
			setMappingValue(target, key, scalar(deltas[key]))
			fields++
		}
	}
	if generatedAt != "" {
		setMappingValue(idx.root, generatedAtKey, scalar(generatedAt))
	}
	return fields, nil
}

// entryNode returns the mapping node backing an entry.
func (idx *Index) entryNode(e Entry) *yaml.Node {
	if idx.entries == nil {
		return nil
	}
	switch idx.Family.Shape {
	case ShapeList:
		if e.pos < len(idx.entries.Content) {
			return idx.entries.Content[e.pos]
		}
	case ShapeMapping:
		if e.pos < len(idx.entries.Content) {
			return idx.entries.Content[e.pos]
		}
	}
	return nil
}

// scalar builds a scalar node, quoting values that YAML would otherwise
// re-read as a number, bool or null. `render-index-update.py` does the
// same with a custom representer, and for the same reason: an ADR id like
// `0001` or a status of `no` must survive as a string.
func scalar(value string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	if needsQuoting(value) {
		n.Style = yaml.SingleQuotedStyle
	}
	return n
}

func needsQuoting(value string) bool {
	if value == "" {
		return true
	}
	switch strings.ToLower(value) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return true
	}
	// A value that round-trips through YAML as a non-string needs quotes.
	var probe any
	if err := yaml.Unmarshal([]byte(value), &probe); err == nil {
		if _, isString := probe.(string); !isString && probe != nil {
			return true
		}
	}
	return false
}

// Render encodes the index back to YAML: block style, two-space indent,
// key order and comments preserved.
func (idx *Index) Render() ([]byte, error) {
	if idx.root == nil {
		return nil, fmt.Errorf("%s: nothing loaded", idx.Path)
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(idx.root); err != nil {
		return nil, fmt.Errorf("encode %s: %w", idx.Path, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode %s: %w", idx.Path, err)
	}
	return []byte(buf.String()), nil
}

// Save renders the index, verifies the result parses back to a mapping,
// and only then replaces the file — a write-then-verify would leave a
// corrupt index on disk, which is what the round-trip check exists to
// prevent.
func (idx *Index) Save() error {
	data, err := idx.Render()
	if err != nil {
		return err
	}
	var probe yaml.Node
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("round-trip parse of rendered %s failed: %w", idx.Path, err)
	}
	if root := documentRoot(&probe); root == nil || root.Kind != yaml.MappingNode {
		return fmt.Errorf("round-trip parse of rendered %s did not produce a mapping", idx.Path)
	}
	return writeFileAtomic(idx.Path, data)
}

// writeFileAtomic writes via a temp file in the same directory and a
// rename, so a crash mid-write cannot truncate an index.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ape-"+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename onto %s: %w", path, err)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
