package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// D210: `ape decision standard [--check] [--json]` — the verb the
// framework's seq-08 and each release that changes the standard run.
func TestDecisionStandard_CLI(t *testing.T) {
	root := v1Repo(t)
	stdout, _, err := v1Exec(t, newDecisionCmd, "standard", "--check", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var res apexv1.StandardADRResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, apexv1.StandardKept, res.Action, "init wrote the standard")

	// A release that changes the standard.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "standards"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(apexv1.StandardADRPayload)), []byte(
		"# ADR-0001: Record architecture decisions\nStatus: accepted\nDate: 2026-10-21\n\n## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: ape project check\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "framework.yaml"), []byte("framework:\n  version_tag: v1.0.0-rc.9\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "chore(apex): update APEX framework to v1.0.0-rc.9")

	stdout, _, err = v1Exec(t, newDecisionCmd, "standard", "--check", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "decision.standard-pending"`)
	require.Contains(t, stdout, `"action": "replaced"`)

	stdout, _, err = v1Exec(t, newDecisionCmd, "standard", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "development/decisions/0001-record-architecture-decisions.md replaced by APEX's standard ADR")
	require.Equal(t, "apex: ADR-0001 standard v1.0.0-rc.9", v1Git(t, root, "log", "-1", "--format=%s"))
	stdout, _, err = v1Exec(t, newDecisionCmd, "standard", "--check", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "nothing due")
}

// rc.7d (rc7d-std): D232 Q131 — on pdfa's shape the verb lists the lasting
// scenarios whose check names what it removed, in its output and JSON
// (stale_scenarios), and project check shows spec.check-names-removed
// (action replan, exit unchanged).
func TestDecisionStandard_StaleScenariosCLI(t *testing.T) {
	root := v1Repo(t)
	dec := filepath.Join(root, "development", "decisions")
	require.NoError(t, os.Remove(filepath.Join(dec, apexv1.StandardADRFile())))
	require.NoError(t, os.WriteFile(filepath.Join(dec, "0001-use-markdown-adrs.md"), []byte("# ADR-0001: Use Markdown Architectural Decision Records\nStatus: superseded by ADR-0031\nDate: 2026-10-11\n"+
		"Lifted from: ADR-0001, development/governance/adrs/adr-0001_use-markdown-adrs.md at apex-v0-final\n\n## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: review\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dec, "0031-use-v1-decision-format.md"), []byte("# ADR-0031: Record decisions in the v1 ADR format\nStatus: proposed\nDate: 2026-10-19\nSupersedes: ADR-0001\n\n"+
		"## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: ape project check\n"), 0o644))
	spec := filepath.Join(root, "development", "specs", "agent-context")
	require.NoError(t, os.MkdirAll(spec, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(spec, "spec.md"), []byte("# Agent context\nPurpose: Agent rules.\n\n### Requirement: Decisions use the format\nThe system SHALL.\n\n"+
		"#### Scenario: the superseding ADR states the format\n- GIVEN x WHEN y THEN z\n- check: f=development/decisions/0031-use-v1-decision-format.md; grep -q x \"$f\"\n\n"+
		"#### Scenario: no stray ADR-0031\n- GIVEN x WHEN y THEN z\n- check: ! test -e development/decisions/0031-use-v1-decision-format.md\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "as the rc.7 lift left pdfa")

	stdout, _, err := v1Exec(t, newDecisionCmd, "standard", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, `stale scenario development/specs/agent-context/spec.md:7: scenario "the superseding ADR states the format" of requirement "Decisions use the format": its check names development/decisions/0031-use-v1-decision-format.md, which `+"`ape decision standard`"+` (the framework's migration v1.0.0_seq-08`)
	require.NotContains(t, stdout, "no stray ADR-0031", "an absence check is not stale")

	stdout, _, err = v1Exec(t, newDecisionCmd, "standard", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var res struct {
		Action         string                 `json:"action"`
		StaleScenarios []apexv1.StaleScenario `json:"stale_scenarios"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, apexv1.StandardKept, res.Action)
	require.Len(t, res.StaleScenarios, 1)
	require.Equal(t, apexv1.StaleScenario{
		Spec: "development/specs/agent-context/spec.md", Requirement: "Decisions use the format",
		Scenario: "the superseding ADR states the format", Line: 7, Names: []string{"development/decisions/0031-use-v1-decision-format.md"},
	}, res.StaleScenarios[0])

	// The toy project's own findings decide project check's exit, not the
	// notice.
	stdout, _, _ = v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	var chk struct {
		Notices []apexv1.Diagnostic `json:"notices"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &chk), stdout)
	var stale []apexv1.Diagnostic
	for _, n := range chk.Notices {
		if n.Code == apexv1.CodeSpecCheckNamesRemoved {
			stale = append(stale, n)
		}
	}
	require.Len(t, stale, 1, stdout)
	require.Equal(t, apexv1.ActionReplan, stale[0].Action)
	require.Equal(t, 7, stale[0].Line)
}

// rc.7d: the red line of a gate or verify carries the pointer (Q131).
func TestPrintChecks_StalePointer(t *testing.T) {
	var b strings.Builder
	printChecks(&b, []apexv1.CheckResult{{
		Task: "agent-context/R/S", Kind: apexv1.KindScenario, Requirement: "R", Cmd: "test -f x", Exit: 1,
		StaleNames: []string{"development/decisions/0031-use-v1-decision-format.md"},
	}}, false)
	require.Contains(t, b.String(), "       stale (spec.check-names-removed): its check names development/decisions/0031-use-v1-decision-format.md, which `ape decision standard`")
	require.Contains(t, b.String(), `retire it with a change whose REMOVED delta of requirement "R" cites the migration v1.0.0_seq-08`)
}
