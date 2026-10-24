package apexv1

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// The alias table (D512): each renamed code is published, with one row in
// DiagnosticCodes; its former name is not. A former name has no producer of
// its own — no source sets it as a Code (the published-list test fails on
// an unpublished code a source sets) — so it reaches a reader only as the
// alias of the new code's diagnostics and as PublishedCodes' generated row,
// which is why TestDiagnosticCodes_PublishedListMatchesTheCode, reading
// DiagnosticCodes, never meets it, and needs no producer for it.
func TestCodeAliases_Table(t *testing.T) {
	published := map[string]CodeDoc{}
	for _, c := range DiagnosticCodes {
		published[c.Code] = c
	}
	seen := map[string]bool{}
	for _, a := range CodeAliases {
		require.Contains(t, published, a.Code, "a renamed code is published")
		require.NotContains(t, published, a.Alias, "a former name is no row of DiagnosticCodes")
		require.False(t, seen[a.Alias], "alias %s twice", a.Alias)
		seen[a.Alias] = true
		require.NotEmpty(t, a.Until, a.Alias)
		require.Equal(t, a.Code, CanonicalCode(a.Alias))
		require.Equal(t, a.Alias, AliasOf(a.Code))
		require.Equal(t, published[a.Code].Action, ActionFor(a.Alias), "a former name's action is the new code's")
	}
	require.Equal(t, "session.orchestrator-running", AliasOf(CodeLoopRunning))
	require.Equal(t, "models.operator-row", AliasOf(CodeModelsOrchestratorRow))
	require.Equal(t, "session.loop-running", CodeLoopRunning)
	require.Equal(t, "models.orchestrator-row", CodeModelsOrchestratorRow)
	require.Empty(t, AliasOf(CodeChildRunning))
	require.Equal(t, CodeChildRunning, CanonicalCode(CodeChildRunning))
	require.Empty(t, FormerlyNote(CodeChildRunning))
	require.Equal(t, "; formerly session.orchestrator-running", FormerlyNote(CodeLoopRunning))
}

// `ape project codes`' rows: the new code's row lists its aliases and is
// followed by the former name's own row, same exit and action, pointing at
// it; every other row is DiagnosticCodes' unchanged.
func TestPublishedCodes_AliasRows(t *testing.T) {
	rows := PublishedCodes()
	require.Len(t, rows, len(DiagnosticCodes)+len(CodeAliases))
	idx := func(code string) int {
		return slices.IndexFunc(rows, func(r PublishedCode) bool { return r.Code == code })
	}
	for _, c := range []struct{ code, alias string }{
		{"session.loop-running", "session.orchestrator-running"},
		{"models.orchestrator-row", "models.operator-row"},
	} {
		i, j := idx(c.code), idx(c.alias)
		require.GreaterOrEqual(t, i, 0, c.code)
		require.Equal(t, i+1, j, "the former name's row follows the new code's")
		require.Equal(t, []string{c.alias}, rows[i].Aliases)
		require.Empty(t, rows[i].RenamedTo)
		require.Equal(t, c.code, rows[j].RenamedTo)
		require.Equal(t, rows[i].Exit, rows[j].Exit)
		require.Equal(t, rows[i].Action, rows[j].Action)
		require.Contains(t, rows[j].Meaning, "renamed to `"+c.code+"`; kept as an alias until v1.5")
	}
	require.Equal(t, 3, rows[idx("session.loop-running")].Exit)
	require.Equal(t, ActionRetry, rows[idx("session.loop-running")].Action)
	require.Equal(t, 0, rows[idx("models.orchestrator-row")].Exit)
	require.Equal(t, ActionPerson, rows[idx("models.orchestrator-row")].Action)

	data, err := json.Marshal(rows)
	require.NoError(t, err)
	var raw []map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Equal(t, []any{"session.orchestrator-running"}, raw[idx("session.loop-running")]["aliases"])
	require.Equal(t, "session.loop-running", raw[idx("session.orchestrator-running")]["renamed_to"])
	require.NotContains(t, raw[idx(CodeChildRunning)], "aliases", "a code never renamed has no aliases key")
	require.NotContains(t, raw[idx(CodeChildRunning)], "renamed_to")
}

// A second apex-loop run's refusal: code session.loop-running, alias the
// former name, exit 3 and action retry as before; JSON carries both.
func TestLoopRunning_CarriesItsAlias(t *testing.T) {
	ds := Diagnostics(SessionBusy(false, "apex-loop, pid 1"))
	require.Len(t, ds, 1)
	require.Equal(t, "session.loop-running", ds[0].Code)
	require.Equal(t, "session.orchestrator-running", ds[0].Alias)
	require.Equal(t, ActionRetry, ds[0].Action)
	data, err := json.Marshal(ds[0])
	require.NoError(t, err)
	require.Contains(t, string(data), `"code":"session.loop-running"`)
	require.Contains(t, string(data), `"alias":"session.orchestrator-running"`)

	child := Diagnostics(SessionBusy(true, "apex-apply, pid 1"))
	require.Empty(t, child[0].Alias)
	data, err = json.Marshal(child[0])
	require.NoError(t, err)
	require.NotContains(t, string(data), `"alias"`, "omitted for a code never renamed")

	// A diagnostic built by hand still marshals its alias.
	data, err = json.Marshal(Diagnostic{Code: CodeLoopRunning})
	require.NoError(t, err)
	require.Contains(t, string(data), `"alias":"session.orchestrator-running"`)
}

// project check's notice for a models.apex-orchestrator row: code
// models.orchestrator-row, alias models.operator-row.
func TestModelsOrchestratorRow_CarriesItsAlias(t *testing.T) {
	p := rc6eProject(t, rc6eConfig, rc6eAgents)
	ns := p.operatorRowNotices()
	require.Len(t, ns, 1)
	require.Equal(t, "models.orchestrator-row", ns[0].Code)
	require.Equal(t, "models.operator-row", ns[0].Alias)
	require.Equal(t, SeverityNotice, ns[0].Severity)
	require.Equal(t, ActionPerson, ns[0].Action)
	data, err := json.Marshal(ns[0])
	require.NoError(t, err)
	require.Contains(t, string(data), `"alias":"models.operator-row"`)
}
