package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// syncedEntry syncs one family and returns the raw index text plus the
// decoded entry for id.
func syncedEntry(t *testing.T, f *fixture, family, id string) (body string, entry map[string]any, res *SyncResult) {
	t.Helper()
	res, err := Sync(f.cfg, SyncOptions{Only: []string{family}, GeneratedAt: "20260925000000"})
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(f.dir(family), IndexFileName))
	require.NoError(t, err)
	body = string(raw)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	switch entries := doc[family].(type) {
	case map[string]any:
		entry, _ = entries[id].(map[string]any)
		return body, entry, res
	case []any:
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok && m["id"] == id {
				return body, m, res
			}
		}
	}
	t.Fatalf("%s not in synced %s index:\n%s", id, family, body)
	return "", nil, nil
}

// entryKeys returns the keys of id's entry in written order.
func entryKeys(t *testing.T, body, family, id string) []string {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(body), &doc))
	entries := mappingValue(documentRoot(&doc), family)
	require.NotNil(t, entries)
	var entry *yaml.Node
	if entries.Kind == yaml.MappingNode {
		entry = mappingValue(entries, id)
	} else {
		for _, e := range entries.Content {
			if v := mappingValue(e, "id"); v != nil && v.Value == id {
				entry = e
			}
		}
	}
	require.NotNil(t, entry)
	var keys []string
	for i := 0; i < len(entry.Content); i += 2 {
		keys = append(keys, entry.Content[i].Value)
	}
	return keys
}

func requireNoMissing(t *testing.T, res *SyncResult) {
	t.Helper()
	for _, c := range res.Changes {
		require.Empty(t, c.Missing, "%s %s: %s", c.Action, c.ID, c.Detail)
	}
}

