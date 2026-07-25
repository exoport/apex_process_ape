package apecmd

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// credFixture writes a fake host credential and returns (source, publishDest).
//
// It also points the ACL grant at the CURRENT user, because granting `aped` requires
// that account to exist on whatever machine runs the tests. What is under test is the
// grant mechanism, not the account name.
func credFixture(t *testing.T) (ctx context.Context, source, dest string) {
	t.Helper()
	if _, err := exec.LookPath("setfacl"); err != nil {
		t.Skip("setfacl not installed: the credential grant is ACL-only by design")
	}
	me, err := user.Current()
	require.NoError(t, err)
	restore := aclUser
	aclUser = func() string { return me.Username }
	t.Cleanup(func() { aclUser = restore })
	home := t.TempDir()
	source = filepath.Join(home, ".claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o700))
	require.NoError(t, os.WriteFile(source, []byte(`{"access_token":"live-1"}`), 0o600))
	return t.Context(), source, filepath.Join(t.TempDir(), "diegos", credentialRelPath)
}

func TestPublishCredentialHardLinkSharesOneInode(t *testing.T) {
	ctx, src, dest := credFixture(t)

	msg, err := publishCredential(ctx, src, dest, false)
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
	// The grant is exactly one user. Note what `ls -l` will show: adding a user ACL
	// creates a MASK, and the mask lives in the mode's group bits, so the file reads as
	// 0660 (`-rw-rw----+`) even though the owning GROUP has no access at all. Asserting
	// the mode would therefore assert the confusing artefact; assert the real grant.
	assert.Equal(t, os.FileMode(0o660), si.Mode().Perm(), "group bits carry the ACL mask, not group access")
	assert.True(t, hasDaemonAccess(ctx, dest), "the publication must carry the ACL entry the daemon needs")
	assert.True(t, groupHasNoAccess(ctx, src), "the owning group gains nothing — this is what a group grant would not give us")
	// The parent directory must be TRAVERSABLE by the daemon's service user, which is
	// not in the publishing user's primary group — hence traverse-only for others,
	// while the credential itself stays unreadable.
	info, err := os.Stat(filepath.Dir(dest))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(credentialDirMode), info.Mode().Perm())
	assert.Zero(t, info.Mode().Perm()&0o004, "others must not be able to LIST the directory")
}

func TestPublishCredentialCopyIsIndependent(t *testing.T) {
	ctx, src, dest := credFixture(t)

	msg, err := publishCredential(ctx, src, dest, true)
	require.NoError(t, err)
	assert.Contains(t, msg, "COPY")

	si, _ := os.Stat(src)
	di, err := os.Stat(dest)
	require.NoError(t, err)
	assert.False(t, os.SameFile(si, di))
	assert.True(t, hasDaemonAccess(ctx, dest), "a copy needs the grant too, or the daemon cannot read it")
	assert.True(t, groupHasNoAccess(ctx, dest), "still only one user, never the group")

	// Divergence is the documented consequence, so pin it: writing one leaves the
	// other alone.
	require.NoError(t, os.WriteFile(dest, []byte(`{"access_token":"workspace-refreshed"}`), 0o600))
	back, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Contains(t, string(back), "live-1")
}

func TestPublishCredentialIsIdempotentAndRepairsAStaleLink(t *testing.T) {
	ctx, src, dest := credFixture(t)
	_, err := publishCredential(ctx, src, dest, false)
	require.NoError(t, err)
	_, err = publishCredential(ctx, src, dest, false)
	require.NoError(t, err, "re-publishing must not fail on an existing target")

	// Simulate the host replacing its credential file (write temp + rename), which
	// decouples the link — the failure mode re-publishing exists to repair.
	tmp := src + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"rotated"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))

	st := credentialStatus(ctx, src, dest)
	assert.True(t, st.Present)
	assert.False(t, st.Live, "a decoupled link must be reported as not live")

	_, err = publishCredential(ctx, src, dest, false)
	require.NoError(t, err)
	st = credentialStatus(ctx, src, dest)
	assert.True(t, st.Live, "re-publishing repairs it")
	assert.Equal(t, credentialModeLink, st.Mode)
}

