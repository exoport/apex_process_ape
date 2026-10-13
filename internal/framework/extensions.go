package framework

// Extension is a configurable APEX framework extension that the user
// chooses among during first-run config bootstrap. The set is
// hardcoded today; a future framework version may ship a manifest
// (_apex/extensions.yaml) that this package would parse
// instead.
type Extension struct {
	ID          string
	Description string
	// Standard marks the framework's standard configuration, which the
	// setup checklist pre-selects. The headless --extensions flag takes
	// exactly what it is given and ignores this.
	Standard bool
}

// Extensions is the canonical list of extensions offered to the user
// during `ape framework update` config bootstrap. Order is preserved
// in the TUI. The standard configuration is ADRs, capabilities and
// features; patterns is opt-in.
var Extensions = []Extension{
	{ID: "ext-adrs", Description: "Architecture Decision Records — track significant architectural choices.", Standard: true},
	{ID: "ext-patterns", Description: "Reusable patterns catalog — codify and enforce engineering patterns."},
	{ID: "ext-capabilities", Description: "Capability inventory — track product capabilities and their delivery state.", Standard: true},
	{ID: "ext-features", Description: "Feature inventory — track features, their states, and links to stories.", Standard: true},
}

// ExtensionIDs returns the bare identifiers of the canonical extensions.
func ExtensionIDs() []string {
	out := make([]string, 0, len(Extensions))
	for _, e := range Extensions {
		out = append(out, e.ID)
	}
	return out
}

// IsKnownExtension reports whether id is one of the canonical
// extensions. Used to validate --extensions flag values.
func IsKnownExtension(id string) bool {
	for _, e := range Extensions {
		if e.ID == id {
			return true
		}
	}
	return false
}
