package apexv1

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		"internal/awake/sum_test.go":       "package awake\n\nvar pinned = map[string]string{\"resolver.go\": \"9f2c\"}\n\nfunc TestPins(t *testing.T) { _ = sha256.Sum256(nil) }\n",
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
	require.Len(t, res.Hashed, 61, "a dry run lists every one (R6)")
	require.Equal(t, LiftHashed{File: "zz/last.yaml", By: []string{"tools/ci/byte_identity.lock.yaml"}}, res.Hashed[0], "what a lock file names comes first")
	require.Equal(t, "pkg/many/f00.go", res.Hashed[1].File)
	for _, h := range res.Hashed {
		require.NotEqual(t, "internal/awake/resolver.go", h.File)
	}

	// A real lift's result is capped: the report it writes has them all.
	res, err = Lift(ctx(), root, LiftOptions{NoVerifyRun: true})
	require.NoError(t, err)
	require.Len(t, res.Hashed, hashedCap)
	require.Equal(t, 61, res.HashedTotal)
	disp, err := os.ReadFile(filepath.Join(root, "development", LiftDisposition))
	require.NoError(t, err)
	require.Contains(t, string(disp), "- … and 21 more (in `_apex/state/lift-disposition.json`)")
}

// The coreutils hashers name no "sha256" on a word boundary: a CI script
// running sha256sum over a file it names is a hasher all the same.
func TestHashWordRe_CoreutilsTools(t *testing.T) {
	for _, src := range []string{"sha256sum other.sh", "md5sum -c sums", "shasum -a 256 x", "b2sum x", "sha512sum x", "SHA256 of x", "hashlib.sha256(", "checksum"} {
		require.True(t, hashWordRe.MatchString(src), src)
	}
	for _, src := range []string{"shaving", "summary", "md5ish", "a sha256sumx"} {
		require.False(t, hashWordRe.MatchString(src), src)
	}
}

// R6, from tenax and mdpdf on rc.4: 63 "hashed" against 14 real, and the
// cap pushed 4 real ones (internal/timer's) to positions 51–54 of a dry
// run that said "and 12 more (in the report)" — a file a dry run never
// writes. cmd/tenaxd/cache_test.go flagged 19 files with "byte-identical"
// only in its prose comments and a ReadDir(".") over .go names: no hasher.
// The source-hash test names its targets, which come before the files a
// glob ties to a guard; and the dry run lists every one.
func TestLift_HashedPrecisionOrderNoDryRunCap(t *testing.T) {
	root := liftFixture(t, true)
	files := map[string]string{
		"cmd/tenaxd/cache.go": "package main\n\n// Story 1.1: the cache\nvar C = 1\n",
		"cmd/tenaxd/main.go":  "package main\n\n// Story 1.1: main\nfunc main() {}\n",
		"cmd/tenaxd/cache_test.go": "package main\n\n" +
			"// TestCacheLayout checks the cache writes stay byte-identical across\n" +
			"// runs: each entry's fingerprint (a sha256 of its bytes) is stable.\n" +
			"func TestCacheLayout(t *testing.T) {\n" +
			"\tentries, _ := os.ReadDir(\".\")\n" +
			"\tfor _, e := range entries {\n" +
			"\t\tif strings.HasSuffix(e.Name(), \".go\") {\n" +
			"\t\t\tt.Log(e.Name()) /* not checksum-compared */\n" +
			"\t\t}\n\t}\n}\n",
		"internal/timer/source_hash_test.go": "package timer\n\n" +
			"var pinned = map[string]string{\n" +
			"\t\"schedule_wake.go\": \"9f2c\",\n" +
			"\t\"timer.go\":         \"1a2b\",\n" +
			"\t\"timer_test.go\":    \"3c4d\",\n" +
			"\t\"wake.go\":          \"5e6f\",\n" +
			"}\n\n" +
			"func TestInternalTimer_PackageSource(t *testing.T) {\n" +
			"\tfor name, want := range pinned {\n" +
			"\t\tdata, _ := os.ReadFile(name)\n" +
			"\t\tif fmt.Sprintf(\"%x\", sha256.Sum256(data)) != want {\n" +
			"\t\t\tt.Fatal(name)\n\t\t}\n\t}\n}\n",
		// 60 files beside a guard that globs them: sorted ahead of
		// internal/ by path, listed after it by kind.
		"a/many/guard_test.go": "package many\n\nfunc TestBytes(t *testing.T) {\n\tfiles, _ := filepath.Glob(\"*.go\")\n\t_ = sha256.New()\n}\n",
	}
	for _, f := range []string{"schedule_wake", "timer", "timer_test", "wake"} {
		files["internal/timer/"+f+".go"] = "package timer\n\n// Story 6.3: " + f + "\nvar " + strings.ToUpper(f[:1]) + " = 1\n"
	}
	for i := range 60 {
		files[fmt.Sprintf("a/many/f%02d.go", i)] = "package many\n\n// Story 1.1: part\nvar X = 1\n"
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

	require.Equal(t, 64, res.HashedTotal, "the 4 the source-hash test names and the 60 the guard globs; never cmd/tenaxd")
	require.Len(t, res.Hashed, 64, "a dry run writes no report: it lists every one")
	var got []string
	for _, h := range res.Hashed {
		got = append(got, h.File)
		require.NotContains(t, h.File, "cmd/tenaxd/", "a hash word in a comment hashes nothing")
	}
	require.Equal(t, []string{"internal/timer/schedule_wake.go", "internal/timer/timer.go", "internal/timer/timer_test.go", "internal/timer/wake.go"}, got[:4],
		"what a hasher names comes before what a glob ties to one")
	require.Equal(t, LiftHashed{File: "a/many/f00.go", By: []string{"a/many/guard_test.go"}}, res.Hashed[4])
	require.Equal(t, "a/many/f59.go", got[63])
}

// R6: the hasher's comments are blanked, its lines kept in place.
func TestCodeText_BlanksComments(t *testing.T) {
	src := "package x\n\n// sha256 of the sources\nvar a = 1 /* checksum */\nvar b = \"// not a comment: sha256\"\n"
	got := codeText("x_test.go", src)
	require.Equal(t, strings.Count(src, "\n"), strings.Count(got, "\n"))
	require.NotContains(t, got, "sources")
	require.NotContains(t, got, "checksum")
	require.Contains(t, got, "\"// not a comment: sha256\"", "strings are code")
	require.True(t, isHashy(got))

	require.False(t, isHashy(codeText("ci/check.yml", "# sha256sum every file\nrun: make test\n")))
	require.True(t, isHashy(codeText("ci/check.yml", "# check\nrun: sha256sum -c sums\n")))
	require.False(t, isHashy(codeText("ci/Jenkinsfile", "// sha256 the artefacts\nsh 'make'\n")), "no tokenizer: a line opening with // is a comment")
}

// R6: a hash call names no hash word — a Go hash package beyond sha256,
// Python's hashlib, Node's createHash.
func TestHashCallRe(t *testing.T) {
	for _, src := range []string{`"golang.org/x/crypto/blake2b"`, `"crypto/sha3"`, `"crypto/hmac"`, "import hashlib", "crypto.createHash('sha1')"} {
		require.True(t, isHashy(src), src)
	}
	for _, src := range []string{`"crypto/rand"`, `"crypto/tls"`, "hashlibrary", "os.ReadDir(\".\")"} {
		require.False(t, isHashy(src), src)
	}
}
