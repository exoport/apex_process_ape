package apexv1

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// L6, from radix and tenax: a lock file of SHA-256s that CI checks, and
// tests that hash their own package's sources, went red after the lift.
// The dry run names those edited files before anything is written.
func TestLift_DryRunListsHashedEdits(t *testing.T) {
	root := liftFixture(t, true)
	for rel, content := range map[string]string{
		"internal/timer/timer.go":          "package timer\n\n// Story 1.1: the timer\nvar T = 1\n",
		"internal/timer/timer_test.go":     "package timer\n\nfunc TestPackageSourceByteUnchanged(t *testing.T) {\n\tfiles, _ := filepath.Glob(\"*.go\")\n\t_ = sha256.New()\n}\n",
		"internal/awake/resolver.go":       "package awake\n\n// Story 1.1: resolve\nvar R = 1\n",
		"internal/awake/other.go":          "package awake\n\n// Story 1.1: other\nvar O = 1\n",
		"internal/awake/sum_test.go":       "package awake\n\n// pins \"resolver.go\" by its sha256\n",
		"tools/ci/byte_identity.lock.yaml": "files:\n  deploy/init/00-bootstrap.sh: 9f2c…\n  modules/core/*: 1a2b…\n",
		"deploy/init/00-bootstrap.sh":      "#!/bin/sh\n# Story 1.1: bootstrap\necho hi\n",
		"modules/core/module.yaml":         "# Story 1.1: the core module\nname: core\n",
		".github/workflows/ci.yml":         "# Epic 1: CI\njobs:\n  build:\n    steps:\n      - run: go build ./internal/timer/\n      - run: sha256sum dist/*\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "perimeter")
	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []LiftHashed{
		{File: "deploy/init/00-bootstrap.sh", By: []string{"tools/ci/byte_identity.lock.yaml"}},
		{File: "modules/core/module.yaml", By: []string{"tools/ci/byte_identity.lock.yaml"}},
		{File: "internal/awake/resolver.go", By: []string{"internal/awake/sum_test.go"}},
		{File: "internal/timer/timer.go", By: []string{"internal/timer/timer_test.go"}},
	}, res.Hashed, "what the lock names first; not other.go (its test names resolver.go only), not main.go, not .github (a hash 5 lines off the path)")

	// Excluded, they are not edited, so not listed.
	res, err = Lift(ctx(), root, LiftOptions{DryRun: true, Exclude: []string{"internal/timer/*.go", "internal/awake/resolver.go", "deploy/**", "modules/**"}})
	require.NoError(t, err)
	require.Empty(t, res.Hashed)
}

// R1, from radix, mdpdf and pdfa on rc.3: 228 "hashed" files against 2
// real. Evidence documents quoting a sha256 beside the paths they record
// hash nothing; what a lock file names comes first; the list is capped.
func TestLift_HashedSkipsRecordsLocksFirstCapped(t *testing.T) {
	root := liftFixture(t, true)
	files := map[string]string{
		"internal/awake/resolver.go": "package awake\n\n// Story 1.1: resolve\nvar R = 1\n",
		// Records: DR evidence (radix's tools/dr/evidence/), a guard's
		// .txt beside a test, a .json dump under tools/.
		"tools/dr/evidence/restore_driver.md": "# Restore\n\nsha256 of internal/awake/resolver.go: 9f2c\n",
		"tools/dr/evidence/run.lock.yaml":     "internal/awake/resolver.go: 9f2c\n",
		"tests/hashes.txt":                    "sha256 internal/awake/resolver.go 9f2c\n",
		"tools/ci/sums.json":                  "{\"internal/awake/resolver.go\": \"sha256:9f2c\"}\n",
		// The real perimeter, named by a lock file that sorts last.
		"zz/last.yaml":                     "# Story 1.1: the last module\nname: last\n",
		"tools/ci/byte_identity.lock.yaml": "files:\n  zz/last.yaml: 1a2b\n",
		// 60 files beside a guard test that globs them (mdpdf's shape).
		"pkg/many/guard_test.go": "package many\n\nfunc TestBytes(t *testing.T) {\n\tfiles, _ := filepath.Glob(\"*.go\")\n\t_ = sha256.New()\n}\n",
	}
	for i := range 60 {
		files[fmt.Sprintf("pkg/many/f%02d.go", i)] = "package many\n\n// Story 1.1: part\nvar X = 1\n"
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "perimeter")
	run(t, root, "tag", "-f", "-a", LiftTag, "-m", "APEX v0 final")
	run(t, root, "push", "-q", "-f", "origin", LiftTag)
	res, err := Lift(ctx(), root, LiftOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, 61, res.HashedTotal, "zz/last.yaml and the 60 beside the guard; never resolver.go, which only records name")
	require.Len(t, res.Hashed, hashedCap)
	require.Equal(t, LiftHashed{File: "zz/last.yaml", By: []string{"tools/ci/byte_identity.lock.yaml"}}, res.Hashed[0], "what a lock file names comes first")
	require.Equal(t, "pkg/many/f00.go", res.Hashed[1].File)
	for _, h := range res.Hashed {
		require.NotEqual(t, "internal/awake/resolver.go", h.File)
	}

	_, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	require.Contains(t, string(disp), "- … and 21 more (in `_apex/state/lift-disposition.json`)")
}
