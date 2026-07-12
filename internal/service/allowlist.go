package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigFileName is the project-relative service config path (D2). When the
// daemon is not started inside a project, it falls back to
// ~/.ape/service.yaml (see ResolveConfigPath).
const ConfigFileName = "_apex/service.yaml"

// Config is the parsed service.yaml: the project the daemon serves, the
// exact-match allowlist of roots any *.run request may name (the project
// plus its sibling component repositories), and the script-job safety
// gates (D5).
//
//nolint:tagliatelle // snake_case is the stable, documented on-disk schema
type Config struct {
	// ProjectRoot is the primary project the daemon is bound to. It is
	// always implicitly allowed (added to Allow if absent).
	ProjectRoot string `yaml:"project_root"`
	// Allow lists every absolute root a request may target. Matching is
	// exact (after path-cleaning) — one daemon, several sibling repos,
	// nothing else.
	Allow []string `yaml:"allow"`
	// AllowScriptSource enables the script.run script_source variant
	// (arbitrary code on the daemon host — off by default, D5).
	AllowScriptSource bool `yaml:"allow_script_source,omitempty"`
	// ForceScriptSandbox forces PLAN-15's interpreter-level --sandbox onto
	// every script job (recommended whenever AllowScriptSource is on, D5).
	ForceScriptSandbox bool `yaml:"force_script_sandbox,omitempty"`

	// path records where the config was loaded from, for diagnostics.
	path string `yaml:"-"`
}

// ResolveConfigPath returns the service.yaml path to load: an explicit
// override wins; else <projectRoot>/_apex/service.yaml if it exists; else
// ~/.ape/service.yaml. It returns the first existing candidate, or the
// project-local path (so a "not found" error names the expected location).
func ResolveConfigPath(override, projectRoot string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	local := filepath.Join(projectRoot, filepath.FromSlash(ConfigFileName))
	if _, err := os.Stat(local); err == nil {
		return local
	}
	if home, err := os.UserHomeDir(); err == nil {
		homePath := filepath.Join(home, ".ape", "service.yaml")
		if _, err := os.Stat(homePath); err == nil {
			return homePath
		}
	}
	return local
}

// LoadConfig reads and validates the service config resolved from override
// / projectRoot. A missing file returns an error naming the expected path.
func LoadConfig(override, projectRoot string) (*Config, error) {
	return LoadConfigFile(ResolveConfigPath(override, projectRoot))
}

// LoadConfigFile reads, parses, and validates a service config from an
// explicit path.
func LoadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("service: config not found at %s (create it or pass --config)", path)
		}
		return nil, fmt.Errorf("service: read config %s: %w", path, err)
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // a misspelled key is a hard error, not a silent drop
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("service: parse config %s: %w", path, err)
	}
	c.path = path
	c.normalize()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("service: invalid config %s: %w", path, err)
	}
	return &c, nil
}

// Path returns the file the config was loaded from (empty when built in
// memory, e.g. in tests).
func (c *Config) Path() string { return c.path }

// normalize cleans ProjectRoot + every Allow entry to a canonical path and
// ensures ProjectRoot is present in Allow exactly once. Idempotent.
func (c *Config) normalize() {
	c.ProjectRoot = cleanPath(c.ProjectRoot)
	seen := make(map[string]bool, len(c.Allow)+1)
	out := make([]string, 0, len(c.Allow)+1)
	add := func(p string) {
		p = cleanPath(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if c.ProjectRoot != "" {
		add(c.ProjectRoot)
	}
	for _, a := range c.Allow {
		add(a)
	}
	c.Allow = out
}

// Validate enforces the D2 invariants: project_root is set and absolute,
// and every allow entry is an absolute path to an existing git working
// tree. A component repo that would fail a job later should fail at
// startup, not mid-request.
func (c *Config) Validate() error {
	c.normalize()
	if c.ProjectRoot == "" {
		return errors.New("project_root is required")
	}
	if !filepath.IsAbs(c.ProjectRoot) {
		return fmt.Errorf("project_root must be an absolute path, got %q", c.ProjectRoot)
	}
	if len(c.Allow) == 0 {
		return errors.New("allow must list at least the project_root")
	}
	for _, a := range c.Allow {
		if !filepath.IsAbs(a) {
			return fmt.Errorf("allow entry must be an absolute path, got %q", a)
		}
		info, err := os.Stat(a)
		if err != nil {
			// Portable message: the OS-specific stat error text ("no such
			// file" on Unix, "cannot find the file specified" on Windows)
			// is unstable to assert on, so name the condition ourselves.
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("allow entry %q does not exist: %w", a, err)
			}
			return fmt.Errorf("allow entry %q: %w", a, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("allow entry %q is not a directory", a)
		}
		if !isGitRepo(a) {
			return fmt.Errorf("allow entry %q is not a git repository (no .git)", a)
		}
	}
	if c.ForceScriptSandbox && !c.AllowScriptSource {
		// Not fatal, but a config smell: force_script_sandbox only bites
		// script_source jobs, which are disabled. Surface it as an error so
		// the operator's intent is unambiguous.
		return errors.New("force_script_sandbox is set but allow_script_source is not — enable allow_script_source or drop force_script_sandbox")
	}
	return nil
}

// Allowed reports whether root exactly matches an allowlist entry (after
// path-cleaning). This is the PROJECT_NOT_ALLOWED gate.
func (c *Config) Allowed(root string) bool {
	root = cleanPath(root)
	if root == "" {
		return false
	}
	return slices.Contains(c.Allow, root)
}

// cleanPath trims and filepath.Cleans a path, leaving "" as "". It does not
// resolve symlinks: allowlist entries and requests are compared as canonical
// lexical paths, which the operator controls.
func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// isGitRepo reports whether dir holds a .git entry (a directory for a
// normal clone, or a file for a linked worktree/submodule).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