func TestCredentialStatusWhenNothingPublished(t *testing.T) {
	ctx, src, dest := credFixture(t)
	st := credentialStatus(ctx, src, dest)
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
	ctx, src, dest := credFixture(t)
	_, err := publishCredential(ctx, src, dest, false)
	require.NoError(t, err)
	require.True(t, credentialStatus(ctx, src, dest).Live)

	// Replace the source the way a login does: write a new file, rename over it.
	tmp := src + ".new"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"post-login"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))

	st := credentialStatus(ctx, src, dest)
	require.True(t, st.Present)
	assert.False(t, st.Live)
	assert.Equal(t, credentialModeLink, st.Mode, "a decoupled link must not be reported as a copy")
	assert.Contains(t, st.summary(), "STALE")
	assert.Contains(t, st.summary(), "old credential")
}

func TestRepairRelinksAStalePublicationButNeverCreatesOne(t *testing.T) {
	ctx, src, dest := credFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(dest))) // <root>/<user>/.claude/file
	t.Setenv("APE_CREDENTIAL_ROOT", root)
	t.Setenv("USER", filepath.Base(filepath.Dir(filepath.Dir(dest))))

	// Nothing published yet: repair must NOT invent a grant.
	repaired, err := RepairCredentialPublication(ctx, "", src)
	require.NoError(t, err)
	assert.False(t, repaired)
	assert.NoFileExists(t, dest)

	// Published and live → nothing to do.
	_, err = publishCredential(ctx, src, dest, false)
	require.NoError(t, err)
	repaired, err = RepairCredentialPublication(ctx, "", src)
	require.NoError(t, err)
	assert.False(t, repaired)

	// Replaced source → repaired back to live.
	tmp := src + ".new"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"post-login"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))
	repaired, err = RepairCredentialPublication(ctx, "", src)
	require.NoError(t, err)
	assert.True(t, repaired)
	assert.True(t, credentialStatus(ctx, src, dest).Live)
	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Contains(t, string(content), "post-login", "the repaired link must carry the NEW credential")
}

func TestRepairLeavesACopyPublicationAlone(t *testing.T) {
	// --copy is a deliberate choice for isolation; silently re-linking would undo it.
	ctx, src, dest := credFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(dest)))
	t.Setenv("APE_CREDENTIAL_ROOT", root)
	t.Setenv("USER", filepath.Base(filepath.Dir(filepath.Dir(dest))))

	_, err := publishCredential(ctx, src, dest, true)
	require.NoError(t, err)
	tmp := src + ".new"
	require.NoError(t, os.WriteFile(tmp, []byte(`{"access_token":"post-login"}`), 0o600))
	require.NoError(t, os.Rename(tmp, src))

	repaired, err := RepairCredentialPublication(ctx, "", src)
	require.NoError(t, err)
	assert.False(t, repaired)
	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.NotContains(t, string(content), "post-login", "a copy stays as the user left it")
}

func TestRevokeRemovesTheACLGrant(t *testing.T) {
	// Revoke must take the grant off the shared inode, not just unlink the extra name —
	// otherwise the operator's own credential stays readable by the daemon after they
	// explicitly revoked it.
	ctx, src, dest := credFixture(t)
	_, err := publishCredential(ctx, src, dest, false)
	require.NoError(t, err)
	require.True(t, hasDaemonAccess(ctx, src), "the grant lands on the shared inode, so it covers the source too")

	assert.True(t, revokeDaemonAccess(ctx, dest))
	assert.False(t, hasDaemonAccess(ctx, src), "revoke removes it from the operator's file as well")

	info, err := os.Stat(src)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"removing the ACL removes the mask too, so the mode returns to plain 0600")
}

