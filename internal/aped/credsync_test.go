package aped

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// credRig lays out a published credential plus N workspace copies and returns the
// syncer and the paths.
func credRig(t *testing.T, workspaces ...string) (sync *CredentialSync, published string, wsPaths map[string]string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	published = filepath.Join(root, "published", ".claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(published), 0o750))
	require.NoError(t, os.WriteFile(published, []byte(cred("v1", time.Now().Add(-2*time.Hour))), 0o660))

	wsPaths = map[string]string{}
	for _, name := range workspaces {
		p := WorkspaceCredentialPath(stateDir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(cred("v1", time.Now().Add(-2*time.Hour))), 0o600))
		wsPaths[name] = p
	}
	return &CredentialSync{Published: published, StateDir: stateDir, Stderr: &testWriter{}}, published, wsPaths
}

// cred is a logged-in Claude Code credential whose session was issued at issued:
// its token expires eight hours later, so a later issue is a later expiry.
func cred(token string, issued time.Time) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"r-%s","expiresAt":%d,"scopes":["user:inference"]}}`,
		token, token, issued.Add(8*time.Hour).UnixMilli())
}

// loggedOut is what a credential file holds after a logout or a lost session.
const loggedOut = `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0}}`

// tokenOf reads a credential's access token ("" for a logged-out or other file).
func tokenOf(t *testing.T, data []byte) string {
	t.Helper()
	var f oauthFile
	require.NoError(t, json.Unmarshal(data, &f), string(data))
	if f.ClaudeAiOauth == nil {
		return ""
	}
	return f.ClaudeAiOauth.AccessToken
}

// testWriter swallows the syncer's progress lines.
type testWriter struct{}

func (testWriter) Write(p []byte) (int, error) { return len(p), nil }

// touchWith writes content and stamps a modification time, so "newest wins" is tested
// deterministically instead of depending on filesystem timing.
func touchWith(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	require.NoError(t, os.Chtimes(path, mod, mod))
}

func TestCredSyncWorkspaceRefreshReachesHostAndOtherWorkspaces(t *testing.T) {
	// The case that matters most: a workspace refreshes its token, and the host plus
	// every other workspace must end up on the SAME new credential — otherwise the
	// shared OAuth session dies at the first rotation.
	sync, published, ws := credRig(t, "alpha", "beta")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("v1", base), base)
	touchWith(t, ws["beta"], cred("v1", base), base)
	touchWith(t, ws["alpha"], cred("v2-refreshed", base.Add(time.Minute)), base.Add(time.Minute))

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the host and the other workspace both get the new credential")

	for name, path := range map[string]string{"published": published, "beta": ws["beta"], "alpha": ws["alpha"]} {
		data, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Equal(t, "v2-refreshed", tokenOf(t, data), name)
	}
}

func TestCredSyncHostLoginReachesWorkspaces(t *testing.T) {
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, ws["alpha"], cred("old", base), base)
	touchWith(t, published, cred("post-login", base.Add(time.Minute)), base.Add(time.Minute))

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	data, err := os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.Equal(t, "post-login", tokenOf(t, data))
}

func TestCredSyncPreservesThePublishedInode(t *testing.T) {
	// Load-bearing: the published file is a HARD LINK to the operator's real credential,
	// so it must be written in place. A temp-file-plus-rename here would create a new
	// inode and silently sever the link, and a workspace's refresh would stop reaching
	// the host — the exact failure this whole component exists to prevent.
	sync, published, ws := credRig(t, "alpha")

	// A second name for the same inode stands in for the operator's real ~/.claude file.
	operatorFile := filepath.Join(t.TempDir(), "operator-credentials.json")
	require.NoError(t, os.Link(published, operatorFile))
	before, err := os.Stat(published)
	require.NoError(t, err)

	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("v1", base), base)
	touchWith(t, ws["alpha"], cred("v2", base.Add(time.Minute)), base.Add(time.Minute))
	_, err = sync.SyncOnce()
	require.NoError(t, err)

	after, err := os.Stat(published)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the published inode must survive the write")

	// And therefore the operator's own file now carries the workspace's refresh.
	data, err := os.ReadFile(operatorFile)
	require.NoError(t, err)
	assert.Equal(t, "v2", tokenOf(t, data),
		"writing in place is what makes a workspace refresh reach the host")
}

func TestCredSyncNeverCreatesAMissingCredential(t *testing.T) {
	// An absent credential is the absence of a grant — and a REVOKED one must stay
	// revoked. Creating it here would silently re-grant access.
	sync, published, ws := credRig(t, "alpha", "beta")
	require.NoError(t, os.Remove(ws["beta"]))
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("v1", base), base)
	touchWith(t, ws["alpha"], cred("v2", base.Add(time.Minute)), base.Add(time.Minute))

	_, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.NoFileExists(t, ws["beta"])

	// Same for the publication: revoked stays revoked.
	require.NoError(t, os.Remove(published))
	_, err = sync.SyncOnce()
	require.NoError(t, err)
	assert.NoFileExists(t, published)
}

func TestCredSyncRefusesToPropagateInvalidJSON(t *testing.T) {
	// A torn read (a file caught mid-write) must never be spread to the host and every
	// workspace — that is the one failure that would be hardest to recover from.
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("good", base), base)
	touchWith(t, ws["alpha"], `{"accessToken":"tru`, base.Add(time.Minute)) // newest, but torn

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the valid credential is restored over the torn one")
	data, err := os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.Equal(t, "good", tokenOf(t, data))

	// With nothing valid anywhere, it reports rather than guessing.
	touchWith(t, published, `not json`, base)
	touchWith(t, ws["alpha"], `also not json`, base.Add(time.Minute))
	_, err = sync.SyncOnce()
	require.Error(t, err)
}

