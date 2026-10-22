package apexv1

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The W7 experiment (go1.27.2, `go mod download` direct from git, as
// proxy.golang.org fetches) as a fixture: a library with lib.go,
// internal/x/x.go and the APEX folders, tagged three ways, and the files
// its module zip held at each tag. v0.1.0: nothing excluded, dot and
// underscore folders included. v0.2.0: .gitattributes export-ignore,
// which cmd/go turns off — the same files plus .gitattributes. v0.3.0:
// the four fences — only the library.
func TestGoModuleZipFiles_TheW7ExperimentsThreeTags(t *testing.T) {
	needSh(t)
	gitEnv(t)
	root := t.TempDir()
	for rel, content := range map[string]string{
		"go.mod":                            "module fence.test/lib\n\ngo 1.27\n",
		"lib.go":                            "package lib\n",
		"internal/x/x.go":                   "package x\n",
		".claude/settings.json":             "{}\n",
		".claude/skills/apex-plan/SKILL.md": "---\nname: apex-plan\n---\n",
		"_apex/config.yaml":                 "library: true\n",
		"development/specs/a/spec.md":       "# A\n",
		"evidence/x/e.json":                 "{}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	run(t, root, "init", "-q", "-b", "main")
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "lib")
	run(t, root, "tag", "v0.1.0")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("/.claude export-ignore\n/_apex export-ignore\n/development export-ignore\n/evidence export-ignore\n"), 0o644))
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "export-ignore")
	run(t, root, "tag", "v0.2.0")
	require.NoError(t, os.Remove(filepath.Join(root, ".gitattributes")))
	for _, f := range []string{".claude", "_apex", "development", "evidence"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, f, "go.mod"), []byte(fenceContent(f, "1.27")), 0o644))
	}
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "fences")
	run(t, root, "tag", "v0.3.0")

	all := []string{".claude/settings.json", ".claude/skills/apex-plan/SKILL.md", "_apex/config.yaml", "development/specs/a/spec.md", "evidence/x/e.json", "go.mod", "internal/x/x.go", "lib.go"}
	for tag, want := range map[string][]string{
		"v0.1.0": all,
		"v0.2.0": append([]string{".gitattributes"}, all...),
		"v0.3.0": {"go.mod", "internal/x/x.go", "lib.go"},
	} {
		got, zipErr, err := GoModuleZipFiles(ctx(), root, tag, "")
		require.NoError(t, err)
		require.Empty(t, zipErr)
		require.ElementsMatch(t, want, got, tag)
	}
}

// package-check on a Go library: a leak while unfenced (the first paths
// named, the rest counted), green once fenced and committed.
func TestPackageCheck_GoLeakThenFenced(t *testing.T) {
	files := libFiles()
	for i := range 25 {
		files["development/specs/s/f"+strings.Repeat("x", i)+".md"] = "x\n"
	}
	p := fixture(t, files)
	res, err := p.PackageCheck(ctx())
	require.Equal(t, CodePackageLeak, liftCode(err))
	require.Len(t, res.Packages, 1)
	r := res.Packages[0]
	require.Equal(t, EcosystemGo, r.Ecosystem)
	require.True(t, r.Checked)
	require.Len(t, r.Leaks, leaksShown)
	require.Greater(t, r.LeaksTotal, leaksShown)
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok)
	require.Len(t, fe.Findings, 1)
	f := ClassifyFindings(fe.Findings)[0]
	require.Equal(t, CodePackageLeak, f.Code)
	require.Equal(t, "go.mod", f.File)
	require.Contains(t, f.Message, "the Go module zip (.) at HEAD holds ")
	require.Contains(t, f.Message, " more")
	require.Contains(t, f.Fix, "ape project fence")

	_, err = p.Fence(ctx())
	require.NoError(t, err)
	res, err = p.PackageCheck(ctx())
	require.NoError(t, err)
	require.Zero(t, res.Packages[0].LeaksTotal)
	require.ElementsMatch(t, []string{"go.mod", "lib.go", "internal/x/x.go"}, goZip(t, p))
}

