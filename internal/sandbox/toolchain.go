package sandbox

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

/*
Durable toolchain state (PLAN-22 D4).

A workspace's VM rootfs is destroyed on `down`, so toolchain and dependency
state must not live only in it: an `asdf install` or `go mod download` that
vanishes on teardown makes every rebuild an online rebuild. Instead each cache
is a DURABLE host mount, which also means a workspace is offline-capable once
warm and that caches can be pre-warmed on the host.

Two deliberate choices:

 1. Caches mount OUTSIDE the guest home. /sandbox/home is a system mount
    (aped composes it), so a cache cannot be nested inside it — and shadowing
    part of the composed home would be a footgun anyway. Each cache lands at
    /cache/<name> and the tools are pointed at it with environment variables.
 2. The env is derived SERVER-SIDE from the mount aped actually applied, not
    sent by the caller. A caller that could inject GOPATH/ASDF_DATA_DIR could
    point a tool at a path of its choosing; here it can only pick a cache NAME
    from this fixed table.
*/

// ToolCache is one durable, host-backed cache a workspace can mount.
type ToolCache struct {
	// Name is the wire/descriptor identifier (`caches: [go, asdf]`).
	Name string
	// SubDir is the directory under the node's cache root that backs it.
	SubDir string
	// Dest is the guest mount point.
	Dest string
	// Env are the KEY=VALUE pairs that point the toolchain at Dest.
	Env []string
}

// CacheRoot is the guest parent for durable tool caches.
const CacheRoot = "/cache"

// DefaultCacheRoot is the host directory holding the per-tool cache dirs. It
// matches deploy/dev-host.sh, which creates them root:ape 2775.
const DefaultCacheRoot = "/srv/ape-caches"

// toolCaches is the closed set of caches a workspace may request. Adding one is a
// deliberate, reviewable act — the guest env it implies is part of the entry.
var toolCaches = map[string]ToolCache{
	"asdf": {
		Name: "asdf", SubDir: "asdf", Dest: path.Join(CacheRoot, "asdf"),
		// asdf (the Go rewrite) keeps installed versions + plugins under its data dir.
		Env: []string{"ASDF_DATA_DIR=" + path.Join(CacheRoot, "asdf")},
	},
	"go": {
		Name: "go", SubDir: "go", Dest: path.Join(CacheRoot, "go"),
		// GOPATH carries the module cache and bingo's version-stamped $GOBIN; GOMODCACHE
		// is set explicitly because a GOPATH-relative default is easy to lose to a later
		// env change, and GOCACHE (build cache) is the other half of an offline rebuild.
		Env: []string{
			"GOPATH=" + path.Join(CacheRoot, "go"),
			"GOMODCACHE=" + path.Join(CacheRoot, "go", "pkg", "mod"),
			"GOCACHE=" + path.Join(CacheRoot, "go", "build-cache"),
			"GOBIN=" + path.Join(CacheRoot, "go", "bin"),
		},
	},
	"cargo": {
		Name: "cargo", SubDir: "cargo", Dest: path.Join(CacheRoot, "cargo"),
		Env: []string{"CARGO_HOME=" + path.Join(CacheRoot, "cargo")},
	},
	"npm": {
		Name: "npm", SubDir: "npm", Dest: path.Join(CacheRoot, "npm"),
		Env: []string{"npm_config_cache=" + path.Join(CacheRoot, "npm")},
	},
	"pub": {
		Name: "pub", SubDir: "pub-cache", Dest: path.Join(CacheRoot, "pub"),
		Env: []string{"PUB_CACHE=" + path.Join(CacheRoot, "pub")},
	},
}

// DefaultToolCaches are the caches a workspace gets when it declares a toolchain
// without naming any: the two that make a Go project offline-capable after one
// warm-up.
var DefaultToolCaches = []string{"asdf", "go"}

// LookupToolCache returns the cache definition for a name.
func LookupToolCache(name string) (ToolCache, error) {
	tc, ok := toolCaches[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return ToolCache{}, fmt.Errorf("sandbox: unknown tool cache %q (known: %s)",
			name, strings.Join(ToolCacheNames(), ", "))
	}
	return tc, nil
}

// ToolCacheNames returns every known cache name, sorted.
func ToolCacheNames() []string {
	out := make([]string, 0, len(toolCaches))
	for name := range toolCaches {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// NormalizeToolCaches de-duplicates, sorts, and validates a requested cache set.
func NormalizeToolCaches(names []string) ([]string, error) {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		tc, err := LookupToolCache(n)
		if err != nil {
			return nil, err
		}
		if seen[tc.Name] {
			continue
		}
		seen[tc.Name] = true
		out = append(out, tc.Name)
	}
	sort.Strings(out)
	return out, nil
}

// ToolchainCaches returns the cache names a descriptor's toolchain section asks
// for: its explicit list, or the defaults when it declares a toolchain but names
// none. A descriptor with no toolchain section gets no caches.
func (d *Descriptor) ToolchainCaches() []string {
	if d == nil || d.Toolchain == nil {
		return nil
	}
	if len(d.Toolchain.Caches) > 0 {
		return d.Toolchain.Caches
	}
	return DefaultToolCaches
}

// ToolchainSetupScript renders the in-guest setup step for a toolchain (PLAN-22
// D3): materialize the declared runtime versions with asdf, then the repo's pinned
// Go tools with bingo.
//
// It is idempotent by construction — both tools no-op when the durable caches are
// already warm, which is what makes a warm workspace offline-capable — and it is
// generated as one `sh -c` script so a single exec does the whole step.
func ToolchainSetupScript(tc *DescriptorToolchain, repoDir string) (string, error) {
	if tc == nil {
		return "", nil
	}
	if strings.TrimSpace(repoDir) == "" {
		return "", errors.New("sandbox: toolchain setup needs the main repo directory")
	}
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "cd %s\n", shellQuote(repoDir))

	// Inline tools are written to a .tool-versions asdf can read. This is the escape
	// hatch for a project that does not want the native file committed.
	toolVersions := strings.TrimSpace(tc.ToolVersions)
	if len(tc.Tools) > 0 {
		fmt.Fprintf(&b, "printf '%%s\\n' %s > .tool-versions.ape\n", shellQuoteAll(tc.Tools))
		toolVersions = ".tool-versions.ape"
	}
	if toolVersions != "" {
		b.WriteString("if command -v asdf >/dev/null 2>&1; then\n")
		fmt.Fprintf(&b, "  echo '==> asdf install (%s)'\n", toolVersions)
		// asdf reads .tool-versions from the current directory; ASDF_DEFAULT_TOOL_VERSIONS_FILENAME
		// points it at a non-default name without moving the file.
		fmt.Fprintf(&b, "  ASDF_DEFAULT_TOOL_VERSIONS_FILENAME=%s asdf install\n", shellQuote(toolVersions))
		b.WriteString("else\n  echo '!! asdf is not in this image — skipping runtime install' >&2\nfi\n")
	}
	if tc.Bingo {
		b.WriteString("if command -v bingo >/dev/null 2>&1; then\n")
		b.WriteString("  echo '==> bingo get (pinned Go tools)'\n  bingo get\n")
		b.WriteString("else\n  echo '!! bingo is not in this image — skipping Go tools' >&2\nfi\n")
	}
	return b.String(), nil
}

// shellQuote single-quotes a value for /bin/sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellQuoteAll quotes each value, space-separated.
func shellQuoteAll(values []string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, shellQuote(v))
	}
	return strings.Join(out, " ")
}