func TestRepairReassertsTheGrantWhenItIsMissing(t *testing.T) {
	// A login creates a fresh inode with no ACL. Re-publishing must restore the grant, or
	// workspaces silently stop being able to read the credential.
	ctx, src, dest := credFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(dest)))
	t.Setenv("APE_CREDENTIAL_ROOT", root)
	t.Setenv("USER", filepath.Base(filepath.Dir(filepath.Dir(dest))))
	_, err := publishCredential(ctx, src, dest, false)
	require.NoError(t, err)

	require.True(t, revokeDaemonAccess(ctx, dest)) // simulate the grant being lost
	require.False(t, hasDaemonAccess(ctx, dest))

	_, err = RepairCredentialPublication(ctx, "", src)
	require.NoError(t, err)
	assert.True(t, hasDaemonAccess(ctx, dest), "repair re-asserts the grant even when the link is still live")
}

func TestStatusReportsAMissingGrant(t *testing.T) {
	// `ls -l` shows only a bare `+`, so status is where an operator learns the grant is
	// absent — before a workspace fails to start for a reason that looks unrelated.
	ctx, src, dest := credFixture(t)
	_, err := publishCredential(ctx, src, dest, false)
	require.NoError(t, err)
	require.True(t, credentialStatus(ctx, src, dest).Granted)

	require.True(t, revokeDaemonAccess(ctx, dest))
	st := credentialStatus(ctx, src, dest)
	assert.False(t, st.Granted)
	assert.Contains(t, st.summary(), "NO access")
	assert.Contains(t, st.summary(), "credentials publish")
}

func TestApeBinaryPathPrefersTheInvokedPathOverTheResolvedTarget(t *testing.T) {
	// A typical install is a STABLE symlink onto a version-stamped binary
	// (~/go/bin/ape → ape-v0.0.48, which is what `go install` leaves). A generated unit
	// must point at the symlink: baking in the resolved target pins it to today's version
	// and breaks the service at the next update. /proc/self/exe resolves, so os.Args[0]
	// via LookPath is what preserves it.
	dir := t.TempDir()
	versioned := filepath.Join(dir, "ape-v9.9.9")
	require.NoError(t, os.WriteFile(versioned, []byte("#!/bin/sh\n"), 0o755))
	link := filepath.Join(dir, "ape")
	require.NoError(t, os.Symlink(versioned, link))

	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	os.Args = []string{link}

	got, err := apeBinaryPath()
	require.NoError(t, err)
	assert.Equal(t, link, got, "the unit must reference the stable symlink, not the versioned target")
}

func TestWatchUnitFileRefusesATemporaryBinary(t *testing.T) {
	// A unit pointing into a temp dir (e.g. `go run`) breaks the moment it is cleaned up,
	// which would show up much later as a service that mysteriously stopped working.
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	tmpBin := filepath.Join(os.TempDir(), "ape-transient")
	require.NoError(t, os.WriteFile(tmpBin, []byte("#!/bin/sh\n"), 0o755))
	t.Cleanup(func() { _ = os.Remove(tmpBin) })
	os.Args = []string{tmpBin}

	_, err := watchUnitFile(2 * time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporary path")
}

func TestWatchUnitFileCarriesANonDefaultInterval(t *testing.T) {
	orig := os.Args
	t.Cleanup(func() { os.Args = orig })
	// The fixture lives under the real temp dir, which the transient-binary guard would
	// (correctly) reject, so point the guard elsewhere for this test.
	restoreTmp := unitTempDir
	unitTempDir = func() string { return "/not-a-real-temp-prefix" }
	t.Cleanup(func() { unitTempDir = restoreTmp })
	dir := t.TempDir()
	bin := filepath.Join(dir, "ape")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	os.Args = []string{bin}

	unit, err := watchUnitFile(10 * time.Second)
	require.NoError(t, err)
	assert.Contains(t, unit, "ExecStart="+bin+" sandbox credentials watch --interval 10s")

	// The default is left implicit, so the unit does not pin a value that may change.
	unit, err = watchUnitFile(2 * time.Second)
	require.NoError(t, err)
	assert.Contains(t, unit, "ExecStart="+bin+" sandbox credentials watch\n")
}
