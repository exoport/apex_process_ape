package apexv1

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// noticeCodes are the codes of project check's notices (D155 Q73): the
// only ones published with exit 0, as a notice never fails.
var noticeCodes = []string{CodeGateOutsideGates, CodeHooksPreCommitAbsent}

// The published list and the code agree: every code the source sets or a
// finding rule yields is published with one exit, and every published code
// is used (the unclassified fallbacks aside).
func TestDiagnosticCodes_PublishedListMatchesTheCode(t *testing.T) {
	published := map[string]int{}
	for _, c := range DiagnosticCodes {
		_, dup := published[c.Code]
		require.False(t, dup, "duplicate published code %s", c.Code)
		if c.Exit == 0 {
			require.Contains(t, noticeCodes, c.Code, "only a notice's code comes with exit 0")
		} else {
			require.Contains(t, []int{1, 2, 3}, c.Exit, c.Code)
		}
		require.NotEmpty(t, c.Meaning, c.Code)
		require.Contains(t, Actions, c.Action, c.Code)
		published[c.Code] = c.Exit
	}
	used := map[string]bool{}
	codeRe := regexp.MustCompile(`Code:\s+"([a-z0-9.-]+)"`)
	constRe := regexp.MustCompile(`Code:\s+(Code[A-Za-z]+)`)
	consts := map[string]string{
		"CodeWrongStatus": CodeWrongStatus, "CodeCommitFailed": CodeCommitFailed,
		"CodeMilestoneNotFound": CodeMilestoneNotFound, "CodeMilestoneNotApproved": CodeMilestoneNotApproved,
		"CodeFolderExists": CodeFolderExists, "CodeDecisionNotFound": CodeDecisionNotFound, "CodeDeltaConflict": CodeDeltaConflict,
		"CodeWrongBranch": CodeWrongBranch, "CodeChangeBranch": CodeChangeBranch, "CodeBranchFailed": CodeBranchFailed,
		"CodeNothingToRebaseline": CodeNothingToRebaseline, "CodeGitStatusFailed": CodeGitStatusFailed,
		"CodeConflicted": CodeConflicted, "CodeUnresolvedConflict": CodeUnresolvedConflict, "CodeNotApproved": CodeNotApproved, "CodeCheckFailed": CodeCheckFailed, "CodeVacuousCheck": CodeVacuousCheck,
		"CodePersonOnly": CodePersonOnly, "CodeSessionDepth": CodeSessionDepth, "CodeSessionNotAllowed": CodeSessionNotAllowed,
		"CodeChildRunning": CodeChildRunning, "CodeOrchestratorRunning": CodeOrchestratorRunning, "CodeBaseCheckedOut": CodeBaseCheckedOut,
		"CodeBehindUpstream": CodeBehindUpstream, "CodeHookFailed": CodeHookFailed,
		"CodeArchiveInterrupted": CodeArchiveInterrupted, "CodeRunInterrupted": CodeRunInterrupted, "CodeRunBusy": CodeRunBusy,
		"CodeRefMoved":         CodeRefMoved,
		"CodeStatusUnrecorded": CodeStatusUnrecorded, "CodeGateChanged": CodeGateChanged, "CodeDecisionUnrecorded": CodeDecisionUnrecorded,
		"CodeAttemptsTotalExceeded": CodeAttemptsTotalExceeded, "CodeReplansExceeded": CodeReplansExceeded, "CodeBlocksExceeded": CodeBlocksExceeded,
		"CodeChangeNotFound": CodeChangeNotFound, "CodeStateUnreadable": CodeStateUnreadable,
		"CodeUnsupportedLayout": CodeUnsupportedLayout, "CodeTagNotPushed": CodeTagNotPushed, "CodeLiftWriteFailed": CodeLiftWriteFailed, "CodeClaudeIgnored": CodeClaudeIgnored,
		"CodeCapsOverCeiling": CodeCapsOverCeiling, "CodeGateGreen": CodeGateGreen, "CodeGateBroken": CodeGateBroken,
		"CodeNoSuchTask": CodeNoSuchTask, "CodeNoChangeMD": CodeNoChangeMD, "CodeDelegateInvalidConfig": CodeDelegateInvalidConfig,
		// D89 (rc.4).
		"CodeChangeOverCeiling": CodeChangeOverCeiling,
		// rc.5 (rc5-d).
		"CodeModelsUnknownSkill": CodeModelsUnknownSkill, "CodeModelsUnknownField": CodeModelsUnknownField,
		"CodeMalformedHumanReview": CodeMalformedHumanReview,
		// rc.6 (rc6-e).
		"CodeGateOutsideGates": CodeGateOutsideGates, "CodeGateArchiveExists": CodeGateArchiveExists, "CodeHotNotIgnored": CodeHotNotIgnored,
		// rc.6 (rc6-g).
		"CodeCommitSecret": CodeCommitSecret,
		// rc.6e (rc6e-sec).
		"CodeHooksPreCommitAbsent": CodeHooksPreCommitAbsent,
	}
	for _, glob := range []string{"*.go", filepath.Join("..", "apecmd", "v1_*.go")} {
		files, err := filepath.Glob(glob)
		require.NoError(t, err)
		for _, f := range files {
			if regexp.MustCompile(`_test\.go$`).MatchString(f) {
				continue
			}
			data, err := os.ReadFile(f)
			require.NoError(t, err)
			for _, m := range codeRe.FindAllStringSubmatch(string(data), -1) {
				used[m[1]] = true
			}
			for _, m := range constRe.FindAllStringSubmatch(string(data), -1) {
				code, ok := consts[m[1]]
				require.True(t, ok, "unknown code constant %s in %s", m[1], f)
				used[code] = true
			}
		}
	}
	for _, r := range findingRules {
		used[r.code] = true
	}
	for code := range used {
		require.Contains(t, published, code, "code %s is used but not published in DiagnosticCodes", code)
	}
	for code := range published {
		if regexp.MustCompile(`\.unclassified$`).MatchString(code) {
			continue
		}
		require.True(t, used[code], "published code %s is used nowhere", code)
	}
}

func TestDiagnostics(t *testing.T) {
	d := Diagnostics(&FailError{Code: "verify.red", Msg: "change x: 1 check red — fix the code", Findings: []Finding{
		{File: "development/changes/x/change.md", Line: 3, Message: "REMOVED needs a `Reason:` line"},
	}})
	require.Equal(t, []Diagnostic{
		{Severity: "error", Code: "verify.red", Message: "change x: 1 check red", Fix: "fix the code", Action: "apply"},
		{Severity: "error", Code: "change.removed-no-reason", Message: "REMOVED needs a `Reason:` line", Action: "replan", File: "development/changes/x/change.md", Line: 3},
	}, d)
	d = Diagnostics(&ConflictError{Code: "archive.delta-conflict", Msg: "the delta no longer applies", Conflicts: []string{"a", "b"}})
	require.Len(t, d, 3)
	require.Equal(t, "archive.delta-conflict", d[2].Code)
	require.Equal(t, "usage.unclassified", Diagnostics(&UsageError{Msg: "x"})[0].Code)
}
