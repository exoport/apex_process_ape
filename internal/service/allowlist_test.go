package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo makes dir look like a git working tree (a .git marker) so the
// allowlist validator accepts it without a real git binary.
func gitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return dir
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "service.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestLoadConfigValidAndMatch(t *testing.T) {
	root := gitRepo(t, t.TempDir())
	comp := gitRepo(t, t.TempDir())
	cfgDir := t.TempDir()
	p := writeConfig(t, cfgDir, "project_root: "+root+"\nallow:\n  - "+comp+"\n")

	c, err := LoadConfigFile(p)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	// project_root is implicitly allowed and deduped to the front.
	if !c.Allowed(root) {
		t.Errorf("project_root should be allowed")
	}
	if !c.Allowed(comp) {
		t.Errorf("component repo should be allowed")
	}
	// Exact match only — a subdir of an allowed root is not allowed.
	if c.Allowed(filepath.Join(root, "sub")) {
		t.Errorf("a subdir of an allowed root must not match")
	}
	// Trailing slash / uncleaned input still matches (path-cleaned).
	if !c.Allowed(root + "/") {
		t.Errorf("path-cleaned match should accept a trailing slash")
	}
	if c.Allowed("") {
		t.Errorf("empty project_root must not match")
	}
}

func TestLoadConfigScriptGates(t *testing.T) {
	root := gitRepo(t, t.TempDir())
	// Both D5 gates on: force_script_sandbox is valid because
	// allow_script_source is also set.
	p := writeConfig(t, t.TempDir(), "project_root: "+root+
		"\nallow_script_source: true\nforce_script_sandbox: true\n")
	c, err := LoadConfigFile(p)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if !c.AllowScriptSource || !c.ForceScriptSandbox {
		t.Fatalf("D5 gates not parsed: %+v", c)
	}

	// Both default to false when omitted.
	def := writeConfig(t, t.TempDir(), "project_root: "+root+"\n")
	c2, err := LoadConfigFile(def)
	if err != nil {
		t.Fatalf("LoadConfigFile default: %v", err)
	}
	if c2.AllowScriptSource || c2.ForceScriptSandbox {
		t.Fatalf("D5 gates should default off: %+v", c2)
	}
}

func TestValidateRejects(t *testing.T) {
	realRepo := gitRepo(t, t.TempDir())
	notARepo := t.TempDir() // exists, but no .git

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no project_root", Config{Allow: []string{realRepo}}, "project_root is required"},
		{"relative project_root", Config{ProjectRoot: "rel/path"}, "absolute path"},
		{"missing allow entry", Config{ProjectRoot: realRepo, Allow: []string{realRepo, filepath.Join(realRepo, "nope")}}, "does not exist"},
		{"allow entry not a git repo", Config{ProjectRoot: realRepo, Allow: []string{notARepo}}, "not a git repository"},
		{"force sandbox without allow_script_source", Config{ProjectRoot: realRepo, Allow: []string{realRepo}, ForceScriptSandbox: true}, "force_script_sandbox"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestResolveConfigPath(t *testing.T) {
	// Explicit override wins.
	if got := ResolveConfigPath("/x/y.yaml", "/proj"); got != "/x/y.yaml" {
		t.Errorf("override: got %q", got)
	}
	// Project-local path is returned when it exists.
	proj := t.TempDir()
	local := filepath.Join(proj, filepath.FromSlash(ConfigFileName))
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("project_root: /x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveConfigPath("", proj); got != local {
		t.Errorf("project-local: got %q, want %q", got, local)
	}
	// With no file anywhere, the project-local path is returned (so the
	// not-found error names it).
	empty := t.TempDir()
	wantLocal := filepath.Join(empty, filepath.FromSlash(ConfigFileName))
	if got := ResolveConfigPath("", empty); got != wantLocal {
		t.Errorf("fallback: got %q, want %q", got, wantLocal)
	}
}

func TestLoadConfigUnknownKey(t *testing.T) {
	root := gitRepo(t, t.TempDir())
	p := writeConfig(t, t.TempDir(), "project_root: "+root+"\nallo: []\n") // typo: allo
	if _, err := LoadConfigFile(p); err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("expected a parse error for an unknown key, got %v", err)
	}
}
