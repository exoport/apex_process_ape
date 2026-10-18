package apexv1

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// The project's release scheme (D120): how apex-release proposes the next
// version. ape validates and guards it; it reads no version itself.
//
//	release:
//	  scheme: semver   # semver (default) | date (YYYY.MM.DD) | edition (the next integer)
//	  docs: [README.md, docs/]   # what a release brings up to date (D129); default
//	                             # README.md and docs/, those that exist

// Release schemes.
const (
	ReleaseSemver  = "semver"
	ReleaseDate    = "date"
	ReleaseEdition = "edition"
)

// ReleaseConfig is _apex/config.yaml's release block.
type ReleaseConfig struct {
	Scheme string `yaml:"scheme"`
	// Docs are the files and folders a release brings up to date (D129):
	// relative paths; absent, DefaultReleaseDocs that exist (DocsList).
	Docs []string `yaml:"docs"`
}

// DefaultReleaseDocs are release.docs when it is not set, each only where
// it exists.
var DefaultReleaseDocs = []string{"README.md", "docs/"}

// UnmarshalYAML validates the block as it parses.
func (r *ReleaseConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKeys(n, "release", "scheme", "docs"); err != nil {
		return err
	}
	type plain ReleaseConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	p.Scheme = strings.TrimSpace(p.Scheme)
	switch p.Scheme {
	case "", ReleaseSemver, ReleaseDate, ReleaseEdition:
	default:
		return fmt.Errorf("release.scheme: %q is not %s, %s or %s", p.Scheme, ReleaseSemver, ReleaseDate, ReleaseEdition)
	}
	if err := checkReleaseDocs(n, p.Docs); err != nil {
		return err
	}
	*r = ReleaseConfig(p)
	return nil
}

// checkReleaseDocs validates release.docs when the key is there: a
// non-empty list of relative paths inside the project, none twice. Whether
// each exists is project check's (docs.missing), not the parser's.
func checkReleaseDocs(n *yaml.Node, docs []string) error {
	present := false
	for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		present = present || n.Content[i].Value == "docs"
	}
	if !present {
		return nil
	}
	if len(docs) == 0 {
		return errors.New("release.docs: list at least one path, or drop the key for the default (README.md and docs/, those that exist)")
	}
	seen := map[string]bool{}
	for _, d := range docs {
		c := path.Clean(strings.TrimSpace(d))
		switch {
		case strings.TrimSpace(d) == "":
			return errors.New("release.docs: an empty path")
		case path.IsAbs(c) || filepath.IsAbs(d) || c == ".." || strings.HasPrefix(c, "../"):
			return fmt.Errorf("release.docs: %q is not a relative path inside the project", d)
		case seen[c]:
			return fmt.Errorf("release.docs: %q is listed twice", d)
		}
		seen[c] = true
	}
	return nil
}

// DocsList is release.docs as written, else those of DefaultReleaseDocs
// that exist under root.
func (r ReleaseConfig) DocsList(root string) []string {
	if len(r.Docs) > 0 {
		return r.Docs
	}
	out := []string{}
	for _, d := range DefaultReleaseDocs {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(d))); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// DocsScope is what the docs default (D152) counts as docs: release.docs
// as written, else every DefaultReleaseDocs entry, whether or not it exists
// in this checkout — a docs change may create docs/, and the delegate may
// run from a base branch that has none yet.
func (r ReleaseConfig) DocsScope() []string {
	if len(r.Docs) > 0 {
		return r.Docs
	}
	return DefaultReleaseDocs
}

// SchemeName is release.scheme, else semver.
func (r ReleaseConfig) SchemeName() string {
	if r.Scheme == "" {
		return ReleaseSemver
	}
	return r.Scheme
}
