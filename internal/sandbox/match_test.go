package sandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatcher(t *testing.T) {
	m := NewMatcher([]string{"api.anthropic.com", "*.githubusercontent.com", "GitHub.com"})

	// Exact (case-insensitive).
	assert.True(t, m.Allowed("api.anthropic.com"))
	assert.True(t, m.Allowed("API.Anthropic.com"))
	assert.True(t, m.Allowed("github.com"))

	// Wildcard matches subdomains at any depth...
	assert.True(t, m.Allowed("raw.githubusercontent.com"))
	assert.True(t, m.Allowed("a.b.githubusercontent.com"))
	// ...but not the apex.
	assert.False(t, m.Allowed("githubusercontent.com"))

	// Deny-by-default.
	assert.False(t, m.Allowed("evil.com"))
	assert.False(t, m.Allowed("notanthropic.com"))

	// Port is stripped before matching.
	assert.True(t, m.Allowed("api.anthropic.com:443"))
}

func TestMatcherEmptyDeniesAll(t *testing.T) {
	m := NewMatcher(nil)
	assert.False(t, m.Allowed("api.anthropic.com"))
	assert.False(t, m.Allowed("anything"))
}
