package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// v1.4 (B10, ANSWER-W6): the check reads only the project's own
// <dev>/lift-disposition.md once lifted; a tracked fixture elsewhere does
// not count (D467); a staged rm counts, an untracked copy does not; an
// unlifted v1 project is applied (lift.not-v0).
func TestLiftDispositionCheck(t *testing.T) {
	p := fixture(t, baseFiles())
	require.NoError(t, p.LiftDispositionCheck(ctx()), "not lifted, v1 already: lift.not-v0, applied")

	write(t, p, "development/lift-disposition.md", "# Lift disposition\n")
	write(t, p, "testdata/lift-disposition.md", "# a fixture\n")
	commitAll(t, p, "apex: lift v0.31.0")
	require.Equal(t, CodeLiftDispositionLeft, failCode(t, p.LiftDispositionCheck(ctx())))

	run(t, p.Root, "rm", "-q", "--cached", "development/lift-disposition.md")
	require.NoError(t, p.LiftDispositionCheck(ctx()), "a staged rm counts as removed; the untracked copy left does not count")
	run(t, p.Root, "commit", "-q", "-m", "chore: disposed")
	require.FileExists(t, filepath.Join(p.Root, "testdata", "lift-disposition.md"))
	require.NoError(t, p.LiftDispositionCheck(ctx()), "a tracked fixture elsewhere does not count (D467)")

	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "development/lift-disposition.md", "# back\n")
	commitAll(t, p, "docs: back")
	require.Equal(t, CodeLiftDispositionLeft, failCode(t, p.LiftDispositionCheck(ctx())), "a later project init does not unlift it")

	q := &Project{Root: t.TempDir(), DevRel: "development"}
	require.NoError(t, os.MkdirAll(q.Root, 0o755))
	require.Equal(t, CodeLiftCheckFailed, failCode(t, q.LiftDispositionCheck(ctx())), "not a repository: cannot tell")
}
