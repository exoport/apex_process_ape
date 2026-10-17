package deferred

import (
	"regexp"
	"strings"
)

// IngestOptions carries what the caller knows that the bullets do not.
type IngestOptions struct {
	Story string
	Skill string
	Cycle int
	Date  string
}

// IngestResult reports what was stored.
type IngestResult struct {
	Records []Record `json:"records" yaml:"records"`
	Stored  []string `json:"stored"  yaml:"stored"`
	// Warnings are non-fatal, by design: see Ingest's contract.
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Count    int      `json:"count"              yaml:"count"`
}

// isIndented reports whether a line is a continuation of the bullet above
// it. One space is enough — the framework indents continuations by two.
func isIndented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

// topLevelBulletRe matches an unindented (or single-space-indented) list
// item, which starts a new record. A more deeply indented bullet is a
// sub-item and stays with its parent.
var topLevelBulletRe = regexp.MustCompile(`^ ?[-*] `)

// Structured is a record's fields as a caller already holds them, with
// no bullet to parse.
//
// It is the SECOND front end onto the store, and it exists because
// `ape change` is handed these fields by a skill's terminal contract
// rather than as prose. Rendering them back into a `[Defer]` bullet so
// the parser could take them apart again would put a regex between a
// structured field and the record it already is — which is the failure
// the contract's structured fields were introduced to avoid.
type Structured struct {
	Title   string
	Anchors []string
	Owner   string
	Trigger string
	// Body is the finding, and may span lines.
	Body string
}
