package deferred

import "strings"

// normaliseTitle collapses a title for duplicate detection: lowercase,
// alphanumerics only. Crude on purpose — the finding is a candidate, and a
// cleverer similarity metric would only make it look more authoritative
// than it is.
func normaliseTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
