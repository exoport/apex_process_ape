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

// flutterLog is a `flutter test` run's output in a non-interactive shell,
// shaped like spirometer's `make spiroapp-flutter-test`: the compact
// reporter's one line per update, "<mm:ss> +<passed> [~<skipped>]
// -<failed>: <name>", a failure's ending in " [E]", and make's error.
// passed shifts the counters, as two runs' timing and order do.
func flutterLog(failing []string, passed int) string {
	var b strings.Builder
	b.WriteString("fvm flutter test\n00:00 +0: loading /home/u/spiro/spiroapp/test/leftover_test.dart\n")
	for i, n := range failing {
		fmt.Fprintf(&b, "\x1b[0;31m00:01 +%d ~2 -%d: %s [E]\x1b[0m\n  Expected: <true>\n    Actual: <false>\n\n", passed+i, i+1, n)
		fmt.Fprintf(&b, "00:01 +%d ~2 -%d: /home/u/spiro/spiroapp/test/widget_test.dart: renders\n", passed+i+1, i+1)
	}
	fmt.Fprintf(&b, "00:02 +%d ~2 -%d: Some tests failed.\nmake: *** [Makefile:88: spiroapp-flutter-test] Error 1\n", passed+len(failing), len(failing))
	return b.String()
}

// R18, from spirometer2 on rc.5: `make spiroapp-flutter-test` failed the
// same 15 tests at both points, but no name was read and the counters
// changed the hash, so the lift said "the output changed". Flutter's and
// dart's "[E]" lines are read as names: the same 15 are pre-existing, a
// 16th is the lift's.
func TestFailingTests_Flutter(t *testing.T) {
	failing := []string{"/home/u/spiro/spiroapp/test/leftover_test.dart: (setUpAll)"}
	for i := range 14 {
		failing = append(failing, fmt.Sprintf("/home/u/spiro/spiroapp/test/screen_%02d_test.dart: Screen %d shows the trace", i, i))
	}
	tagOut, afterOut := flutterLog(failing, 121), flutterLog(failing, 118)
	require.Equal(t, failing, failingTests(tagOut), "the [E] lines, counters and colour dropped; no other reporter line")
	require.Equal(t, []string{"test/a_test.dart: group b"}, failingTests("00:03 +4 -1: test/a_test.dart: group b [E]\r\n00:03 +4 -1: Some tests failed.\r\n"), "dart test, no skips, CRLF")

	tag, after := &CheckResult{Exit: 2, Log: "t.log", OutputSHA256: "a"}, &CheckResult{Exit: 2, Log: "a.log", OutputSHA256: "b"}
	v, note, names := judgeLiftRegression(tag, after, tagOut, afterOut)
	require.Equal(t, LiftRegressionRedAtTag, v)
	require.Empty(t, names)
	require.Contains(t, note, "failing as it did there")

	extra := "/home/u/spiro/spiroapp/test/sprint_status_test.dart: reads the tracker"
	v, note, names = judgeLiftRegression(tag, after, tagOut, flutterLog(append(append([]string{}, failing...), extra), 118))
	require.Equal(t, LiftRegressionRedAtTagNewFailures, v)
	require.Equal(t, []string{extra}, names)
	require.Contains(t, note, "1 failing test(s)")
}
