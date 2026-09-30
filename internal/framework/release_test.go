package framework_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// releaseRepo builds a released-layout framework repo whose skill apex-foo
// says which release it is, with one commit per tag, in order.
func releaseRepo(t *testing.T, tags ...string) string {
	t.Helper()
	fw := t.TempDir()
	fakeFramework(t, fw, "")
	for _, tag := range tags {
		require.NoError(t, os.WriteFile(filepath.Join(fw, ".claude", "skills", "apex-foo", "SKILL.md"),
			[]byte("# apex-foo "+tag), 0o644))
		commitAll(t, fw, "release "+tag)
		gitIn(t, fw, "tag", "-a", tag, "-m", tag)
	}
	return fw
}

func installedFoo(t *testing.T, proj string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(proj, ".claude", "skills", "apex-foo", "SKILL.md"))
	require.NoError(t, err)
	return string(b)
}

func setupRelease(t *testing.T, fw string, sel framework.ReleaseSelector) (*framework.UpdateResult, string, error) {
	t.Helper()
	proj := newProject(t)
	res, err := framework.Setup(context.Background(), &framework.UpdateOptions{
		FrameworkRepo: fw, ProjectRoot: proj, ApeVersion: "0.4.0",
		Bootstrapper: staticBootstrap("p"), Now: fixedNow, Release: &sel,
	})
	return res, proj, err
}

// The default is the highest FINAL tag by semver: v0.10.0 beats v0.9.0 (a
// string sort gets that backwards), and a newer candidate is never picked.
func TestRelease_DefaultIsTheHighestFinalTag(t *testing.T) {
	fw := releaseRepo(t, "v0.9.0", "v0.10.0", "v0.11.0-rc.1")
	res, proj, err := setupRelease(t, fw, framework.ReleaseSelector{})
	require.NoError(t, err)
	require.Equal(t, "# apex-foo v0.10.0", installedFoo(t, proj))

	meta := res.Metadata.Framework
	require.Equal(t, framework.SourceTag, meta.Source)
	require.Equal(t, "v0.10.0", meta.VersionTag)
	require.Equal(t, gitIn(t, fw, "rev-parse", "v0.10.0^{commit}"), meta.GitHash)
	require.Len(t, meta.GitHash, 40, "the full sha is recorded")
	require.Empty(t, meta.GitBranch, "a release install has no branch")

	onDisk, err := framework.ReadMetadata(proj)
	require.NoError(t, err)
	require.Equal(t, meta, onDisk.Framework)
}

func TestRelease_VersionPicksExactlyThatTag_CandidatesIncluded(t *testing.T) {
	fw := releaseRepo(t, "v1.0.0", "v1.1.0", "v1.2.0-rc.1")
	for _, tag := range []string{"v1.2.0-rc.1", "v1.0.0"} {
		res, proj, err := setupRelease(t, fw, framework.ReleaseSelector{Version: tag})
		require.NoError(t, err, tag)
		require.Equal(t, "# apex-foo "+tag, installedFoo(t, proj))
		require.Equal(t, tag, res.Metadata.Framework.VersionTag)
	}
}

func TestRelease_RefusesWhatItCannotInstall(t *testing.T) {
	fw := releaseRepo(t, "v1.0.0")
	cases := map[string]struct {
		repo string
		sel  framework.ReleaseSelector
		code string
	}{
		"missing tag":   {fw, framework.ReleaseSelector{Version: "v9.9.9"}, "framework_tag_missing"},
		"bad shape":     {fw, framework.ReleaseSelector{Version: "v1.0.0-beta"}, "framework_version_invalid"},
		"no release":    {releaseRepo(t, "v2.0.0-rc.1"), framework.ReleaseSelector{}, "framework_no_release"},
		"bare word tag": {fw, framework.ReleaseSelector{Version: "latest"}, "framework_version_invalid"},
	}
	for name, c := range cases {
		_, proj, err := setupRelease(t, c.repo, c.sel)
		var ve *framework.ValidationError
		require.ErrorAs(t, err, &ve, name)
		require.Equal(t, c.code, ve.Code, name)
		_, statErr := os.Stat(framework.MetadataPath(proj))
		require.True(t, os.IsNotExist(statErr), "%s: nothing is written", name)
	}
}

