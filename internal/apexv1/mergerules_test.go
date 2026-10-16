package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog item 8: the delta rules, as a matrix mirroring OpenSpec's
// validation and merge list (research/openspec-surface-map.md 2.5), minus
// what v1 leaves out (RENAMED, SHALL/length warnings — v1 has no warning
// tier). Each row is a change.md delta against the greeting fixture, and
// the finding ape change check must give (or none).
func TestDeltaRules_OpenSpecMatrix(t *testing.T) {
	sc := "#### Scenario: s\n- check: true\n"
	keepHello := "#### Scenario: plain run\n- check: ./greet.sh | grep -q Hello\n"
	cases := []struct {
		name, touches, retire, delta, want string
	}{
		{"duplicate within ADDED", "greeting", "", "### ADDED [greeting] Requirement: A\nx\n" + sc + "\n### ADDED [greeting] Requirement: A\nx\n" + sc, "appears twice in the delta"},
		{"same name in ADDED and MODIFIED", "greeting", "", "### MODIFIED [greeting] Requirement: Say hello\nx\n" + keepHello + "\n### ADDED [greeting] Requirement: Say hello\nx\n" + sc, "appears twice in the delta"},
		{"same name in MODIFIED and REMOVED", "greeting", "", "### MODIFIED [greeting] Requirement: Say name\nx\n" + sc + "\n### REMOVED [greeting] Requirement: Say name\nReason: r\n", "appears twice in the delta"},
		{"ADDED without a scenario", "greeting", "", "### ADDED [greeting] Requirement: A\nThe system SHALL.\n", "has no `#### Scenario:`"},
		{"MODIFIED without a scenario", "greeting", "", "### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL.\n", "has no `#### Scenario:`"},
		{"prose before the first delta (dropped at archive)", "greeting", "", "Some context.\n\n### ADDED [greeting] Requirement: A\nx\n" + sc, "text before the first delta block is not carried"},
		{"prose in a REMOVED block (dropped at archive)", "greeting", "", "### REMOVED [greeting] Requirement: Say name\nReason: r\nIt was never used.\n", "a REMOVED block carries only `Reason:`"},
		{"zero deltas, no none line", "greeting", "", "", "## Delta is empty"},
		{"MODIFIED scenario loss", "greeting", "", "### MODIFIED [greeting] Requirement: Say hello\nx\n" + sc, `drops scenario "plain run"`},
		{"MODIFIED target missing, near miss", "greeting", "", "### MODIFIED [greeting] Requirement: say  HELLO\nx\n" + keepHello, `did you mean "Say hello"?`},
		{"REMOVED target missing, typo", "greeting", "", "### REMOVED [greeting] Requirement: Say nam\nReason: r\n", `did you mean "Say name"?`},
		{"REMOVED target absent, no near miss", "greeting", "", "### REMOVED [greeting] Requirement: Teleport\nReason: r\n", "name it exactly"},
		{"ADDED already exists", "greeting", "", "### ADDED [greeting] Requirement: Say hello\nx\n" + sc, "already exists in specs/greeting"},
		{"ADDED near duplicate", "greeting", "", "### ADDED [greeting] Requirement: Say Hello\nx\n" + sc, `nearly matches the existing "Say hello"`},
		{"new capability accepts ADDED only", "cli", "", "### MODIFIED [cli] Requirement: Help\nx\n" + sc, "MODIFIED names \"Help\""},
		{
			"removing the last requirement needs retire", "greeting", "",
			"### REMOVED [greeting] Requirement: Say hello\nReason: r\n\n### REMOVED [greeting] Requirement: Say name\nReason: r\n", "add `retire: [greeting]`",
		},
		{"retire with requirements left", "greeting", "greeting", "### REMOVED [greeting] Requirement: Say name\nReason: r\n", "does not remove every one of its requirements"},
		{
			"retire, all removed", "greeting", "greeting",
			"### REMOVED [greeting] Requirement: Say hello\nReason: r\n\n### REMOVED [greeting] Requirement: Say name\nReason: r\n", "",
		},
		{"none with a reason", "greeting", "", "none — refactor", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			doc := changeDoc("rule-case", tc.touches, tc.delta, "- T1 x — check: true\n")
			if tc.retire != "" {
				doc = strings.Replace(doc, "---\n# ", "retire: ["+tc.retire+"]\n---\n# ", 1)
			}
			addChange(t, p, "rule-case", doc)
			got := findingMessages(p.CheckChange("rule-case"))
			if tc.want == "" {
				require.Empty(t, got)
				return
			}
			require.Contains(t, got, tc.want)
		})
	}
}

