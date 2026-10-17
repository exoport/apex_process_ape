package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// IndexFileName is the derived listing every family keeps beside its
// records.
const IndexFileName = "index.yaml"

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