func TestCredSyncIsANoOpWhenConverged(t *testing.T) {
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("same", base), base)
	touchWith(t, ws["alpha"], cred("same", base), base)

	before, err := os.Stat(ws["alpha"])
	require.NoError(t, err)
	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Zero(t, n, "identical content must not be rewritten")
	after, err := os.Stat(ws["alpha"])
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "a no-op must not touch mtimes (which would loop)")
}

func TestCredSyncWithNothingToConverge(t *testing.T) {
	// One peer alone (no workspaces yet, or no publication) is not an error.
	sync, _, _ := credRig(t)
	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestCredSyncWorkspaceWriteIsAtomicForReaders(t *testing.T) {
	// Workspace copies are replaced by rename, so a guest reading concurrently sees
	// either the whole old file or the whole new one — never a partial write. Asserted
	// via the inode changing, which is what rename does and in-place writing does not.
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, ws["alpha"], cred("old", base), base)
	touchWith(t, published, cred("new", base.Add(time.Minute)), base.Add(time.Minute))

	before, err := os.Stat(ws["alpha"])
	require.NoError(t, err)
	_, err = sync.SyncOnce()
	require.NoError(t, err)
	after, err := os.Stat(ws["alpha"])
	require.NoError(t, err)
	assert.False(t, os.SameFile(before, after), "a workspace copy is replaced, not overwritten")
	if runtime.GOOS != goosWindows {
		// Windows has no POSIX mode bits — Go reports 0666 there whatever was requested —
		// and the guarantee (only the owner reads a credential) is a POSIX one anyway.
		assert.Equal(t, os.FileMode(credFileMode), after.Mode().Perm())
	}
}

func TestCredSyncARepublishedCredentialBeatsANewerWorkspaceToken(t *testing.T) {
	// The scenario: the operator runs `claude /login` (a NEW session), then a workspace
	// that was still running refreshes a token from the OLD session. The workspace's file
	// has the newer mtime, but its token belongs to a session the login already replaced —
	// so "newest wins" would clobber the operator's fresh credential with a dead one.
	// A re-publish must therefore win on authority, not timestamp.
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("pre-login", base), base)
	touchWith(t, ws["alpha"], cred("pre-login", base), base)

	// Establish the baseline identity (what a first tick does).
	_, err := sync.SyncOnce()
	require.NoError(t, err)

	// The workspace refreshes from the old session — NEWER mtime.
	touchWith(t, ws["alpha"], cred("refreshed-from-old-session", base.Add(2*time.Minute)), base.Add(2*time.Minute))
	// And the operator re-publishes after a login: a NEW file at the same path, older mtime.
	require.NoError(t, os.Remove(published))
	touchWith(t, published, cred("post-login", base.Add(time.Minute)), base.Add(time.Minute))

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	data, err := os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.Equal(t, "post-login", tokenOf(t, data),
		"the re-published credential must win over a newer token from the session it replaced")
}

func TestCredSyncARepublishThatReusesTheInodeStillWins(t *testing.T) {
	// The scenario above, plus the one filesystem behaviour that defeats an identity-only
	// check: `credentials publish` unlinks the path and re-creates it, and the create can
	// be handed back the inode number the unlink just freed — os.SameFile then reports the
	// same file and the re-publish goes unnoticed. Not hypothetical: CI hit exactly this
	// while the developer's ext4 handed out a fresh inode and the bug stayed invisible.
	//
	// Rewriting the published file IN PLACE reproduces it deterministically on every
	// filesystem — same inode, content the syncer did not write — which is precisely what
	// inode reuse looks like from here.
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("pre-login", base), base)
	touchWith(t, ws["alpha"], cred("pre-login", base), base)

	_, err := sync.SyncOnce()
	require.NoError(t, err)
	beforeID, err := os.Stat(published)
	require.NoError(t, err)

	// The workspace refreshes from the old session — NEWER mtime.
	touchWith(t, ws["alpha"], cred("refreshed-from-old-session", base.Add(2*time.Minute)), base.Add(2*time.Minute))
	// The operator's post-login credential lands on the same inode, with an older mtime.
	touchWith(t, published, cred("post-login", base.Add(time.Minute)), base.Add(time.Minute))

	afterID, err := os.Stat(published)
	require.NoError(t, err)
	require.True(t, os.SameFile(beforeID, afterID),
		"the rig must preserve the inode, or this is not exercising the reuse case")

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	data, err := os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.Equal(t, "post-login", tokenOf(t, data),
		"a re-publish must win even when the new credential reuses the old inode number")
}