// Merge order: the spec's own requirement order is kept, ADDED appended.
func TestMergeDelta_KeepsOrderAndAppends(t *testing.T) {
	p := fixture(t, baseFiles())
	c := ParseChange("", changeDoc("x-y-z", "greeting",
		addedWave+"\n### MODIFIED [greeting] Requirement: Say name\nThe system SHALL greet by full name.\n#### Scenario: named\n- check: ./greet.sh Ann | grep -q Ann\n",
		"- T1 x — check: true\n"))
	updates, conflicts := p.MergeDelta(c)
	require.Empty(t, conflicts)
	reqs := ParseSpec(updates[0].content).Requirements
	names := make([]string, 0, len(reqs))
	for _, r := range reqs {
		names = append(names, r.Name)
	}
	require.Equal(t, []string{"Say hello", "Say name", "Wave"}, names)
}

// Retirement end to end: archive deletes the capability's spec.
func TestArchive_RetiresACapability(t *testing.T) {
	p := fixture(t, baseFiles())
	doc := changeDoc("drop-greeting", "greeting",
		"### REMOVED [greeting] Requirement: Say hello\nReason: replaced\n\n### REMOVED [greeting] Requirement: Say name\nReason: replaced\n",
		"- T1 x — check: true\n")
	doc = strings.Replace(doc, "---\n# ", "retire: [greeting]\n---\n# ", 1)
	addChange(t, p, "drop-greeting", doc)
	commitAll(t, p, "plan")
	_, err := p.Start(ctx(), "drop-greeting", RunOptions{})
	require.NoError(t, err)
	_, err = p.Verify(ctx(), "drop-greeting", RunOptions{})
	require.NoError(t, err)

	preview, err := p.PreviewDelta("drop-greeting")
	require.NoError(t, err)
	require.True(t, preview.Specs[0].Retired)
	require.Contains(t, preview.Specs[0].Diff, "+++ /dev/null")

	write(t, p, "development/changes/drop-greeting/review.md", "ok\n")
	_, err = p.Review(ctx(), "drop-greeting", "pass", "development/changes/drop-greeting/review.md")
	require.NoError(t, err)
	res, err := p.Archive(ctx(), "drop-greeting", false, RunOptions{})
	require.NoError(t, err)
	require.True(t, res.Specs[0].Retired)
	_, err = os.Stat(filepath.Join(p.SpecsDir(), "greeting"))
	require.True(t, os.IsNotExist(err))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"), "the deletion is committed")
	require.Empty(t, p.Check())
}

// Without retire:, the merge itself refuses to leave an empty spec (a
// change checked before another emptied the capability, say).
func TestMergeDelta_EmptySpecWithoutRetireIsAConflict(t *testing.T) {
	p := fixture(t, baseFiles())
	c := ParseChange("", changeDoc("drop-all", "greeting",
		"### REMOVED [greeting] Requirement: Say hello\nReason: r\n\n### REMOVED [greeting] Requirement: Say name\nReason: r\n",
		"- T1 x — check: true\n"))
	_, conflicts := p.MergeDelta(c)
	require.Len(t, conflicts, 1)
	require.Contains(t, conflicts[0], "add `retire: [greeting]`")
}

func TestEditDistanceAndNearMiss(t *testing.T) {
	require.Equal(t, 0, editDistance("abc", "abc"))
	require.Equal(t, 1, editDistance("say nam", "say name"))
	spec := ParseSpec(greetingSpec)
	require.Equal(t, "Say hello", nearMiss(spec, "SAY   hello"))
	require.Equal(t, "Say name", nearMiss(spec, "Say nme"))
	require.Empty(t, nearMiss(spec, "Teleport"))
	require.Empty(t, nearMiss(spec, "Say hello"), "an exact name is not a near miss")
}