func goZip(t *testing.T, p *Project) []string {
	t.Helper()
	files, _, err := GoModuleZipFiles(ctx(), p.Root, "HEAD", "")
	require.NoError(t, err)
	var out []string
	for _, f := range files {
		// The fixture's own root files (AGENTS.md and the like).
		if strings.HasSuffix(f, ".go") || f == "go.mod" {
			out = append(out, f)
		}
	}
	return out
}

// Option A: the module in a subfolder, no go.mod at the root — its zip
// holds no APEX folder, with no fence.
func TestPackageCheck_SubfolderModuleNeedsNoFence(t *testing.T) {
	files := libFiles()
	delete(files, "go.mod")
	delete(files, "lib.go")
	delete(files, "internal/x/x.go")
	files["go/go.mod"] = "module example.com/lib\n\ngo 1.27\n"
	files["go/lib.go"] = "package lib\n"
	files["LICENSE"] = "MIT\n"
	p := fixture(t, files)
	res, err := p.PackageCheck(ctx())
	require.NoError(t, err)
	require.Len(t, res.Packages, 1)
	r := res.Packages[0]
	require.Equal(t, "go", r.Dir)
	require.Equal(t, "go/go.mod", r.Manifest)
	require.Equal(t, 3, r.Files, "go.mod, lib.go and the root's LICENSE, as cmd/go adds it")
	require.Zero(t, r.LeaksTotal)
	require.Empty(t, fenceCodes(p.Check()), "no fence is required")
}

// Not a library: nothing packed, green; a library with nothing ape knows
// how to pack: a notice, not a silent pass.
func TestPackageCheck_NotALibraryAndNoManifest(t *testing.T) {
	files := libFiles()
	files["_apex/config.yaml"] = "project_name: lib\n"
	p := fixture(t, files)
	res, err := p.PackageCheck(ctx())
	require.NoError(t, err)
	require.False(t, res.Library)
	require.Empty(t, res.Packages)

	files = libFiles()
	delete(files, "go.mod")
	p = fixture(t, files)
	res, err = p.PackageCheck(ctx())
	require.NoError(t, err)
	require.Len(t, res.Notices, 1)
	require.Equal(t, CodePackageUnchecked, res.Notices[0].Code)
	require.Equal(t, SeverityNotice, res.Notices[0].Severity)
}

// toolPath is a PATH of stub tools plus git and sh (and tar/gzip for the
// python stub), so a test decides which packaging tools exist.
func toolPath(t *testing.T, stubs map[string]string) {
	t.Helper()
	if ok, why := shFixtures(); !ok {
		t.Skip("sh stubs: " + why)
	}
	dir := t.TempDir()
	for _, bin := range []string{"git", "sh", "tar", "gzip", "mkdir", "cat", "rm"} {
		found, err := exec.LookPath(bin)
		if err != nil {
			t.Skipf("%s not on PATH", bin)
		}
		require.NoError(t, os.Symlink(found, filepath.Join(dir, bin)))
	}
	for name, script := range stubs {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755))
	}
	t.Setenv("PATH", dir)
}