func TestCredSyncInPlaceUpdatesAreNotMistakenForARepublish(t *testing.T) {
	// This component updates the published file IN PLACE, which keeps its inode. If that
	// were read as a re-publish, every workspace refresh would make the publication
	// authoritative and immediately undo itself.
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("v1", base), base)
	touchWith(t, ws["alpha"], cred("v1", base), base)
	_, err := sync.SyncOnce()
	require.NoError(t, err)

	touchWith(t, ws["alpha"], cred("v2-from-workspace", base.Add(time.Minute)), base.Add(time.Minute))
	_, err = sync.SyncOnce() // writes the published file in place
	require.NoError(t, err)

	n, err := sync.SyncOnce() // the tick after: must be a no-op, not a reversal
	require.NoError(t, err)
	assert.Zero(t, n)
	data, err := os.ReadFile(published)
	require.NoError(t, err)
	assert.Equal(t, "v2-from-workspace", tokenOf(t, data),
		"the workspace's refresh must stick, not be undone by a false republish detection")
}

// D605.1: a logged-out file is never a source, whatever its modification time —
// the incident: a workspace's logged-out copy, written last, spread into the
// operator's ~/.claude through the published hard link and logged the host out.
func TestCredSyncNeverSpreadsALoggedOutCredential(t *testing.T) {
	sync, published, ws := credRig(t, "alpha", "beta")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("good", base), base)
	touchWith(t, ws["beta"], cred("good", base), base)
	touchWith(t, ws["alpha"], loggedOut, base.Add(time.Minute)) // newest mtime, logged out

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the logged-out workspace is written: it gets the session back")
	for name, path := range map[string]string{"published": published, "alpha": ws["alpha"], "beta": ws["beta"]} {
		data, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Equal(t, "good", tokenOf(t, data), name)
	}
}

// An earlier expiry never overwrites a later one, however recently it was written.
func TestCredSyncRanksByExpiryNotModificationTime(t *testing.T) {
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, cred("later-expiry", base.Add(time.Minute)), base)
	touchWith(t, ws["alpha"], cred("earlier-expiry", base), base.Add(5*time.Minute))

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	for name, path := range map[string]string{"published": published, "alpha": ws["alpha"]} {
		data, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Equal(t, "later-expiry", tokenOf(t, data), name)
	}
}

// A publication with no logged-in session — the operator logged out — is left
// alone and nothing is taken from it: no workspace is logged out by it, and no
// workspace token is written back into the operator's file.
func TestCredSyncLeavesALoggedOutPublicationAlone(t *testing.T) {
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, ws["alpha"], cred("ws", base), base)
	touchWith(t, published, cred("ws", base), base)
	_, err := sync.SyncOnce() // baseline
	require.NoError(t, err)

	// The operator logs out: a new file at the published path.
	require.NoError(t, os.Remove(published))
	touchWith(t, published, loggedOut, base.Add(time.Minute))
	for range 2 {
		n, err := sync.SyncOnce()
		require.NoError(t, err)
		assert.Zero(t, n)
	}
	data, err := os.ReadFile(published)
	require.NoError(t, err)
	assert.JSONEq(t, loggedOut, string(data), "the operator's logout stands")
	data, err = os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.Equal(t, "ws", tokenOf(t, data), "the workspace is not logged out by it")

	// A login afterwards is a re-publish, and wins as before.
	require.NoError(t, os.Remove(published))
	touchWith(t, published, cred("post-login", base.Add(-time.Minute)), base.Add(2*time.Minute))
	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	data, err = os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.Equal(t, "post-login", tokenOf(t, data))
}

// With no logged-in session anywhere, nothing is written; a publication of a
// shape this does not know counts as holding none, and is left alone.
func TestCredSyncWithNoLoggedInPeerWritesNothing(t *testing.T) {
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, `{"pub`, base) // torn
	touchWith(t, ws["alpha"], loggedOut, base.Add(time.Minute))
	n, err := sync.SyncOnce()
	require.Error(t, err)
	assert.Zero(t, n)

	touchWith(t, published, `{"accessToken":"an-unknown-shape"}`, base)
	touchWith(t, ws["alpha"], cred("ws", base), base.Add(time.Minute))
	n, err = sync.SyncOnce()
	require.NoError(t, err)
	assert.Zero(t, n)
	data, err := os.ReadFile(published)
	require.NoError(t, err)
	assert.JSONEq(t, `{"accessToken":"an-unknown-shape"}`, string(data))
}

// Equal expiries: the later write wins, then the publication.
func TestCredSyncBreaksAnExpiryTieByModificationTime(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	same := base.Add(time.Minute)
	peers := []credPeer{
		{path: "ws", mod: base.Add(2 * time.Minute), data: []byte(cred("ws", same)), expires: sessionExpiry([]byte(cred("ws", same)))},
		{path: "pub", published: true, mod: base, data: []byte(cred("pub", same)), expires: sessionExpiry([]byte(cred("pub", same)))},
	}
	assert.Equal(t, "ws", newestPeer(peers).path)
	peers[0].mod = base
	assert.Equal(t, "pub", newestPeer(peers).path)
}
