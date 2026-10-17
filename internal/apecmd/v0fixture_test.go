package apecmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// v0 project fixtures for the doctor checks that still read a v0 project
// (the commands that wrote these records were retired with APEX v0).

// allExtensionsConfig enables all four families so the registry commands
// have something to look at.
const allExtensionsConfig = `config_schema_version: "1"
project_name: fx
extensions: [ext-adrs, ext-patterns, ext-features, ext-capabilities]
development_folder: development
implementation_folder: development/implementation
governance_folder: development/governance
functionality_folder: development/functionality
`

// seedADRCorpus writes n ADR records and an index listing all but the
// ids in skip — the ADR-0050 shape.
func seedADRCorpus(t *testing.T, root string, n int, skip ...int) {
	t.Helper()
	dir := filepath.Join(root, "development", "governance", "adrs")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "changelog"), 0o755))
	skipped := map[int]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var index strings.Builder
	index.WriteString("generated_at: '20260821120000'\nadrs:\n")
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("ADR-%04d", i)
		name := fmt.Sprintf("adr-%04d_decision.md", i)
		body := fmt.Sprintf("---\nid: %s\ntitle: Decision %s\nstatus: accepted\ntype: process\ntags: [x]\n"+
			"version: v1\ncreated_at: \"20260101000000\"\nupdated_at: \"20260101000000\"\n---\n\n## Context\n\nx\n", id, id)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
		// Every record gets a changelog sidecar, as the real corpus does:
		// 54 of them, none of which may count as a record.
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "changelog", fmt.Sprintf("adr-%04d_decision_changelog.yaml", i)),
			[]byte("entries: []\n"), 0o644,
		))
		if skipped[i] {
			continue
		}
		fmt.Fprintf(&index, "  - id: %s\n    slug: decision\n    file: %s\n    status: accepted\n    type: process\n"+
			"    tags: [x]\n    version: v1\n    created_at: '20260101000000'\n    updated_at: '20260101000000'\n"+
			"    title: Decision %s\n", id, name, id)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(index.String()), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# ADRs\n"), 0o644))
}

const legacyLedgerFixture = "# Deferred Work\n\n" +
	"## Deferred from: story review of 54-1 (2026-08-21)\n\n" +
	"- [ ] [Defer] First thing [pkg/a.go:1] — defer: owner=platform ; trigger: story 54-2 lands\n" +
	"- [Defer] Second thing [pkg/b.go:2] — defer: owner=data\n\n" +
	"A pasted paragraph with no marker at all, which must survive.\n"

// writeLegacyLedger drops legacyLedgerFixture where the config resolves
// the legacy file.
func writeLegacyLedger(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "development", "implementation")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "deferred-work.md")
	require.NoError(t, os.WriteFile(path, []byte(legacyLedgerFixture), 0o644))
}
