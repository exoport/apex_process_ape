package sandbox

import "strings"

// Matcher decides whether a hostname is on an egress allowlist. Entries
// are exact hostnames or a single leading-wildcard label
// ("*.githubusercontent.com"). A wildcard matches any subdomain depth but
// NOT the apex ("*.example.com" matches a.example.com and a.b.example.com,
// not example.com). Matching is case-insensitive. Deny-by-default: an
// empty matcher allows nothing.
type Matcher struct {
	exact    map[string]struct{}
	suffixes []string // stored as ".example.com" for "*.example.com"
}

// NewMatcher compiles the allowlist patterns. Patterns are assumed
// pre-validated (see validateDomainPattern); unknown shapes are treated
// as exact hostnames so a matcher never silently widens access.
func NewMatcher(patterns []string) *Matcher {
	m := &Matcher{exact: make(map[string]struct{})}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(p, "*."); ok {
			m.suffixes = append(m.suffixes, "."+rest)
			continue
		}
		m.exact[p] = struct{}{}
	}
	return m
}

// Allowed reports whether host is permitted. host may include a port; it
// is stripped before matching.
func (m *Matcher) Allowed(host string) bool {
	host = strings.ToLower(host)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		// Guard against IPv6 literals like [::1] — only strip when the
		// colon follows a plausible hostname/port, not inside brackets.
		if !strings.Contains(host, "]") || strings.HasSuffix(host[:i], "]") {
			host = host[:i]
		}
	}
	host = strings.Trim(host, "[]")
	if _, ok := m.exact[host]; ok {
		return true
	}
	for _, suf := range m.suffixes {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	return false
}
