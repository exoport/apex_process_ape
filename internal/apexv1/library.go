package apexv1

import (
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// W7 (D203, D207, D208): a library's package is what its users download,
// and everything APEX adds to the repository — .claude/, _apex/, the
// development folder, a lifted project's evidence/ — rides along unless
// something keeps it out. A Go module zip has no manifest: it holds every
// file outside a nested module, dot and underscore folders included, and
// cmd/go turns .gitattributes export-ignore off (both measured, the W7
// experiment). So a Go library gets one fence go.mod per APEX folder
// (fence.go), and every library's packed files are checked at release
// (packagecheck.go). Both apply only with `library: true` in
// _apex/config.yaml, which ape never writes: init and the lift suggest it.

// LibraryKey is the _apex/config.yaml line that marks a library (D208).
const LibraryKey = "library: true"

// EvidenceFolder is the root evidence folder an APEX v0 project kept and
// the lift leaves in place. v1 has none: its evidence is committed under
// <dev>/archive/, inside the development folder.
const EvidenceFolder = "evidence"

// LibrarySuggestion is init's and the lift's suggestion of `library: true`
// for a project that looks like a library and has no library: key. Only
// a suggestion: the key is a person's choice (D208).
type LibrarySuggestion struct {
	// Line is the line to add, File where.
	Line string `json:"line"`
	File string `json:"file"`
	// Reason says what made it look like a library.
	Reason string `json:"reason"`
}

// Text is the suggestion as init and the lift print it.
func (s *LibrarySuggestion) Text() string {
	return "add \"" + s.Line + "\" to " + s.File + " if this is a library others download — " + s.Reason +
		": a library's module zip carries .claude/, _apex/ and the development folder unless ape project fence keeps them out" +
		" (see ape project fence --help); ape never sets the key"
}

// IsLibrary reports `library: true` in the project's config; false when
// the config is absent or does not parse.
func (p *Project) IsLibrary() bool {
	cfg, err := LoadConfig(p.Root)
	return err == nil && cfg.Library
}

// ApexFolders are the folders APEX adds at the project root, relative and
// slash-separated, whether they exist or not: what a package must not
// carry (package-check), and, where present, what a Go library fences.
func (p *Project) ApexFolders() []string {
	return []string{".claude", "_apex", p.DevRel, EvidenceFolder}
}

// underApexFolder is the APEX folder a repo-relative slash path sits
// under, "" for none.
func (p *Project) underApexFolder(rel string) string {
	for _, f := range p.ApexFolders() {
		if strings.HasPrefix(rel, f+"/") {
			return f
		}
	}
	return ""
}

// SuggestLibrary is the suggestion of `library: true` for the project at
// root (development folder dev): nil when _apex/config.yaml already has a
// library: key, true or false (the person decided), or when the project
// does not look like a Go library — a go.mod at the root and no main
// package (goHasMain). Other ecosystems are not detected (the accepted
// W7 plan names Go); their packages are checked once the key is set.
func SuggestLibrary(root, dev string) *LibrarySuggestion {
	if configKeyLine(root, "library") != 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}
	mod := modfile.ModulePath(data)
	if mod == "" || goHasMain(root, dev) {
		return nil
	}
	return &LibrarySuggestion{Line: LibraryKey, File: configYAML, Reason: "go.mod declares module " + mod + " and no package is main"}
}

// goHasMain reports whether the Go module at root has a main package: a
// .go file, not a test, whose package clause is main and whose build
// constraint does not exclude it everywhere (//go:build ignore, as a
// generator's). Not looked at, as no library's users build them: what the
// go command skips (testdata, vendor, folders starting with . or _),
// nested modules, the development folder (gate programs are main), and
// examples/ and example/ folders.
func goHasMain(root, dev string) bool {
	found := false
	_ = filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, abs)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			name := d.Name()
			switch {
			case strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_"),
				name == "testdata" || name == "vendor" || name == "examples" || name == "example" || name == "node_modules",
				rel == dev,
				fileExists(filepath.Join(abs, "go.mod")):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		if goFileIsMain(abs) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// goFileIsMain reports whether a .go file is package main and built on
// some platform: a //go:build line that holds only with the tag "ignore"
// set keeps it out (any other tag may be set).
func goFileIsMain(abs string) bool {
	src, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	f, err := parser.ParseFile(token.NewFileSet(), abs, src, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil || f.Name == nil || f.Name.Name != "main" {
		return false
	}
	for _, g := range f.Comments {
		if g.Pos() >= f.Package {
			break
		}
		for _, c := range g.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			if expr, err := constraint.Parse(c.Text); err == nil && !expr.Eval(func(tag string) bool { return tag != "ignore" }) {
				return false
			}
		}
	}
	return true
}
