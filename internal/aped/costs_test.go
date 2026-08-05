//go:build linux || darwin

package aped

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// costsBackend returns the shared vmm fake carrying the named workspaces, so
// the reporter is exercised against the same List every other verb uses.
func costsBackend(t *testing.T, names ...string) *fakeBackend {
	t.Helper()
	b := newFakeBackend()
	for _, n := range names {
		if _, err := b.Create(context.Background(), workspace.CreateRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

// seedWorkspaceHome writes one assistant turn into the composed home aped would
// have created for name, at the exact path the guest's $HOME bind produces:
// <staging>/.claude/projects/<slug>/<sid>.jsonl.
func seedWorkspaceHome(t *testing.T, stateDir, name string, outputTokens int) {
	t.Helper()
	dir := filepath.Join(sandbox.StagingDirFor(stateDir, name), ".claude", "projects", "-workspace-app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"2026-08-01T10:00:00Z","sessionId":"s",` +
		`"message":{"id":"m-` + name + `","model":"claude-sonnet-4-5-20250929","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":100,"output_tokens":` + costsItoa(outputTokens) + `}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "sess.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func costsItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestWorkspaceCostsRollsUpEachHome(t *testing.T) {
	state := t.TempDir()
	seedWorkspaceHome(t, state, "alpha", 200)
	seedWorkspaceHome(t, state, "beta", 400)

	rep := &WorkspaceCosts{StateDir: state, Backend: costsBackend(t, "beta", "alpha")}
	got, err := rep.Costs(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(got.Workspaces))
	}
	// Sorted by name, so a report is stable across runs (List iterates a map).
	if got.Workspaces[0].Name != "alpha" || got.Workspaces[1].Name != "beta" {
		t.Errorf("order = %s, %s; want alpha, beta", got.Workspaces[0].Name, got.Workspaces[1].Name)
	}
	if got.Workspaces[0].Totals.OutputTokens != 200 || got.Workspaces[1].Totals.OutputTokens != 400 {
		t.Errorf("per-workspace attribution wrong: %+v", got.Workspaces)
	}
	if got.Workspaces[0].Sessions != 1 {
		t.Errorf("Sessions = %d, want 1", got.Workspaces[0].Sessions)
	}
	if got.Totals.OutputTokens != 600 {
		t.Errorf("node total = %d output tokens, want 600", got.Totals.OutputTokens)
	}
	if got.Totals.CostUSD <= 0 {
		t.Error("node total cost is 0 for a priced model")
	}
	if got.Workspaces[0].LastTurnAt == "" {
		t.Error("LastTurnAt is empty for a workspace with turns")
	}
}

func TestWorkspaceCostsNarrowsToOne(t *testing.T) {
	state := t.TempDir()
	seedWorkspaceHome(t, state, "alpha", 200)
	seedWorkspaceHome(t, state, "beta", 400)

	rep := &WorkspaceCosts{StateDir: state, Backend: costsBackend(t, "alpha", "beta")}
	got, err := rep.Costs(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workspaces) != 1 || got.Workspaces[0].Name != "beta" {
		t.Fatalf("got %+v, want only beta", got.Workspaces)
	}
}

func TestWorkspaceCostsUnknownNameIsNotFound(t *testing.T) {
	rep := &WorkspaceCosts{StateDir: t.TempDir(), Backend: costsBackend(t, "alpha")}
	_, err := rep.Costs(context.Background(), "nope")
	if !errors.Is(err, workspace.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A workspace whose composed home is missing must be REPORTED, not silently
// counted as costing nothing and not allowed to fail the other workspaces'
// numbers.
func TestWorkspaceCostsMissingHomeIsReportedNotZeroed(t *testing.T) {
	state := t.TempDir()
	seedWorkspaceHome(t, state, "alpha", 200)

	rep := &WorkspaceCosts{StateDir: state, Backend: costsBackend(t, "alpha", "ghost")}
	got, err := rep.Costs(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var ghost *workspace.Cost
	for i := range got.Workspaces {
		if got.Workspaces[i].Name == "ghost" {
			ghost = &got.Workspaces[i]
		}
	}
	if ghost == nil {
		t.Fatal("the workspace with no home was dropped from the report entirely")
	}
	if ghost.Error == "" {
		t.Error("a missing composed home reported no error — it would read as $0.00 of real work")
	}
	if got.Totals.OutputTokens != 200 {
		t.Errorf("node total = %d, want alpha's 200 — one bad home must not skew the sum", got.Totals.OutputTokens)
	}
}

// A workspace that exists but has never run Claude costs nothing, and that is a
// fact rather than an error.
func TestWorkspaceCostsUnusedWorkspaceIsZeroNotAnError(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(sandbox.StagingDirFor(state, "fresh"), 0o700); err != nil {
		t.Fatal(err)
	}
	rep := &WorkspaceCosts{StateDir: state, Backend: costsBackend(t, "fresh")}
	got, err := rep.Costs(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workspaces) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(got.Workspaces))
	}
	if got.Workspaces[0].Error != "" {
		t.Errorf("Error = %q, want none — the home exists, it is just unused", got.Workspaces[0].Error)
	}
	if got.Workspaces[0].Totals.NumTurns != 0 {
		t.Errorf("turns = %d, want 0", got.Workspaces[0].Totals.NumTurns)
	}
}

func TestWorkspaceCostsNoStateDirIsUnsupported(t *testing.T) {
	rep := &WorkspaceCosts{Backend: costsBackend(t)}
	_, err := rep.Costs(context.Background(), "")
	if !errors.Is(err, workspace.ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}
