package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog item 11: a new capability's Purpose comes from a `Purpose:` line
// in its ADDED block — what the capability is for — rather than from the
// change's Why, which describes the work. Why stays the fallback.
func TestNewCapabilityPurpose(t *testing.T) {
	added := "### ADDED [cli] Requirement: Help\nPurpose: Lets people discover the greeter's options.\n\nThe system SHALL print help.\n#### Scenario: h\n- check: ./greet.sh --help\n"
	p := fixture(t, baseFiles())
	c := ParseChange("", changeDoc("add-cli", "cli", added, "- T1 x — check: true\n"))
	require.Equal(t, "Lets people discover the greeter's options.", c.Deltas[0].Purpose)
	require.NotContains(t, c.Deltas[0].Req.Text, "Purpose:", "the Purpose line is not requirement text")
	updates, conflicts := p.MergeDelta(c)
	require.Empty(t, conflicts)
	require.Contains(t, updates[0].content, "# Cli\nPurpose: Lets people discover the greeter's options.\n")
	require.Empty(t, ValidateSpec(updates[0].Path, updates[0].content, DefaultCaps().Spec))

	noPurpose := ParseChange("", changeDoc("add-cli", "cli",
		"### ADDED [cli] Requirement: Help\nThe system SHALL print help.\n#### Scenario: h\n- check: true\n", "- T1 x — check: true\n"))
	updates, _ = p.MergeDelta(noPurpose)
	require.Contains(t, updates[0].content, "Purpose: Because the greeter must change.", "falls back to Why")

	for name, tc := range map[string]struct{ delta, want string }{
		"existing capability": {"### ADDED [greeting] Requirement: Wave\nPurpose: Greets.\nx\n#### Scenario: w\n- check: true\n", "already has one"},
		"empty":               {"### ADDED [cli] Requirement: Help\nPurpose:\n\nx\n#### Scenario: h\n- check: true\n", "`Purpose:` is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			addChange(t, p, "add-x", changeDoc("add-x", "greeting, cli", tc.delta, "- T1 x — check: true\n"))
			require.Contains(t, findingMessages(p.CheckChange("add-x")), tc.want)
		})
	}
}

// Item 4 (Q44): a Purpose is a block — the `Purpose:` line and every line
// after it up to the first blank line, heading or fenced code. Read as one
// line, its second line stayed in the requirement and landed under the
// `### Requirement:` heading, in change diff and in the archived spec.
func TestNewCapabilityPurpose_MultiLine(t *testing.T) {
	added := "### ADDED [cli] Requirement: Help\n" +
		"Purpose: the greeter's command line — its flags, output streams and exit\n" +
		"statuses.\n" +
		"\n" +
		"The system SHALL print help.\n#### Scenario: h\n- check: ./help.sh\n"
	want := "# Cli\nPurpose: the greeter's command line — its flags, output streams and exit\nstatuses.\n\n### Requirement: Help\nThe system SHALL print help.\n"

	c := ParseChange("", changeDoc("add-cli", "cli", added, "- T1 help — check: ./help.sh\n"))
	require.Equal(t, "the greeter's command line — its flags, output streams and exit\nstatuses.", c.Deltas[0].Purpose)
	require.NotContains(t, c.Deltas[0].Req.Text, "statuses.", "the Purpose's second line is not requirement text")

	p := fixture(t, baseFiles())
	addChange(t, p, "add-cli", changeDoc("add-cli", "cli", added, "- T1 help — check: ./help.sh\n"))
	require.Empty(t, p.CheckChange("add-cli"))
	commitAll(t, p, "plan: add-cli")

	prev, err := p.PreviewDelta("add-cli")
	require.NoError(t, err)
	require.Len(t, prev.Specs, 1)
	require.Contains(t, prev.Specs[0].Diff, "+Purpose: the greeter's command line — its flags, output streams and exit\n+statuses.\n+\n+### Requirement: Help\n+The system SHALL print help.\n")

	_, err = p.Start(ctx(), "add-cli", RunOptions{})
	require.NoError(t, err)
	write(t, p, "help.sh", "#!/bin/sh\necho help\n")
	commitAll(t, p, "feat: help")
	v, err := p.Verify(ctx(), "add-cli", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	write(t, p, "development/changes/add-cli/review.md", "fine\n")
	_, err = p.Review(ctx(), "add-cli", "pass", "development/changes/add-cli/review.md")
	require.NoError(t, err)
	_, err = p.Archive(ctx(), "add-cli", false, RunOptions{})
	require.NoError(t, err)
	spec := read(t, p, "development/specs/cli/spec.md")
	require.True(t, strings.HasPrefix(spec, want), "archived spec:\n%s", spec)

	// The spec reader takes the same block.
	s := ParseSpec(spec)
	require.Equal(t, c.Deltas[0].Purpose, s.Purpose)
	require.Len(t, s.Requirements, 1)
	require.NotContains(t, s.Requirements[0].Text, "statuses.")
	require.Empty(t, ValidateSpec("development/specs/cli/spec.md", spec, DefaultCaps().Spec))
}

// The spec reader's Purpose block ends at a blank line, a heading or
// fenced code, and a Purpose: with its text on the next line is not empty.
func TestParseSpec_PurposeBlock(t *testing.T) {
	for name, tc := range map[string]struct{ doc, want string }{
		"one line":          {"# T\nPurpose: p\n\n### Requirement: R\n", "p"},
		"two lines":         {"# T\nPurpose: p\nq\n\n### Requirement: R\n", "p\nq"},
		"ends at heading":   {"# T\nPurpose: p\nq\n### Requirement: R\nx\n", "p\nq"},
		"ends at fence":     {"# T\nPurpose: p\n```\ncode\n```\n### Requirement: R\n", "p"},
		"text on next line": {"# T\nPurpose:\n  p\n\n### Requirement: R\n", "p"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, ParseSpec(tc.doc).Purpose)
		})
	}
}
