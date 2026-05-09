package trait

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrCatalogNotFound is returned when the catalog.yaml cannot be located.
var ErrCatalogNotFound = errors.New("trait catalog not found")

// NotFoundError wraps a trait-not-found error.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("trait not found: %q", e.Name)
}

// IsNotFoundError returns true if the error is a NotFoundError.
func IsNotFoundError(err error) bool {
	var nfe *NotFoundError
	return errors.As(err, &nfe)
}

// CatalogBaseDir returns the directory from which traits are loaded.
// Traits live at <base>/traits/, ADRs at <base>/../adrs/, patterns at <base>/../patterns/.
func CatalogBaseDir() string {
	if repo := os.Getenv("APE_PROCESS_REPO"); repo != "" {
		return filepath.Join(repo, "testdata", "governance", "traits")
	}
	return filepath.Join("testdata", "governance", "traits")
}

// LoadCatalog reads the trait catalog from the process repo or working directory.
func LoadCatalog() (*Catalog, error) {
	base := CatalogBaseDir()
	catalogPath := filepath.Join(base, "catalog.yaml")

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrCatalogNotFound, catalogPath)
		}
		return nil, fmt.Errorf("cannot read catalog %s: %w", catalogPath, err)
	}

	var catalog Catalog
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("cannot parse catalog %s: %w", catalogPath, err)
	}

	return &catalog, nil
}

// LoadTrait loads a trait by name from the catalog.
func LoadTrait(name string) (*Trait, error) {
	catalog, err := LoadCatalog()
	if err != nil {
		return nil, err
	}

	for _, ref := range catalog.Traits {
		if ref.Name == name {
			return loadTraitFile(ref.File)
		}
	}

	return nil, &NotFoundError{Name: name}
}

// LoadTraitFromFile loads a trait directly from a YAML file path.
func LoadTraitFromFile(file string) (*Trait, error) {
	return loadTraitFile(file)
}

func loadTraitFile(file string) (*Trait, error) {
	path := file
	if !filepath.IsAbs(path) {
		base := CatalogBaseDir()
		candidate := filepath.Join(base, path)
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read trait file %s: %w", path, err)
	}

	var t Trait
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("cannot parse trait file %s: %w", path, err)
	}

	return &t, nil
}
