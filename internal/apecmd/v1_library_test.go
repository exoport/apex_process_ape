package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// v1Library is an initialised v1 Go library: a root module with no main
// package. init suggests library: true; the key is added here.
func v1Library(t *testing.T) string {
	t.Helper()
	root := initGitRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/lib\n\ngo 1.27\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "lib.go"), []byte("package lib\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "lib")
	stdout, _, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "suggest: add \"library: true\" to _apex/config.yaml if this is a library others download — go.mod declares module example.com/lib and no package is main")
	cfg := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(data), "library", "init never sets the key")
	require.NoError(t, os.WriteFile(cfg, append(data, []byte("library: true\n")...), 0o644))
	v1Git(t, root, "commit", "-q", "--no-verify", "-am", "a library")
	return root
}

// fence --check's exits are a migration check's: 0 applied, 1 not
// applied, 2 the check failed.
func TestV1ProjectFence_CheckExits(t *testing.T) {
	root := v1Library(t)
	stdout, stderr, err := v1Exec(t, newProjectCmd, "fence", "--check", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Empty(t, stdout)
	require.Contains(t, stderr, "module fence development/go.mod is missing")

	stdout, _, err = v1Exec(t, newProjectCmd, "fence", "--check", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	var fail struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
		Fences      []apexv1.Fence      `json:"fences"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &fail))
	require.Equal(t, apexv1.CodeFenceMissing, fail.Diagnostics[0].Code)
	require.Len(t, fail.Fences, 3)

	stdout, _, err = v1Exec(t, newProjectCmd, "fence", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "wrote     .claude/go.mod\nwrote     _apex/go.mod\nwrote     development/go.mod\ncommit ")
	stdout, _, err = v1Exec(t, newProjectCmd, "fence", "--check", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, "module fences: all 3 in place (.claude/go.mod, _apex/go.mod, development/go.mod)\n", stdout)

	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/lib\nrequire (\n"), 0o644))
	_, _, err = v1Exec(t, newProjectCmd, "fence", "--check", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "fence.unreadable: the check itself failed")
}

// package-check: a leak exits 1 with library.package-leak, a fenced
// library exits 0, and the JSON carries the packages.
func TestV1ProjectPackageCheck(t *testing.T) {
	root := v1Library(t)
	stdout, _, err := v1Exec(t, newProjectCmd, "package-check", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	var out struct {
		Diagnostics []apexv1.Diagnostic    `json:"diagnostics"`
		Packages    []apexv1.PackageReport `json:"packages"`
		Notices     []apexv1.Diagnostic    `json:"notices"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	require.Equal(t, apexv1.CodePackageLeak, out.Diagnostics[0].Code)
	require.Equal(t, apexv1.CodePackageLeak, out.Diagnostics[1].Code)
	require.Equal(t, "replan", out.Diagnostics[1].Action)
	require.Len(t, out.Packages, 1)
	require.NotNil(t, out.Notices)

	_, _, err = v1Exec(t, newProjectCmd, "fence", "--cwd", root)
	require.NoError(t, err)
	stdout, _, err = v1Exec(t, newProjectCmd, "package-check", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "package   go . (go.mod): ")
	require.Contains(t, stdout, "none under an APEX folder (golang.org/x/mod/zip at HEAD)\npackage-check: green\n")
}
