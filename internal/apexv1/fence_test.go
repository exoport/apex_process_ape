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

// libFiles is a v1 Go library: a root module with no main package,
// library: true, and .claude/ as init leaves it.
func libFiles() map[string]string {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: lib\nlibrary: true\n"
	files["go.mod"] = "module example.com/lib\n\ngo 1.27\n"
	files["lib.go"] = "package lib\n\n// A is the library.\nfunc A() int { return 1 }\n"
	files["internal/x/x.go"] = "package x\n"
	files[".claude/settings.json"] = "{}\n"
	return files
}

func fenceStates(res *FenceResult) map[string]string {
	out := map[string]string{}
	for _, f := range res.Fences {
		out[f.Path] = f.State
	}
	return out
}

func fenceCodes(fs []Finding) []string {
	var out []string
	for _, f := range ClassifyFindings(fs) {
		if strings.HasPrefix(f.Code, "fence.") {
			out = append(out, f.Code+" "+f.File)
		}
	}
	return out
}

// W7 (D207, D337): a new v1 library gets three fences — .claude/, _apex/,
// the development folder — in one commit, each the one-line-ish go.mod
// with the root's go line; a second run writes nothing.
func TestFence_NewLibraryThreeFences(t *testing.T) {
	p := fixture(t, libFiles())
	require.Equal(t, []string{"fence.missing .claude/go.mod", "fence.missing _apex/go.mod", "fence.missing development/go.mod"}, fenceCodes(p.Check()))
	_, err := p.FenceCheck(ctx())
	require.Equal(t, CodeFenceMissing, liftCode(err))

	res, err := p.Fence(ctx())
	require.NoError(t, err)
	require.Equal(t, []string{".claude/go.mod", "_apex/go.mod", "development/go.mod"}, res.Written)
	require.Equal(t, "1.27", res.Go)
	require.Equal(t, FenceSubject, subjects(t, p)[0])
	require.Equal(t, ".claude/go.mod\n_apex/go.mod\ndevelopment/go.mod", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	clean(t, p, "fence")
	require.Equal(t, "// This folder is APEX's, not the library's: this go.mod keeps it out of the module zip.\n"+
		"// See "+FenceDocsURL+". Written by ape project fence.\nmodule apex.invalid/fence/.claude\n\ngo 1.27\n", read(t, p, ".claude/go.mod"))
	require.Contains(t, read(t, p, "development/go.mod"), "module apex.invalid/fence/development\n")
	require.Empty(t, fenceCodes(p.Check()))
	_, err = p.FenceCheck(ctx())
	require.NoError(t, err)

	head := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.Fence(ctx())
	require.NoError(t, err)
	require.Empty(t, res.Written)
	require.Empty(t, res.Commit)
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD"))
}

// A lifted library keeps v0's root evidence/: four fences. The count is
// the folders present, never a fixed number.
func TestFence_LiftedEvidenceFourFences(t *testing.T) {
	files := libFiles()
	files["evidence/x/e.json"] = "{}\n"
	p := fixture(t, files)
	res, err := p.Fence(ctx())
	require.NoError(t, err)
	require.Equal(t, []string{".claude/go.mod", "_apex/go.mod", "development/go.mod", "evidence/go.mod"}, res.Written)
	require.Contains(t, read(t, p, "evidence/go.mod"), "module apex.invalid/fence/evidence\n")
}

// Without library: true, or without a go.mod at the root (a module in a
// subfolder, option A), there is nothing to fence: no finding, no write,
// and --check passes.
func TestFence_NothingToFence(t *testing.T) {
	for name, edit := range map[string]func(map[string]string){
		"not a library":      func(f map[string]string) { f["_apex/config.yaml"] = "project_name: lib\n" },
		"library: false":     func(f map[string]string) { f["_apex/config.yaml"] = "project_name: lib\nlibrary: false\n" },
		"no go.mod at root":  func(f map[string]string) { delete(f, "go.mod"); f["go/go.mod"] = "module example.com/lib\n" },
		"not even Go at all": func(f map[string]string) { delete(f, "go.mod"); delete(f, "lib.go") },
	} {
		t.Run(name, func(t *testing.T) {
			files := libFiles()
			edit(files)
			p := fixture(t, files)
			require.Empty(t, fenceCodes(p.Check()))
			res, err := p.FenceCheck(ctx())
			require.NoError(t, err)
			require.Empty(t, res.Fences)
			res, err = p.Fence(ctx())
			require.NoError(t, err)
			require.Empty(t, res.Written)
			require.NoFileExists(t, filepath.Join(p.Root, "_apex", "go.mod"))
		})
	}
}

// fence.missing for a deleted or an uncommitted fence (the zip is built
// from git), fence.altered for another module or go line; fence restores
// an altered one's two lines and keeps the rest (a gate's requires).
func TestFence_MissingAlteredRestored(t *testing.T) {
	p := fixture(t, libFiles())
	_, err := p.Fence(ctx())
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(p.Root, "_apex", "go.mod")))
	run(t, p.Root, "commit", "-q", "-am", "drop a fence")
	write(t, p, "go.mod", "module example.com/lib\n\ngo 1.28\n")
	write(t, p, "development/go.mod", "module apex.invalid/fence/development\n\ngo 1.27\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../\n")
	run(t, p.Root, "commit", "-q", "-am", "go 1.28 and a gate's requires")
	require.Equal(t, []string{"fence.altered .claude/go.mod", "fence.missing _apex/go.mod", "fence.altered development/go.mod"}, fenceCodes(p.Check()))
	res, err := p.FenceCheck(ctx())
	require.Equal(t, CodeFenceMissing, liftCode(err))
	require.Equal(t, map[string]string{".claude/go.mod": FenceAltered, "_apex/go.mod": FenceMissing, "development/go.mod": FenceAltered}, fenceStates(res))
	require.Contains(t, res.Fences[0].Detail, "go 1.27, want go 1.28 as the root go.mod")

	res, err = p.Fence(ctx())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{".claude/go.mod", "_apex/go.mod", "development/go.mod"}, res.Written)
	clean(t, p, "fence")
	require.Contains(t, read(t, p, ".claude/go.mod"), "go 1.28\n")
	dev := read(t, p, "development/go.mod")
	require.Contains(t, dev, "go 1.28\n")
	require.Contains(t, dev, "require example.com/lib v0.0.0\n")
	require.Contains(t, dev, "replace example.com/lib => ../\n")
	require.Empty(t, fenceCodes(p.Check()))

	// Written but not committed: still missing from the zip.
	require.NoError(t, os.Remove(filepath.Join(p.Root, "_apex", "go.mod")))
	run(t, p.Root, "commit", "-q", "-am", "drop it again")
	write(t, p, "_apex/go.mod", fenceContent("_apex", "1.28"))
	require.Equal(t, []string{"fence.missing _apex/go.mod"}, fenceCodes(p.Check()))
	require.Contains(t, findingMessages(p.Check()), "module fence _apex/go.mod is not committed, so the module zip, which is built from git, still carries _apex/ — `ape project fence` commits it")
	res, err = p.Fence(ctx())
	require.NoError(t, err)
	require.Empty(t, res.Written)
	require.NotEmpty(t, res.Commit)
	clean(t, p, "fence")
}