// TestSync_ADREntryIsSchemaComplete is the defect the framework reported:
// an ADR added by sync carried only id, title, status, type and file, so
// every orphan repair wrote an entry adr-index-schema.json rejects.
func TestSync_ADREntryIsSchemaComplete(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", "adr-0005_use-go-stdlib.md", `---
id: "ADR-0005"
title: "Use the Go Standard Library"
type: "technology"
status: proposed
version: v1
created_at: 20260925005346
updated_at: "20260925005346"
tags: [go, dependencies]
pattern_ids: [PAT-0001]
bootstrap: false
output_document: "x"
---
body
`)
	body, entry, res := syncedEntry(t, f, "adrs", "ADR-0005")
	requireNoMissing(t, res)

	require.Equal(t, []string{
		"id", "slug", "file", "status", "type", "tags", "version",
		"created_at", "updated_at", "title", "pattern_ids", "bootstrap",
	}, entryKeys(t, body, "adrs", "ADR-0005"), "schema key order, and nothing the schema does not name")
	require.Equal(t, "use-go-stdlib", entry["slug"], "ADR frontmatter has no slug: the file name is the source")
	require.Equal(t, []any{"go", "dependencies"}, entry["tags"], "a list stays a YAML sequence")
	require.Equal(t, "20260925005346", entry["created_at"],
		"an unquoted timestamp in the record is still a string in the index")
	require.Equal(t, false, entry["bootstrap"], "a bool stays a bool")
	require.Contains(t, body, "tags: [go, dependencies]", "flow style, as update and the generators write lists")
}

func TestSync_PatternEntryIsSchemaComplete(t *testing.T) {
	f := newFixture(t)
	f.raw("patterns", "patloc-0001_retry-budget.md", `---
id: PATLOC-0001
slug: "retry-budget"
category: "resilience"
version: v1
status: draft
created_at: "20260925005346"
updated_at: "20260925005346"
---
`)
	body, entry, res := syncedEntry(t, f, "patterns", "PATLOC-0001")
	requireNoMissing(t, res)
	require.Equal(t, []string{
		"id", "slug", "category", "file", "status", "version", "created_at", "updated_at",
	}, entryKeys(t, body, "patterns", "PATLOC-0001"))
	require.Equal(t, "patloc-0001_retry-budget.md", entry["file"])
}

func TestSync_FeatureEntryIsSchemaComplete(t *testing.T) {
	f := newFixture(t)
	f.raw("features", "feat-1-2_name-validation.md", `---
id: FEAT-1-2
name: "Name Validation"
capability: CAP-1
status: proposed
depends_on: [FEAT-1-1]
depended_on_by: []
created_at: "20260925005346"
updated_at: "20260925005346"
output_document: ""
---
`)
	body, entry, res := syncedEntry(t, f, "features", "FEAT-1-2")
	requireNoMissing(t, res)
	require.Equal(t, []string{
		"name", "slug", "file", "capability", "status", "depends_on", "depended_on_by",
		"created_at", "updated_at",
	}, entryKeys(t, body, "features", "FEAT-1-2"), "no id field: features are keyed by id")
	require.Equal(t, "name-validation", entry["slug"])
	require.Equal(t, []any{}, entry["depended_on_by"], "an empty list is copied as an empty list")
}

// TestSync_FeatureNullCapabilityIsCopied: the one nullable field in any
// schema. A record saying `capability: null` states a value.
func TestSync_FeatureNullCapabilityIsCopied(t *testing.T) {
	f := newFixture(t)
	f.raw("features", "feat-1-1_a.md", "---\nid: FEAT-1-1\nname: A\ncapability: null\nstatus: proposed\n"+
		"created_at: \"20260925005346\"\nupdated_at: \"20260925005346\"\n---\n")
	body, entry, res := syncedEntry(t, f, "features", "FEAT-1-1")
	requireNoMissing(t, res)
	require.Contains(t, entry, "capability")
	require.Nil(t, entry["capability"])
	require.Contains(t, body, "capability: null")
}

func TestSync_CapabilityEntryIsSchemaComplete(t *testing.T) {
	f := newFixture(t)
	f.raw("capabilities", "cap-4_greeting-history.md", `---
id: CAP-4
name: "Greeting History"
slug: "greeting-history"
status: "proposed"
components: ["internal/server", "README.md"]
related_epics: []
related_capabilities: [CAP-1]
created_at: "20260925010245"
updated_at: "20260925010245"
---
`)
	body, _, res := syncedEntry(t, f, "capabilities", "CAP-4")
	requireNoMissing(t, res)
	require.Equal(t, []string{
		"id", "slug", "name", "status", "components", "related_epics", "related_capabilities",
		"created_at", "updated_at",
	}, entryKeys(t, body, "capabilities", "CAP-4"), "and still no file: key")
}

// TestSync_ReportsWhatTheRecordCannotSupply keeps sync's one rule: it copies
// and never invents. A required field the record has no value for — or has
// in a shape the schema does not accept — is left out and named.
func TestSync_ReportsWhatTheRecordCannotSupply(t *testing.T) {
	f := newFixture(t)
	f.raw("adrs", "adr-0007_thin.md", "---\nid: ADR-0007\ntitle: Thin\nstatus: proposed\n"+
		"type: technology\ntags: go\ncreated_at: \"\"\n---\n")
	body, entry, res := syncedEntry(t, f, "adrs", "ADR-0007")

	require.Len(t, res.Changes, 1)
	change := res.Changes[0]
	require.Equal(t, []string{"tags", "version", "created_at", "updated_at"}, change.Missing)
	require.Contains(t, change.Detail, "tags, version, created_at, updated_at")
	for _, key := range change.Missing {
		require.NotContains(t, entry, key, "sync must not invent %s", key)
	}
	require.NotContains(t, body, "tags: [go]", "a scalar is not promoted into a list")
	require.Equal(t, "thin", entry["slug"])
}

func TestSlugFromName(t *testing.T) {
	for name, want := range map[string]string{
		"adr-0005_use-go-stdlib.md":   "use-go-stdlib",
		"feat-1-2_name_with_under.md": "name_with_under",
		"cap-1_greeting.md":           "greeting",
		"patloc-0001_x.md":            "x",
		"no-separator.md":             "",
	} {
		require.Equal(t, want, slugFromName(name), name)
	}
}
