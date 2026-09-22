package apecmd

import (
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// `ape story verify --help` states its exit table twice: once as the
// numbered block under the `--file` description, and once as the parenthesised
// summary on the flag line itself. They are read by the same caller in the
// same output, and up to v0.0.73 they disagreed — the block documented 0/2/3/4
// and the summary said "exit 0/2/3", so the body-shape gate's only verdict
// looked like an anomaly to anyone who read the shorter text.
//
// The framework eval hit it from the other side: a captured `story-batch-dev`
// run took a real exit 4 and first recorded it as an UNDOCUMENTED code.
//
// Asserted as set equality between the two texts rather than against a literal
// list, so a fifth code added to the block fails here until the summary names
// it too. A literal would have to be edited in three places instead of two,
// which is how the pair drifted in the first place.
func TestStoryVerify_FlagSummaryNamesEveryDocumentedExitCode(t *testing.T) {
	t.Parallel()

	cmd := newStoryVerifyCmd()
	flag := cmd.Flags().Lookup("file")
	require.NotNil(t, flag, "the --file flag is what the exit table belongs to")

	// The numbered block: lines of the form "  <n>  <meaning>".
	blockRe := regexp.MustCompile(`(?m)^\s+(\d)\s{2,}\S`)
	matches := blockRe.FindAllStringSubmatch(cmd.Long, -1)
	documented := make([]string, 0, len(matches))
	for _, m := range matches {
		documented = append(documented, m[1])
	}
	require.NotEmpty(t, documented,
		"the long help must carry the numbered exit block this test compares against")

	summarised := regexp.MustCompile(`\d`).FindAllString(flag.Usage, -1)

	require.Equal(t, uniqueSorted(documented), uniqueSorted(summarised),
		"the --file summary (%q) must name exactly the exit codes the long help documents",
		flag.Usage)
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// The guard above is only worth having if it fails when the pair drifts.
// Exercised on a copy so the real command is untouched.
func TestStoryVerify_TheExitCodeGuardCatchesDrift(t *testing.T) {
	t.Parallel()

	cmd := newStoryVerifyCmd()
	blockRe := regexp.MustCompile(`(?m)^\s+(\d)\s{2,}\S`)
	documented := blockRe.FindAllStringSubmatch(cmd.Long, -1)
	require.NotEmpty(t, documented)

	// The pre-fix summary: stops at 3 while the block documents 4.
	stale := "Verify one story file as a gate (exit 0/2/3)"
	codes := make([]string, 0, len(documented))
	for _, m := range documented {
		codes = append(codes, m[1])
	}
	require.NotEqual(t, uniqueSorted(codes),
		uniqueSorted(regexp.MustCompile(`\d`).FindAllString(stale, -1)),
		"the guard must reject the exact summary that shipped in v0.0.73")

	require.Contains(t, cmd.Long, "4",
		"sanity: the long help really does document a code the stale summary omitted")
}