// A fence that does not parse is never overwritten; a root go.mod that
// does not parse makes fence and --check exit 2 (fence.unreadable).
func TestFence_BrokenFiles(t *testing.T) {
	p := fixture(t, libFiles())
	_, err := p.Fence(ctx())
	require.NoError(t, err)
	write(t, p, "_apex/go.mod", "module\n")
	run(t, p.Root, "commit", "-q", "-am", "break a fence")
	require.Equal(t, []string{"fence.altered _apex/go.mod"}, fenceCodes(p.Check()))
	_, err = p.Fence(ctx())
	require.Equal(t, CodeFenceAltered, liftCode(err))
	require.Equal(t, "module\n", read(t, p, "_apex/go.mod"))

	write(t, p, "go.mod", "module example.com/lib\n\ngo 1.27\nrequire (\n")
	_, err = p.FenceCheck(ctx())
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeFenceUnreadable, ue.Code)
}

// The .claude/ shape ignores .claude/go.mod; fence commits it anyway, and
// on a change branch it refuses.
func TestFence_IgnoredClaudeFolderAndChangeBranch(t *testing.T) {
	files := libFiles()
	files[".gitignore"] = shapeBlock
	p := fixture(t, files)
	_, err := p.Fence(ctx())
	require.NoError(t, err)
	require.Contains(t, run(t, p.Root, "ls-files", ".claude"), ".claude/go.mod")

	require.NoError(t, os.Remove(filepath.Join(p.Root, ".claude", "go.mod")))
	run(t, p.Root, "commit", "-q", "-am", "drop")
	run(t, p.Root, "switch", "-q", "-c", "change/c-one")
	_, err = p.Fence(ctx())
	require.Equal(t, CodeChangeBranch, liftCode(err))
}

