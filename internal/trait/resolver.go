package trait

// Resolver resolves a list of trait names into a merged set of ADRs and patterns,
// detecting and handling conflicts.
type Resolver struct {
	catalog *Catalog
}

// NewResolver creates a new Resolver backed by the given catalog.
func NewResolver(catalog *Catalog) *Resolver {
	return &Resolver{catalog: catalog}
}

// Resolve expands all named traits (including sub-traits via Uses) and collects
// their ADRs, patterns, and any category ownership conflicts.
func (r *Resolver) Resolve(names []string) (*ResolveResult, error) {
	visited := make(map[string]bool)
	var orderedTraits []string
	var allADRs []ADRRef
	var allPatterns []PatternRef

	// depth-first expansion
	var expand func(name string) error
	expand = func(name string) error {
		if visited[name] {
			return nil
		}
		visited[name] = true

		t, err := LoadTrait(name)
		if err != nil {
			return err
		}

		for _, sub := range t.Uses {
			if err := expand(sub); err != nil {
				return err
			}
		}

		orderedTraits = append(orderedTraits, name)

		for _, a := range t.ADRs {
			if !adrExists(allADRs, a.ID) {
				allADRs = append(allADRs, a)
			}
		}
		for _, p := range t.Patterns {
			if !patternExists(allPatterns, p.ID) {
				allPatterns = append(allPatterns, p)
			}
		}

		return nil
	}

	for _, name := range names {
		if err := expand(name); err != nil {
			return nil, err
		}
	}

	conflicts := r.detectConflicts(orderedTraits)

	return &ResolveResult{
		Traits:    orderedTraits,
		ADRs:      allADRs,
		Patterns:  allPatterns,
		Conflicts: conflicts,
	}, nil
}

// ResolveConflicts applies a conflict resolution strategy to a ResolveResult.
func (r *Resolver) ResolveConflicts(result *ResolveResult, strategy ConflictStrategy) *ResolveResult {
	if len(result.Conflicts) == 0 {
		return result
	}

	// For first/last strategies, we filter traits to keep only the first or last
	// owner for each conflicting category. For "all", we keep everything.
	switch strategy {
	case ConflictStrategyAll:
		// No filtering needed, keep all.
		return &ResolveResult{
			Traits:    result.Traits,
			ADRs:      result.ADRs,
			Patterns:  result.Patterns,
			Conflicts: nil,
		}
	case ConflictStrategyFirst:
		resolvedTraits := r.filterTraitsByConflict(result.Traits, result.Conflicts, true)
		return r.rebuildResult(resolvedTraits)
	case ConflictStrategyLast:
		resolvedTraits := r.filterTraitsByConflict(result.Traits, result.Conflicts, false)
		return r.rebuildResult(resolvedTraits)
	default:
		return result
	}
}

func (r *Resolver) filterTraitsByConflict(traits []string, conflicts []Conflict, keepFirst bool) []string {
	excluded := make(map[string]bool)

	for _, c := range conflicts {
		if len(c.Owners) < 2 {
			continue
		}
		var drop []string
		if keepFirst {
			drop = c.Owners[1:]
		} else {
			drop = c.Owners[:len(c.Owners)-1]
		}
		for _, d := range drop {
			excluded[d] = true
		}
	}

	var result []string
	for _, t := range traits {
		if !excluded[t] {
			result = append(result, t)
		}
	}
	return result
}

func (r *Resolver) rebuildResult(traitNames []string) *ResolveResult {
	var adrs []ADRRef
	var patterns []PatternRef

	for _, name := range traitNames {
		t, err := LoadTrait(name)
		if err != nil {
			continue
		}
		for _, a := range t.ADRs {
			if !adrExists(adrs, a.ID) {
				adrs = append(adrs, a)
			}
		}
		for _, p := range t.Patterns {
			if !patternExists(patterns, p.ID) {
				patterns = append(patterns, p)
			}
		}
	}

	return &ResolveResult{
		Traits:    traitNames,
		ADRs:      adrs,
		Patterns:  patterns,
		Conflicts: nil,
	}
}

func (r *Resolver) detectConflicts(traitNames []string) []Conflict {
	// map category -> list of trait owners
	categoryOwners := make(map[string][]string)

	for _, name := range traitNames {
		t, err := LoadTrait(name)
		if err != nil {
			continue
		}
		for _, cat := range t.OwnsCategories {
			categoryOwners[cat] = append(categoryOwners[cat], name)
		}
	}

	var conflicts []Conflict
	for cat, owners := range categoryOwners {
		if len(owners) > 1 {
			conflicts = append(conflicts, Conflict{
				Category: cat,
				Owners:   owners,
			})
		}
	}

	return conflicts
}

func adrExists(adrs []ADRRef, id string) bool {
	for _, a := range adrs {
		if a.ID == id {
			return true
		}
	}
	return false
}

func patternExists(patterns []PatternRef, id string) bool {
	for _, p := range patterns {
		if p.ID == id {
			return true
		}
	}
	return false
}
