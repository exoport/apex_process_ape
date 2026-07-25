package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// credFixture writes a fake host credential and returns (source, publishDest).
func credFixture(t *testing.T) (source, dest string) {
	t.Helper()
	home := t.TempDir()
	source = filepath.Join(home, ".claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o700))
	require.NoError(t, os.WriteFile(source, []byte(`{"access_token":"live-1"}`), 0o600))
	return source, filepath.Join(t.TempDir(), "diegos", credentialRelPath)
}

func TestPublishCredentialHardLinkSharesOneInode(t *testing.T) {
	src, dest := credFixture(t)

	msg, err := publishCredential(src, dest, false)
	require.NoError(t, err)
	assert.Contains(t, msg, "hard link")

	si, err := os.Stat(src)
	require.NoError(t, err)
	di, err := os.Stat(dest)
	require.NoError(t, err)
	assert.True(t, os.SameFile(si, di), "the published path must BE the source file, not a copy")

	// The point of the link: a refresh through either name is visible through the
	// other, so a workspace and the host never hold divergent tokens.
	require.NoError(t, os.WriteFile(dest, []byte(`{"access_token":"refreshed"}`), 0o600))
	back, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Contains(t, string(back), "refreshed")

	// And the source's permissions are untouched — the daemon only stat()s this path.
	assert.Equal(t, os.FileMode(0o600), si.Mode().Perm())
	// The parent directory must be TRAVERSABLE by the daemon's service user, which is
	// not in the publishing user's primary group — hence traverse-only for others,
	// while the credential itself stays unreadable.
	info, err := os.Stat(filepath.Dir(dest))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(credentialDirMode), info.Mode().Perm())
	assert.Zero(t, info.Mode().Perm()&0o004, "others must not be able to LIST the directory")
}

func TestPublishCredentialCopyIsIndependent(t *testing.T) {
	src, dest := credFixture(t)

	msg, err := publishCredential(src, dest, true)
	require.NoError(t, err)
	assert.Contains(t, msg, "COPY")

	si, _ := os.Stat(src)
	di, err := os.Stat(dest)
	require.NoError(t, err)
	assert.False(t, os.SameFile(si, di))
	assert.Equal(t, os.FileMode(0o600), di.Mode().Perm(), "a copy must be no more readable than the original")

	// Divergence is the documented consequence, so pin it: writing one leaves the
	// other alone.
	require.NoError(t, os.WriteFile(dest, []byte(`{"access_token":"workspace-refreshed"}`), 0o600))
	back, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Contains(t, string(back), "live-1")
}

func TestPublishCredentialIsIdempotentAndRepairsAStaleLink(t *testing.T) {
	src, dest := credFixture(t)
	_, err := publishCredential(src, dest, false)
	require.NoError(t, err)
	_, err = publishCredential(src, dest, false)
	require.NoError(t, err, "re-publishing must not fail on an existing target")

	// Simulate the host replacing its credential file (write temp + rename), which
	// decouples the link — the failure mode re-publishing exists to repair.
	tmp := src + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"rotated"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))

	st := credentialStatus(src, dest)
	assert.True(t, st.Present)
	assert.False(t, st.Live, "a decoupled link must be reported as not live")

	_, err = publishCredential(src, dest, false)
	require.NoError(t, err)
	st = credentialStatus(src, dest)
	assert.True(t, st.Live, "re-publishing repairs it")
	assert.Equal(t, credentialModeLink, st.Mode)
}

func TestCredentialStatusWhenNothingPublished(t *testing.T) {
	src, dest := credFixture(t)
	st := credentialStatus(src, dest)
	assert.False(t, st.Present)
	assert.Contains(t, st.summary(), "not published")
}

func TestResolveCredentialSourceRejectsMissing(t *testing.T) {
	_, err := resolveCredentialSource(filepath.Join(t.TempDir(), "nope.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "log in with claude first")
}

func TestCredentialRootPrecedence(t *testing.T) {
	assert.Equal(t, "/flag", credentialRoot("/flag"))
	t.Setenv("APE_CREDENTIAL_ROOT", "/env")
	assert.Equal(t, "/env", credentialRoot(""))
	t.Setenv("APE_CREDENTIAL_ROOT", "")
	assert.Equal(t, DefaultCredentialRoot, credentialRoot(""))
}

// TestStaleLinkIsReportedAsStaleNotAsACopy pins the diagnostic a real `claude /login`
// exposed: the login REPLACES the credential file, so the published link keeps the old
// inode — which by stat alone (one link, different inode) is indistinguishable from a
// deliberate copy. Reporting "copy" there gave the user no reason to re-publish while
// their workspaces silently held the pre-login token.
func TestStaleLinkIsReportedAsStaleNotAsACopy(t *testing.T) {
	src, dest := credFixture(t)
	_, err := publishCredential(src, dest, false)
	require.NoError(t, err)
	require.True(t, credentialStatus(src, dest).Live)

	// Replace the source the way a login does: write a new file, rename over it.
	tmp := src + ".new"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"post-login"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))

	st := credentialStatus(src, dest)
	require.True(t, st.Present)
	assert.False(t, st.Live)
	assert.Equal(t, credentialModeLink, st.Mode, "a decoupled link must not be reported as a copy")
	assert.Contains(t, st.summary(), "STALE")
	assert.Contains(t, st.summary(), "old credential")
}

func TestRepairRelinksAStalePublicationButNeverCreatesOne(t *testing.T) {
	src, dest := credFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(dest))) // <root>/<user>/.claude/file
	t.Setenv("APE_CREDENTIAL_ROOT", root)
	t.Setenv("USER", filepath.Base(filepath.Dir(filepath.Dir(dest))))

	// Nothing published yet: repair must NOT invent a grant.
	repaired, err := RepairCredentialPublication("", src)
	require.NoError(t, err)
	assert.False(t, repaired)
	assert.NoFileExists(t, dest)

	// Published and live → nothing to do.
	_, err = publishCredential(src, dest, false)
	require.NoError(t, err)
	repaired, err = RepairCredentialPublication("", src)
	require.NoError(t, err)
	assert.False(t, repaired)

	// Replaced source → repaired back to live.
	tmp := src + ".new"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"post-login"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))
	repaired, err = RepairCredentialPublication("", src)
	require.NoError(t, err)
	assert.True(t, repaired)
	assert.True(t, credentialStatus(src, dest).Live)
	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Contains(t, string(content), "post-login", "the repaired link must carry the NEW credential")
}

func TestRepairLeavesACopyPublicationAlone(t *testing.T) {
	// --copy is a deliberate choice for isolation; silently re-linking would undo it.
	src, dest := credFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(dest)))
	t.Setenv("APE_CREDENTIAL_ROOT", root)
	t.Setenv("USER", filepath.Base(filepath.Dir(filepath.Dir(dest))))

	_, err := publishCredential(src, dest, true)
	require.NoError(t, err)
	tmp := src + ".new"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"post-login"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))

	repaired, err := RepairCredentialPublication("", src)
	require.NoError(t, err)
	assert.False(t, repaired)
	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.NotContains(t, string(content), "post-login", "a copy stays as the user left it")
}