// The clone is read for objects only: a dirty working tree on another
// branch installs the tag's content, and the clone is left exactly as it was.
func TestRelease_IgnoresTheClonesCheckout(t *testing.T) {
	fw := releaseRepo(t, "v1.0.0", "v1.1.0")
	gitIn(t, fw, "checkout", "-q", "-b", "wip", "v1.0.0")
	skill := filepath.Join(fw, ".claude", "skills", "apex-foo", "SKILL.md")
	require.NoError(t, os.WriteFile(skill, []byte("# uncommitted work"), 0o644))

	_, proj, err := setupRelease(t, fw, framework.ReleaseSelector{})
	require.NoError(t, err)
	require.Equal(t, "# apex-foo v1.1.0", installedFoo(t, proj))

	require.Equal(t, "wip", gitIn(t, fw, "rev-parse", "--abbrev-ref", "HEAD"))
	b, err := os.ReadFile(skill)
	require.NoError(t, err)
	require.Equal(t, "# uncommitted work", string(b), "the clone's working tree is untouched")
}

// A build-repo tag nests the payload under framework/, and a release
// install refuses it by name rather than installing nothing.
func TestRelease_RefusesTheBuildLayout(t *testing.T) {
	fw := t.TempDir()
	fakeFramework(t, fw, "")
	require.NoError(t, os.MkdirAll(filepath.Join(fw, "framework"), 0o755))
	gitIn(t, fw, "mv", "_apex", "framework/_apex")
	gitIn(t, fw, "mv", ".claude", "framework/_claude")
	commitAll(t, fw, "build layout")
	gitIn(t, fw, "tag", "v1.0.0")

	_, _, err := setupRelease(t, fw, framework.ReleaseSelector{})
	var ve *framework.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "framework_layout_invalid", ve.Code)
	require.Contains(t, ve.Detail, "BUILD layout")
}

// No origin at all, with a fetch requested: a warning, and the tags the
// clone has still install. --no-fetch (Fetch false) is silent.
func TestRelease_FailedFetchWarnsAndUsesLocalTags(t *testing.T) {
	fw := releaseRepo(t, "v1.0.0")
	res, _, err := setupRelease(t, fw, framework.ReleaseSelector{Fetch: true})
	require.NoError(t, err)
	require.Contains(t, res.FetchWarning, "could not fetch tags")
	require.Equal(t, "v1.0.0", res.Metadata.Framework.VersionTag)

	res, _, err = setupRelease(t, fw, framework.ReleaseSelector{})
	require.NoError(t, err)
	require.Empty(t, res.FetchWarning)
}

func TestStatus_ReleaseDriftAndAMovedTag(t *testing.T) {
	ctx := context.Background()
	fw := releaseRepo(t, "v1.0.0")
	_, proj, err := setupRelease(t, fw, framework.ReleaseSelector{})
	require.NoError(t, err)

	st, err := framework.Status(ctx, framework.StatusOptions{ProjectRoot: proj, FrameworkRepo: fw, NoFetch: true})
	require.NoError(t, err)
	require.False(t, st.Drift.TagDrift)
	require.False(t, st.Drift.TagMoved)

	// A newer release is drift; the working tree's HEAD is not consulted.
	require.NoError(t, os.WriteFile(filepath.Join(fw, "x.txt"), []byte("x"), 0o644))
	commitAll(t, fw, "unreleased work")
	st, err = framework.Status(ctx, framework.StatusOptions{ProjectRoot: proj, FrameworkRepo: fw, NoFetch: true})
	require.NoError(t, err)
	require.False(t, st.Drift.TagDrift, "an untagged commit on main is not a release")
	gitIn(t, fw, "tag", "v1.1.0")
	st, err = framework.Status(ctx, framework.StatusOptions{ProjectRoot: proj, FrameworkRepo: fw, NoFetch: true})
	require.NoError(t, err)
	require.True(t, st.Drift.TagDrift)
	require.Equal(t, "v1.1.0", st.Current.VersionTag)

	// The installed tag moved to another commit.
	gitIn(t, fw, "tag", "-f", "v1.0.0", "HEAD")
	st, err = framework.Status(ctx, framework.StatusOptions{ProjectRoot: proj, FrameworkRepo: fw, NoFetch: true})
	require.NoError(t, err)
	require.True(t, st.Drift.TagMoved)
	require.Contains(t, strings.Join(st.Drift.Notes, "\n"), "framework tag moved: v1.0.0 was installed at")
}

