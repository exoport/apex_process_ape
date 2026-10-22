package apexv1

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// bareTagged is a local bare repository holding one commit and the tags,
// a marketplace `git ls-remote` reads with no network.
func bareTagged(t *testing.T, tags ...string) string {
	t.Helper()
	gitEnv(t)
	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "mkt.git")
	run(t, work, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("mkt\n"), 0o644))
	run(t, work, "add", "-A")
	run(t, work, "commit", "-q", "-m", "catalog")
	for _, tag := range tags {
		run(t, work, "tag", "-a", tag, "-m", tag)
	}
	c := exec.Command("git", "clone", "-q", "--bare", work, bare)
	out, err := c.CombinedOutput()
	require.NoError(t, err, string(out))
	return bare
}

// W6 (Q-E7): plugins status --outdated lists, per listed marketplace, the
// release tags newer than its ref, newest first — pre-releases and other
// tags left out, a leading v optional.
func TestOutdatedMarketplaces(t *testing.T) {
	isolatedClaudeDir(t)
	bare := bareTagged(t, "v0.1.0", "v0.2.0", "0.10.0", "v0.3.0-rc.1", "nightly", "v0.0.9")
	p := settingsProject(t, "")
	writeConfigForTest(t, p.Root, "marketplaces:\n"+
		"  pinned: {source: git, url: "+bare+", ref: v0.1.0}\n"+
		"  latest: {source: git, url: "+bare+", ref: 0.10.0}\n"+
		"  branch: {source: git, url: "+bare+", ref: main}\n"+
		"  free: {source: git, url: "+bare+"}\n"+
		"  local: {source: directory, path: ../mkt}\n"+
		"  gone: {source: git, url: "+filepath.Join(t.TempDir(), "nope.git")+", ref: v1.0.0}\n")
	rows, err := p.OutdatedMarketplaces(ctx())
	require.NoError(t, err)
	byName := map[string]MarketplaceTags{}
	for _, r := range rows {
		byName[r.Marketplace] = r
	}
	require.Equal(t, []string{"branch", "free", "gone", "latest", "local", "pinned"}, func() []string {
		n := make([]string, 0, len(rows))
		for _, r := range rows {
			n = append(n, r.Marketplace)
		}
		return n
	}())
	require.Equal(t, MarketplaceTags{Marketplace: "pinned", Source: "git", Ref: "v0.1.0", Latest: "0.10.0", Newer: []string{"0.10.0", "v0.2.0"}}, byName["pinned"])
	require.Equal(t, []string{}, byName["latest"].Newer)
	require.Empty(t, byName["latest"].Note)
	require.Equal(t, "ref main is not a version tag; pin a tag to compare (latest 0.10.0)", byName["branch"].Note)
	require.Equal(t, "no ref: the marketplace follows its default branch; pin a tag (ref: 0.10.0) to compare", byName["free"].Note)
	require.Equal(t, "a directory marketplace has no tags", byName["local"].Note)
	require.True(t, strings.HasPrefix(byName["gone"].Error, "git ls-remote "), byName["gone"].Error)
	require.Equal(t, []string{}, byName["gone"].Newer)
}

func TestNewerTags(t *testing.T) {
	latest, newer, note := newerTags([]string{"v1.2.0", "1.10.0", "v1.9.9", "v2.0.0-beta.1", "x"}, "v1.2.0")
	require.Equal(t, "1.10.0", latest)
	require.Equal(t, []string{"1.10.0", "v1.9.9"}, newer)
	require.Empty(t, note)
	_, newer, note = newerTags(nil, "v1")
	require.Equal(t, []string{}, newer)
	require.Equal(t, "no version tags", note)
	require.Equal(t, "https://github.com/exoar/apish_experts_plugin.git", Marketplace{Source: "github", Repo: "exoar/apish_experts_plugin", Ref: "v0.1.0"}.remoteURL())
}
