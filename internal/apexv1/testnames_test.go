package apexv1

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gotestsumLog is a gotestsum run's output, shaped like atomix's `make
// gotest`: package lines with ✓/✖, then the "=== FAIL: <pkg> <test>
// (<duration>)" summary with each failure's output.
func gotestsumLog(failing []string, dur string) string {
	var b strings.Builder
	b.WriteString("(re)installing /home/u/go/bin/gotestsum-v1.13.0\n")
	b.WriteString("✓  internal/store (1.204s)\n✖  tools/ci (" + dur + ")\n✖  internal/alert (" + dur + ")\n\n=== Failed\n")
	for _, n := range failing {
		fmt.Fprintf(&b, "=== FAIL: %s (0.00s)\n    x_test.go:40: expected the stack to be up\n\n", n)
	}
	fmt.Fprintf(&b, "DONE 812 tests, %d failures in 260.1s\n", len(failing))
	return b.String()
}

// R11, from atomix on rc.4: its tests run through gotestsum, whose report
// the parser did not read, so no names came out and the verdict fell back
// to the output hash. Its two logs hold 22 and 25 "=== FAIL:" lines whose
// name difference is exactly the 3 tests the lift broke.
func TestFailingTests_Gotestsum(t *testing.T) {
	atTag := make([]string, 0, 22)
	for i := range 20 {
		atTag = append(atTag, fmt.Sprintf("tools/ci TestG6_Guard%02d", i))
	}
	atTag = append(atTag, "internal/alert TestNeedsPostgres", "internal/alert TestNeedsPostgres/pg15")
	after := append([]string{
		"internal/alert TestAlertPackLevelsCarryEvidenceCitations/the_real_pack_passes",
		"internal/alert TestAlertPackLevelsCarryEvidenceCitations",
	}, atTag...)
	after = append(after[:12], append([]string{"tools/ci TestKnownUnwiredList"}, after[12:]...)...)
	require.Len(t, atTag, 22)
	require.Len(t, after, 25)

	tagOut, afterOut := gotestsumLog(atTag, "4m19s"), gotestsumLog(after, "4m02s")
	require.Equal(t, append(append([]string{}, "tools/ci", "internal/alert"), atTag...), failingTests(tagOut), "packages (✖) and tests (=== FAIL:), the duration dropped")

	fresh, named := newFailures(tagOut, afterOut)
	require.True(t, named)
	require.Equal(t, []string{
		"internal/alert TestAlertPackLevelsCarryEvidenceCitations/the_real_pack_passes",
		"internal/alert TestAlertPackLevelsCarryEvidenceCitations",
		"tools/ci TestKnownUnwiredList",
	}, fresh)

	v, note, names := judgeLiftRegression(&CheckResult{Exit: 2, Log: "t.log", OutputSHA256: "a"}, &CheckResult{Exit: 2, Log: "a.log", OutputSHA256: "b"}, tagOut, afterOut)
	require.Equal(t, LiftRegressionRedAtTagNewFailures, v)
	require.Equal(t, fresh, names)
	require.Contains(t, note, "3 failing test(s)")
}

// gotestsum's standard-verbose format also prints go test's "--- FAIL:"
// lines: the same tests, unqualified, read once (as "=== FAIL:"). ✖ reads
// only a package line: eslint's "✖ 3 problems (3 errors, 0 warnings)" is
// no name.
func TestFailingTests_GotestsumOnce(t *testing.T) {
	out := "=== RUN   TestA\n--- FAIL: TestA (0.00s)\nFAIL\n✖  pkg/a (12ms)\n✖  pkg/b [build failed]\n\n" +
		"=== FAIL: pkg/a TestA (0.00s)\n--- FAIL: TestA (0.00s)\n" +
		"✖ 3 problems (3 errors, 0 warnings)\n"
	require.Equal(t, []string{"pkg/a", "pkg/b [build failed]", "pkg/a TestA"}, failingTests(out))
}
