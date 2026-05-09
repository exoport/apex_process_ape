package trait

// TraitRef is an entry in the trait catalog index.
//
//nolint:revive // stutter is intentional: trait.TraitRef reads clearly at call sites; renaming to Ref would lose domain context
type TraitRef struct {
	Name        string   `json:"name"        yaml:"name"`
	Version     string   `json:"version"     yaml:"version"`
	Description string   `json:"description" yaml:"description"`
	File        string   `json:"file"        yaml:"file"`
	Tags        []string `json:"tags"        yaml:"tags"`
}

// Catalog holds all trait references.
type Catalog struct {
	Traits []TraitRef `json:"traits" yaml:"traits"`
}

// ADRRef references a single ADR artifact within a trait.
type ADRRef struct {
	ID       string `json:"id"       yaml:"id"`
	Title    string `json:"title"    yaml:"title"`
	Category string `json:"category" yaml:"category"`
	File     string `json:"file"     yaml:"file"`
}

// PatternRef references a single pattern artifact within a trait.
type PatternRef struct {
	ID       string `json:"id"       yaml:"id"`
	Title    string `json:"title"    yaml:"title"`
	Category string `json:"category" yaml:"category"`
	File     string `json:"file"     yaml:"file"`
}

// Trait is the full definition of a trait loaded from its YAML file.
type Trait struct {
	Name           string       `json:"name"            yaml:"name"`
	Version        string       `json:"version"         yaml:"version"`
	Description    string       `json:"description"     yaml:"description"`
	Uses           []string     `json:"uses"            yaml:"uses"`
	ADRs           []ADRRef     `json:"adrs"            yaml:"adrs"` //nolint:tagliatelle // "adrs" is the correct domain key; "adRs" would be confusing
	Patterns       []PatternRef `json:"patterns"        yaml:"patterns"`
	OwnsCategories []string     `json:"owns_categories" yaml:"owns_categories"` //nolint:tagliatelle // snake_case matches trait YAML file format
}

// Conflict describes a category owned by more than one trait.
type Conflict struct {
	Category string
	Owners   []string
}

// ResolveResult is the output of the resolver.
type ResolveResult struct {
	Traits    []string
	ADRs      []ADRRef
	Patterns  []PatternRef
	Conflicts []Conflict
}

// ConflictStrategy determines how to handle conflicts.
type ConflictStrategy int

const (
	ConflictStrategyFirst ConflictStrategy = iota
	ConflictStrategyLast
	ConflictStrategyAll
)
