package registry

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