// D207's claim as a fixture: with the fences, the go command is green and
// creates or changes no file — build, vet, test, mod tidy, mod verify, and
// go work sync with a go.work.
func TestFence_GoToolchainLeavesTheTreeAlone(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	files := libFiles()
	files["lib_test.go"] = "package lib\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n\tif A() != 1 {\n\t\tt.Fatal(A())\n\t}\n}\n"
	files["go.work"] = "go 1.27\n\nuse .\n"
	files["development/gates/m1/main.go"] = "package main\n\nfunc main() {}\n"
	p := fixture(t, files)
	_, err = p.Fence(ctx())
	require.NoError(t, err)
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "")
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}, {"test", "./..."}, {"mod", "tidy"}, {"mod", "verify"}, {"work", "sync"}} {
		c := exec.Command(gobin, args...)
		c.Dir = p.Root
		out, err := c.CombinedOutput()
		require.NoError(t, err, "go %v: %s", args, out)
		require.Empty(t, run(t, p.Root, "status", "--porcelain", "--untracked-files=all"), "go %v changed the tree", args)
	}
}

// The suggestion: a Go module with no main package (examples/, a
// //go:build ignore generator, testdata and the development folder's gate
// programs do not count), and no library: key at all.
func TestSuggestLibrary(t *testing.T) {
	for name, tc := range map[string]struct {
		files   map[string]string
		suggest bool
	}{
		"a library":             {map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n"}, true},
		"an app":                {map[string]string{"go.mod": "module example.com/app\n", "cmd/app/main.go": "package main\n\nfunc main() {}\n"}, false},
		"a root main":           {map[string]string{"go.mod": "module example.com/app\n", "main.go": "package main\n"}, false},
		"examples and gates":    {map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n", "examples/a/main.go": "package main\n", "development/gates/m1/main.go": "package main\n", "testdata/t/main.go": "package main\n"}, true},
		"a generator":           {map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n", "gen.go": "//go:build ignore\n\npackage main\n"}, true},
		"a tagged main":         {map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n", "tool.go": "//go:build tools\n\npackage main\n"}, false},
		"a nested module":       {map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n", "tools/go.mod": "module tools\n", "tools/main.go": "package main\n"}, true},
		"no go.mod":             {map[string]string{"lib.go": "package lib\n"}, false},
		"library: false is set": {map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n", "_apex/config.yaml": "library: false\n"}, false},
		"library: true is set":  {map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n", "_apex/config.yaml": "library: true\n"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tc.files {
				path := filepath.Join(root, filepath.FromSlash(rel))
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
			}
			s := SuggestLibrary(root, "development")
			if !tc.suggest {
				require.Nil(t, s)
				return
			}
			require.NotNil(t, s)
			require.Equal(t, LibrarySuggestion{Line: "library: true", File: "_apex/config.yaml", Reason: "go.mod declares module example.com/lib and no package is main"}, *s)
		})
	}
}

// init suggests library: true and never writes it; with the key set
// before init, init writes the three fences into its own commit.
func TestInit_LibrarySuggestionAndFences(t *testing.T) {
	needSh(t)
	gitEnv(t)
	asPerson(t)
	root := t.TempDir()
	run(t, root, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/lib\n\ngo 1.27\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "lib.go"), []byte("package lib\n"), 0o644))
	res, err := Init(ctx(), root, true)
	require.NoError(t, err)
	require.NotNil(t, res.SuggestLibrary)
	require.Empty(t, res.Fences)
	cfg, err := os.ReadFile(ConfigPath(root))
	require.NoError(t, err)
	require.NotContains(t, string(cfg), "library")

	root = t.TempDir()
	run(t, root, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/lib\n\ngo 1.27\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(ConfigPath(root), []byte("library: true\n"), 0o644))
	res, err = Init(ctx(), root, false)
	require.NoError(t, err)
	require.Nil(t, res.SuggestLibrary)
	require.Equal(t, []string{".claude/go.mod", "_apex/go.mod", "development/go.mod"}, res.Fences)
	p, err := Open(root)
	require.NoError(t, err)
	committed := strings.Split(run(t, root, "show", "--name-only", "--format=", "HEAD"), "\n")
	require.Subset(t, committed, res.Fences, ".claude/go.mod too, though the .claude/ shape ignores it")
	_, err = p.FenceCheck(ctx())
	require.NoError(t, err)
}

func gateNotices(p *Project) []Diagnostic {
	var out []Diagnostic
	for _, d := range p.Notices() {
		if d.Code == CodeGateRootModuleChange {
			out = append(out, d)
		}
	}
	return out
}

// The gate dependency rule (D207): a library's gate program that imports
// what its module does not provide is a notice; from a fence with its
// own require and replace it is not. Only for library: true.
func TestGateRootModuleChange(t *testing.T) {
	gate := "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/lib\"\n\t\"github.com/acme/check\"\n)\n\nfunc main() { fmt.Println(lib.A(), check.X) }\n"
	stdlib := "package main\n\nimport \"os\"\n\nfunc main() { os.Exit(0) }\n"

	// Unfenced: the root module provides lib, not github.com/acme/check.
	files := libFiles()
	files["development/gates/M1-x/main.go"] = gate
	files["development/gates/M2-y/main.go"] = stdlib
	p := fixture(t, files)
	ns := gateNotices(p)
	require.Len(t, ns, 1)
	require.Equal(t, "development/gates/M1-x/main.go", ns[0].File)
	require.Equal(t, 7, ns[0].Line)
	require.Equal(t, SeverityNotice, ns[0].Severity)
	require.Equal(t, ActionReplan, ns[0].Action)
	require.Equal(t, "gate program development/gates/M1-x imports github.com/acme/check, which the root module does not provide, so running it from the root changes the root go.mod and go.sum", ns[0].Message)
	require.Contains(t, ns[0].Fix, "`require example.com/lib v0.0.0` and `replace example.com/lib => ../`")
	require.Contains(t, ns[0].Fix, "`GOWORK=off go -C development run ./gates/M1-x`")

	// Fenced, the fence without requires: it provides neither.
	_, err := p.Fence(ctx())
	require.NoError(t, err)
	ns = gateNotices(p)
	require.Len(t, ns, 1)
	require.Contains(t, ns[0].Message, "imports example.com/lib, github.com/acme/check, which its module development/go.mod does not require")

	// The fence with its own requires and the replace: none.
	write(t, p, "development/go.mod", fenceContent("development", "1.27")+"\nrequire (\n\texample.com/lib v0.0.0\n\tgithub.com/acme/check v1.2.0\n)\n\nreplace example.com/lib => ../\n")
	require.Empty(t, gateNotices(p))
	require.Empty(t, fenceCodes(p.Check()), "a fence's require and replace lines are not an alteration")

	// Not a library: no notice.
	write(t, p, "_apex/config.yaml", "project_name: lib\n")
	write(t, p, "development/go.mod", fenceContent("development", "1.27"))
	require.Empty(t, gateNotices(p))
}
