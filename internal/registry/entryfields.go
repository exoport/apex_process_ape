package registry

import (
	"fmt"
	"os"
	"strings"

	"github.com/exoport/apex_process_ape/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// fieldKind is the value shape a family's index schema declares for a
// field. Sync copies a record's value only when it already has that shape:
// converting a scalar `tags: go` into `[go]` would be sync authoring a
// value, which is the one thing it does not do.
type fieldKind int

const (
	kindString fieldKind = iota
	kindList
	kindBool
)

// entryField is one property of a family's index-entry schema.
type entryField struct {
	Key      string
	Kind     fieldKind
	Required bool
	// Nullable admits an explicit `null` in the record as a value to copy.
	// Only features' `capability` is declared ["string","null"].
	Nullable bool
	// SlugFromName allows the record's file name to be the value's source
	// when the frontmatter has none: `<prefix>_<slug>.md`. ADR and feature
	// frontmatter carry no slug at all, so for them the name is the only
	// source there is.
	SlugFromName bool
	// IsFile marks the position of `file:`, whose value is always the
	// record's own name. Capabilities have no such property.
	IsFile bool
}

// Entry field tables, one per family, in the key order the framework's own
// generators write — so an entry ape adds reads like its neighbours. Each
// mirrors the family's index schema under framework skill resources:
//
//	adrs          apex-adr-adoption/resources/adr-index-schema.json
//	patterns      apex-pattern-generate/resources/pattern-index-schema.json
//	features      apex-feature-create/resources/feature-index-schema.json
//	capabilities  apex-capability-create/resources/capability-index-schema.json
//
// `id` is not listed: it is the entry's key (features) or its first field,
// placed by addEntry.
// Optional fields are copied when the record states them. `supersedes` and
// `superseded_by` are not in the ADR schema's properties, but it does not
// forbid extra keys and the framework's ADR skills write them.
var (
	adrEntryFields = []entryField{
		{Key: "slug", Required: true, SlugFromName: true},
		{Key: "file", IsFile: true},
		{Key: "status", Required: true},
		{Key: "type", Required: true},
		{Key: "tags", Kind: kindList, Required: true},
		{Key: "version", Required: true},
		{Key: "created_at", Required: true},
		{Key: "updated_at", Required: true},
		{Key: "title", Required: true},
		{Key: "pattern_ids", Kind: kindList},
		{Key: "bootstrap", Kind: kindBool},
		{Key: "supersedes"},
		{Key: "superseded_by"},
	}
	patternEntryFields = []entryField{
		{Key: "slug", Required: true, SlugFromName: true},
		{Key: "category", Required: true},
		{Key: "file", IsFile: true},
		{Key: "status", Required: true},
		{Key: "version", Required: true},
		{Key: "created_at", Required: true},
		{Key: "updated_at", Required: true},
	}
	featureEntryFields = []entryField{
		{Key: "name", Required: true},
		{Key: "slug", Required: true, SlugFromName: true},
		{Key: "file", IsFile: true},
		{Key: "capability", Required: true, Nullable: true},
		{Key: "status", Required: true},
		{Key: "depends_on", Kind: kindList},
		{Key: "depended_on_by", Kind: kindList},
		{Key: "created_at", Required: true},
		{Key: "updated_at", Required: true},
	}
	capabilityEntryFields = []entryField{
		{Key: "slug", Required: true, SlugFromName: true},
		{Key: "name", Required: true},
		{Key: "status", Required: true},
		{Key: "components", Kind: kindList, Required: true},
		{Key: "related_epics", Kind: kindList},
		{Key: "related_capabilities", Kind: kindList},
		{Key: "created_at", Required: true},
		{Key: "updated_at", Required: true},
	}
)

// entryValues is what a record supplies for a new index entry.
type entryValues struct {
	// Nodes are the rendered values, keyed by field.
	Nodes map[string]*yaml.Node
	// Missing are required fields the record has no source for, in table
	// order. Sync reports them; it does not invent them.
	Missing []string
}

// readEntryValues reads the frontmatter of the record at path and resolves
// every field of the family's entry table against it.
func readEntryValues(path, name string, fields []entryField) (entryValues, error) {
	f, err := os.Open(path)
	if err != nil {
		return entryValues{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, recordCap)
	n, readErr := f.Read(buf)
	if n == 0 && readErr != nil {
		return entryValues{}, fmt.Errorf("read %s: %w", path, readErr)
	}
	fm, _, err := frontmatter.Split(buf[:n])
	if err != nil {
		return entryValues{}, fmt.Errorf("%s: %w", path, err)
	}
	// Decoded as nodes, not into map[string]any: a node keeps the text the
	// record wrote, so `created_at: 20260925005346` stays that string
	// rather than passing through an int64, and `version: v1` cannot be
	// mangled by any numeric reading.
	var doc yaml.Node
	if err := yaml.Unmarshal(fm, &doc); err != nil {
		return entryValues{}, fmt.Errorf("%s: frontmatter is not valid YAML: %w", path, err)
	}
	root := documentRoot(&doc)
	if root != nil && root.Kind != yaml.MappingNode {
		root = nil
	}
	return resolveEntryValues(root, name, fields), nil
}

func resolveEntryValues(fm *yaml.Node, name string, fields []entryField) entryValues {
	out := entryValues{Nodes: make(map[string]*yaml.Node, len(fields))}
	for _, field := range fields {
		if field.IsFile {
			out.Nodes[field.Key] = scalar(name)
			continue
		}
		var src *yaml.Node
		if fm != nil {
			src = mappingValue(fm, field.Key)
		}
		node := copyValue(src, field)
		if node == nil && field.SlugFromName {
			if slug := slugFromName(name); slug != "" {
				node = scalar(slug)
			}
		}
		if node == nil {
			if field.Required {
				out.Missing = append(out.Missing, field.Key)
			}
			continue
		}
		out.Nodes[field.Key] = node
	}
	return out
}

// copyValue renders a frontmatter value as an index value when it has the
// shape the field declares, and returns nil otherwise.
func copyValue(src *yaml.Node, field entryField) *yaml.Node {
	if src == nil {
		return nil
	}
	if src.Kind == yaml.AliasNode {
		src = src.Alias
	}
	if src.Kind == yaml.ScalarNode && src.Tag == "!!null" {
		if field.Nullable {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
		return nil
	}
	switch field.Kind {
	case kindList:
		if src.Kind != yaml.SequenceNode {
			return nil
		}
		// Flow style, as `update` writes lists and the generators do.
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, item := range src.Content {
			if item.Kind != yaml.ScalarNode || item.Tag == "!!null" {
				return nil
			}
			seq.Content = append(seq.Content, scalar(item.Value))
		}
		return seq
	case kindBool:
		if src.Kind != yaml.ScalarNode || src.Tag != "!!bool" {
			return nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: src.Value}
	default:
		if src.Kind != yaml.ScalarNode || src.Value == "" {
			return nil
		}
		// scalar() quotes whatever YAML would re-read as a non-string, so a
		// timestamp stays a quoted string in the index.
		return scalar(src.Value)
	}
}

// slugFromName returns the part of a record file name after the id prefix
// and before `.md`: `adr-0005_use-go.md` → `use-go`. Every family names its
// records `<id-ish>_<slug>.md`, so the first underscore is the separator.
func slugFromName(name string) string {
	stem := strings.TrimSuffix(name, ".md")
	_, slug, ok := strings.Cut(stem, "_")
	if !ok {
		return ""
	}
	return slug
}
