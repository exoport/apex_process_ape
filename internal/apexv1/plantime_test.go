package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.4 B6: a task whose own words edit a guarded config key is found by
// change check, and start refuses it; the two field cases are the eval's
// (cell 5u96wx2w T5, cell jie__sq1 T7). A task that only reads the config
// in its check, or names an unguarded key, is not one.
func TestCheckChange_GuardedConfigTask(t *testing.T) {
	p := fixture(t, baseFiles())
	tasks := "- T1 add wave — check: ./wave.sh\n" +
		"- T5 Set `regression: go test ./...` in `_apex/config.yaml` (project.md, Notes: \"M1's first change sets it\") — check: grep -qx 'regression: go test ./\\.\\.\\.' _apex/config.yaml\n" +
		"- T7 `_apex/config.yaml` sets `regression: go test ./... && go vet ./...` (a guarded config change a person accepts, as the roadmap's M1 Units say) — check: grep -Fqx 'regression: x' _apex/config.yaml\n" +
		"- T8 the docs name the config — check: grep -q 'timeouts:' _apex/config.yaml\n" +
		"- T9 add `docs: {changelog: CHANGELOG.md}` to _apex/config.yaml — check: grep -q changelog _apex/config.yaml\n" +
		"- T10 `regression:` in the README's example stays as written — check: grep -q regression README.md\n"
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave, tasks))
	commitAll(t, p, "plan: add-wave")

	fs := p.CheckChange("add-wave")
	var hit []string
	for _, f := range ClassifyFindings(fs) {
		if f.Code == CodeGuardedConfigTask {
			hit = append(hit, f.Message+" — "+f.Fix)
		}
	}
	require.Len(t, hit, 2, strings.Join(codesOf(fs), " "))
	require.Contains(t, hit[0], "task T5 edits a guarded config key (regression:) in _apex/config.yaml")
	require.Contains(t, hit[1], "task T7 edits a guarded config key (regression:)")
	require.Contains(t, hit[0], "leave the value under ## Notes for the person")

	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.Error(t, err, "start runs change check and refuses its findings")
	ds := Diagnostics(err)
	codes := make([]string, 0, len(ds))
	for _, d := range ds {
		codes = append(codes, d.Code)
	}
	require.Contains(t, codes, CodeGuardedConfigTask)
}

// v1.4 F4: a change whose paths: cover a gate file a person approved is
// found at plan time (verify refused it one apply later); ape status
// shows the frozen set.
func TestCheckChange_PathsCoverAnApprovedGate(t *testing.T) {
	files := baseFiles()
	files["development/gates/lib/common.sh"] = "x=1\n"
	files["gate.sh"] = "#!/bin/sh\n. development/gates/lib/common.sh\ntest -f done.flag\n"
	p := fixture(t, files)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Contains(t, recs["M1"].Approval.GateOwnFiles, "development/gates/lib/common.sh")

	doc := strings.Replace(waveChange("add-wave"), "touches: [greeting]\n", "touches: [greeting]\npaths: [wave.sh, development/gates/lib/*.sh]\n", 1)
	addChange(t, p, "add-wave", doc)
	commitAll(t, p, "plan: add-wave")
	fs := ClassifyFindings(p.CheckChange("add-wave"))
	var msgs []string
	for _, f := range fs {
		if f.Code == CodeTouchesGate {
			msgs = append(msgs, f.Message+" — "+f.Fix)
		}
	}
	require.Len(t, msgs, 1, "%v", fs)
	require.Contains(t, msgs[0], `paths: "development/gates/lib/*.sh" covers development/gates/lib/common.sh (M1's gate), a gate file a person approved`)

	st := p.Status()
	require.NotNil(t, st.Milestone.GateFrozen)
	require.Contains(t, st.Milestone.GateFrozen.Files, "development/gates/lib/common.sh")
	require.Contains(t, st.Milestone.GateFrozen.Files, "gate.sh")
	require.NotNil(t, st.Milestones[0].GateFrozen)
}
