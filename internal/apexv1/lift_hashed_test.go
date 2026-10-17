package apexv1

import (
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
		{File: "internal/awake/resolver.go", By: []string{"internal/awake/sum_test.go"}},
		{File: "internal/timer/timer.go", By: []string{"internal/timer/timer_test.go"}},
		{File: "modules/core/module.yaml", By: []string{"tools/ci/byte_identity.lock.yaml"}},
	}, res.Hashed, "not other.go (its test names resolver.go only), not main.go, not .github (a hash 5 lines off the path)")

	// Excluded, they are not edited, so not listed.
	res, err = Lift(ctx(), root, LiftOptions{DryRun: true, Exclude: []string{"internal/timer/*.go", "internal/awake/resolver.go", "deploy/**", "modules/**"}})
	require.NoError(t, err)
	require.Empty(t, res.Hashed)
}
