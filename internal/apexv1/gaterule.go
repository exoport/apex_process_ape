package apexv1

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// The gate dependency rule (W7, D207): a library's gate uses only what
// its module provides. A gate program under <dev>/gates/ belongs to the
// nearest go.mod above it: the development folder's fence once the
// library is fenced, the root module before. A gate that needs more than
// the root module graph runs from its fence — `require` lines (the root
// module's packages through `require <root> v0.0.0` and
// `replace <root> => ../`), its own go.sum — as
// `GOWORK=off go -C <dev> run ./gates/<x>`, so the root go.mod, go.sum
// and go.work never change for a gate. An import the gate's module does
// not provide fails the go command (-mod=readonly) or, with -mod=mod,
// writes it into that module's go.mod: either way it is the notice
// gate.root-module-change. Read from the files alone, offline: an import
// is provided when it is the standard library's (a first path element
// with no dot), the module's own, or under a module its go.mod requires.

// gateRootModuleNotices are project check's gate.root-module-change
// notices, one per gate package that imports what its module does not
// provide; only for library: true with a go.mod at the root.
func (p *Project) gateRootModuleNotices() []Diagnostic {
	if !p.IsLibrary() {
		return nil
	}
	root, err := p.rootGoMod()
	if err != nil || root.Module == nil {
		return nil
	}
	gates := p.GatesDir()
	if !dirExists(gates) {
		return nil
	}
	// Each package folder's Go files, test files included (a gate may be
	// a test), in walk order.
	pkgs := map[string][]string{}
	var order []string
	_ = filepath.WalkDir(gates, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); abs != gates && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") || n == "testdata" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(abs, ".go") {
			dir := filepath.Dir(abs)
			if pkgs[dir] == nil {
				order = append(order, dir)
			}
			pkgs[dir] = append(pkgs[dir], abs)
		}
		return nil
	})
	var out []Diagnostic
	for _, dir := range order {
		modDir, mf := p.owningModule(dir, root)
		if mf == nil || mf.Module == nil {
			continue
		}
		file, line := "", 0
		var missing []string
		seen := map[string]bool{}
		for _, abs := range pkgs[dir] {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
			if err != nil {
				continue
			}
			for _, imp := range f.Imports {
				ip, err := strconv.Unquote(imp.Path.Value)
				if err != nil || seen[ip] || provides(mf, ip) {
					continue
				}
				seen[ip] = true
				missing = append(missing, ip)
				if file == "" {
					file, line = p.Rel(abs), fset.Position(imp.Pos()).Line
				}
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		out = append(out, notice(Diagnostic{Code: CodeGateRootModuleChange, File: file, Line: line, Message: p.gateRuleMessage(dir, modDir, root, missing)}))
	}
	return out
}

// owningModule is the folder of the nearest go.mod at or above dir, up to
// the root, and that go.mod parsed (root for the root's); nil when it
// does not parse.
func (p *Project) owningModule(dir string, root *modfile.File) (string, *modfile.File) {
	for cur := dir; ; cur = filepath.Dir(cur) {
		if cur == p.Root {
			return p.Root, root
		}
		data, err := os.ReadFile(filepath.Join(cur, "go.mod"))
		if err == nil {
			mf, err := modfile.ParseLax(filepath.Join(cur, "go.mod"), data, nil)
			if err != nil {
				return cur, nil
			}
			return cur, mf
		}
		if parent := filepath.Dir(cur); parent == cur || !strings.HasPrefix(cur, p.Root) {
			return p.Root, root
		}
	}
}

// provides reports whether the module mf provides the import path ip:
// the standard library (a first element with no dot, cgo's "C"), the
// module's own packages, or a required module's.
func provides(mf *modfile.File, ip string) bool {
	first, _, _ := strings.Cut(ip, "/")
	under := func(mod string) bool { return ip == mod || strings.HasPrefix(ip, mod+"/") }
	if under(mf.Module.Mod.Path) {
		return true
	}
	for _, r := range mf.Require {
		if under(r.Mod.Path) {
			return true
		}
	}
	return !strings.Contains(first, ".")
}

// gateRuleMessage is the notice's text for the gate package at dir, whose
// module is the go.mod in modDir (the root's, or a fence's).
func (p *Project) gateRuleMessage(dir, modDir string, root *modfile.File, missing []string) string {
	pkg := p.Rel(dir)
	shown := missing
	if len(shown) > 5 {
		shown = shown[:5]
	}
	imports := strings.Join(shown, ", ")
	if len(missing) > len(shown) {
		imports += " and " + strconv.Itoa(len(missing)-len(shown)) + " more"
	}
	fence := p.DevRel
	if modDir != p.Root {
		fence = p.Rel(modDir)
	}
	up, _ := filepath.Rel(filepath.Join(p.Root, filepath.FromSlash(fence)), p.Root)
	up = filepath.ToSlash(up) + "/"
	rest, _ := filepath.Rel(filepath.Join(p.Root, filepath.FromSlash(fence)), dir)
	run := "`GOWORK=off go -C " + fence + " run ./" + filepath.ToSlash(rest) + "`"
	fix := "give it its own requires in " + fence + "/go.mod (the module fence): a `require` per module, `require " + root.Module.Mod.Path +
		" v0.0.0` and `replace " + root.Module.Mod.Path + " => " + up + "` for the root module's packages, and its own go.sum; run it as " + run +
		" — the root go.mod, go.sum and go.work never change for a gate (D207)"
	if modDir == p.Root {
		return "gate program " + pkg + " imports " + imports + ", which the root module does not provide, so running it from the root changes the root go.mod and go.sum — " + fix
	}
	return "gate program " + pkg + " imports " + imports + ", which its module " + fence + "/go.mod does not require — " + fix
}