func TestReadAtTag_MinApeVersion(t *testing.T) {
	ctx := context.Background()
	fw := releaseRepo(t, "v1.0.0")
	_, found, err := framework.ReadAtTag(ctx, fw, "v1.0.0", framework.ProjectApeCommands)
	require.NoError(t, err)
	require.False(t, found, "a release with no manifest has none")

	require.NoError(t, os.WriteFile(filepath.Join(fw, framework.ProjectApeCommands),
		[]byte("min_ape_version: 0.4.0\nrequired_commands:\n  - ape version\n"), 0o644))
	commitAll(t, fw, "manifest")
	gitIn(t, fw, "tag", "v1.1.0")
	data, found, err := framework.ReadAtTag(ctx, fw, "v1.1.0", framework.ProjectApeCommands)
	require.NoError(t, err)
	require.True(t, found)
	m, err := framework.ParseApeCommands(data)
	require.NoError(t, err)
	require.Equal(t, "0.4.0", m.MinApeVersion)
	require.Equal(t, []string{"ape version"}, m.Required)
}

func TestCompareApeVersion(t *testing.T) {
	cases := []struct {
		running, minimum string
		want             framework.ApeVersionVerdict
	}{
		{"0.4.0", "0.4.0", framework.ApeVersionOK},
		{"0.4.1", "0.4.0", framework.ApeVersionOK},
		{"v0.4.0", "v0.4.0", framework.ApeVersionOK},
		{"0.3.9", "0.4.0", framework.ApeVersionBelow},
		{"0.4.0-rc.2", "0.4.0", framework.ApeVersionOK},    // the build that becomes 0.4.0
		{"0.4.0-rc.2", "0.4.1", framework.ApeVersionBelow}, // an rc satisfies only its own version
		{"0.5.0-rc.1", "0.4.0", framework.ApeVersionOK},    // ordinary semver above it
		{"0.4.0", "0.4.0-rc.1", framework.ApeVersionOK},
		{"dev", "0.4.0", framework.ApeVersionUnknown},
		{"0.3.2-0.20260930123456-abcdef123456", "0.4.0", framework.ApeVersionUnknown},
		{"0.4.0", "", framework.ApeVersionNoMinimum},
		{"0.4.0", "not-a-version", framework.ApeVersionUnknown},
	}
	for _, c := range cases {
		require.Equal(t, c.want, framework.CompareApeVersion(c.running, c.minimum), "%s vs >= %s", c.running, c.minimum)
	}
}

// A candidate installed by name, newer than the newest final release, is
// "ahead": status says so and does not call it drift to update away from.
func TestStatus_ACandidateAheadOfTheNewestRelease(t *testing.T) {
	fw := releaseRepo(t, "v1.0.0", "v1.1.0-rc.1")
	_, proj, err := setupRelease(t, fw, framework.ReleaseSelector{Version: "v1.1.0-rc.1"})
	require.NoError(t, err)
	st, err := framework.Status(context.Background(), framework.StatusOptions{ProjectRoot: proj, FrameworkRepo: fw, NoFetch: true})
	require.NoError(t, err)
	require.True(t, st.Drift.Ahead)
	require.False(t, st.Drift.TagDrift)
	require.True(t, framework.InstalledIsAhead("v1.1.0-rc.1", "v1.0.0"))
	require.False(t, framework.InstalledIsAhead("v1.1.0-rc.1", "v1.1.0"), "the final release is ahead of its own rc")
}
