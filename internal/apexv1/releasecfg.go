package apexv1

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The project's release scheme (D120): how apex-release proposes the next
// version. ape validates and guards it; it reads no version itself.
//
//	release:
//	  scheme: semver   # semver (default) | date (YYYY.MM.DD) | edition (the next integer)

// Release schemes.
const (
	ReleaseSemver  = "semver"
	ReleaseDate    = "date"
	ReleaseEdition = "edition"
)

// ReleaseConfig is _apex/config.yaml's release block.
type ReleaseConfig struct {
	Scheme string `yaml:"scheme"`
}

// UnmarshalYAML validates the block as it parses.
func (r *ReleaseConfig) UnmarshalYAML(n *yaml.Node) error {
	if err := checkKeys(n, "release", "scheme"); err != nil {
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
	*r = ReleaseConfig(p)
	return nil
}

// SchemeName is release.scheme, else semver.
func (r ReleaseConfig) SchemeName() string {
	if r.Scheme == "" {
		return ReleaseSemver
	}
	return r.Scheme
}