// npm: npm pack's file list judged; npm absent: library.package-unchecked.
func TestPackageCheck_NPM(t *testing.T) {
	files := libFiles()
	delete(files, "go.mod")
	files["package.json"] = `{"name": "lib", "version": "1.0.0"}` + "\n"
	p := fixture(t, files)

	toolPath(t, map[string]string{"npm": `echo '[{"id": "lib@1.0.0", "files": [{"path": "package.json"}, {"path": "index.js"}, {"path": "_apex/config.yaml"}, {"path": ".claude/settings.json"}]}]'` + "\n"})
	res, err := p.PackageCheck(ctx())
	require.Equal(t, CodePackageLeak, liftCode(err))
	require.Equal(t, []string{".claude/settings.json", "_apex/config.yaml"}, res.Packages[0].Leaks)
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok)
	f := ClassifyFindings(fe.Findings)[0]
	require.Equal(t, CodePackageLeak, f.Code)
	require.Equal(t, "package.json", f.File)
	require.Contains(t, f.Message, "npm pack (package.json) holds 2 file(s) under APEX folders: .claude/settings.json, _apex/config.yaml")

	// files: leaves them out — clean.
	toolPath(t, map[string]string{"npm": `echo '[{"files": [{"path": "package.json"}, {"path": "index.js"}]}]'` + "\n"})
	res, err = p.PackageCheck(ctx())
	require.NoError(t, err)
	require.True(t, res.Packages[0].Checked)
	require.Equal(t, 2, res.Packages[0].Files)

	toolPath(t, nil)
	res, err = p.PackageCheck(ctx())
	require.NoError(t, err, "a tool absent is a notice, never a failure")
	require.False(t, res.Packages[0].Checked)
	require.Equal(t, "npm is not on PATH", res.Packages[0].Unchecked)
	require.Len(t, res.Notices, 1)
	require.Equal(t, CodePackageUnchecked, res.Notices[0].Code)
	require.Equal(t, "package.json", res.Notices[0].File)
	require.Equal(t, "npm package . was not checked for APEX folders: npm is not on PATH", res.Notices[0].Message)
}

// Cargo and Python: their own lists; Python's from the sdist and wheel a
// build of a copy of HEAD writes (the stub writes an sdist).
func TestPackageCheck_CargoAndPython(t *testing.T) {
	files := libFiles()
	delete(files, "go.mod")
	files["Cargo.toml"] = "[package]\nname = \"lib\"\n"
	files["pyproject.toml"] = "[build-system]\n"
	p := fixture(t, files)
	python := `out=""; while [ $# -gt 0 ]; do [ "$1" = --outdir ] && out=$2; shift; done
d=$(mktemp -d 2>/dev/null || echo /tmp/pystub$$); mkdir -p "$d/lib-1.0/src" "$d/lib-1.0/development"
: > "$d/lib-1.0/src/lib.py"; : > "$d/lib-1.0/development/project.md"; mkdir -p "$out"
tar -czf "$out/lib-1.0.tar.gz" -C "$d" lib-1.0
`
	toolPath(t, map[string]string{
		"cargo":   "printf 'Cargo.toml\\nsrc/lib.rs\\nevidence/x.json\\n'\n",
		"python3": python,
		"mktemp":  "echo \"" + t.TempDir() + "\"\n",
	})
	res, err := p.PackageCheck(ctx())
	require.Equal(t, CodePackageLeak, liftCode(err))
	got := map[string][]string{}
	for _, r := range res.Packages {
		require.True(t, r.Checked, "%s: %s", r.Ecosystem, r.Unchecked)
		got[r.Ecosystem] = r.Leaks
	}
	require.Equal(t, map[string][]string{"python": {"development/project.md"}, "cargo": {"evidence/x.json"}}, got)
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok)
	msgs := findingMessages(fe.Findings)
	require.Contains(t, msgs, "the Python sdist or wheel (pyproject.toml) holds 1 file(s) under APEX folders: development/project.md")
	require.Contains(t, msgs, "cargo package (Cargo.toml) holds 1 file(s) under APEX folders: evidence/x.json")

	toolPath(t, map[string]string{"cargo": "echo 'error: no network' >&2; exit 101\n"})
	res, err = p.PackageCheck(ctx())
	require.NoError(t, err)
	why := make([]string, 0, len(res.Notices))
	for _, n := range res.Notices {
		require.Equal(t, CodePackageUnchecked, n.Code)
		why = append(why, n.Message)
	}
	require.Equal(t, []string{
		"python package . was not checked for APEX folders: python3 is not on PATH",
		"cargo package . was not checked for APEX folders: cargo package --list --allow-dirty failed: error: no network",
	}, why)
}
