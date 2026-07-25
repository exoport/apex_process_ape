package aped

import (
	"os"
	"path/filepath"
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
	require.NoError(t, os.WriteFile(published, []byte(`{"accessToken":"v1"}`), 0o660))

	wsPaths = map[string]string{}
	for _, name := range workspaces {
		p := WorkspaceCredentialPath(stateDir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(`{"accessToken":"v1"}`), 0o600))
		wsPaths[name] = p
	}
	return &CredentialSync{Published: published, StateDir: stateDir, Stderr: &testWriter{}}, published, wsPaths
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
	touchWith(t, published, `{"accessToken":"v1"}`, base)
	touchWith(t, ws["beta"], `{"accessToken":"v1"}`, base)
	touchWith(t, ws["alpha"], `{"accessToken":"v2-refreshed"}`, base.Add(time.Minute))

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the host and the other workspace both get the new credential")

	for name, path := range map[string]string{"published": published, "beta": ws["beta"], "alpha": ws["alpha"]} {
		data, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.JSONEq(t, `{"accessToken":"v2-refreshed"}`, string(data), name)
	}
}

func TestCredSyncHostLoginReachesWorkspaces(t *testing.T) {
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, ws["alpha"], `{"accessToken":"old"}`, base)
	touchWith(t, published, `{"accessToken":"post-login"}`, base.Add(time.Minute))

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	data, err := os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"accessToken":"post-login"}`, string(data))
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
	touchWith(t, published, `{"accessToken":"v1"}`, base)
	touchWith(t, ws["alpha"], `{"accessToken":"v2"}`, base.Add(time.Minute))
	_, err = sync.SyncOnce()
	require.NoError(t, err)

	after, err := os.Stat(published)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the published inode must survive the write")

	// And therefore the operator's own file now carries the workspace's refresh.
	data, err := os.ReadFile(operatorFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"accessToken":"v2"}`, string(data),
		"writing in place is what makes a workspace refresh reach the host")
}

func TestCredSyncNeverCreatesAMissingCredential(t *testing.T) {
	// An absent credential is the absence of a grant — and a REVOKED one must stay
	// revoked. Creating it here would silently re-grant access.
	sync, published, ws := credRig(t, "alpha", "beta")
	require.NoError(t, os.Remove(ws["beta"]))
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, `{"accessToken":"v1"}`, base)
	touchWith(t, ws["alpha"], `{"accessToken":"v2"}`, base.Add(time.Minute))

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
	touchWith(t, published, `{"accessToken":"good"}`, base)
	touchWith(t, ws["alpha"], `{"accessToken":"tru`, base.Add(time.Minute)) // newest, but torn

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the valid credential is restored over the torn one")
	data, err := os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"accessToken":"good"}`, string(data))

	// With nothing valid anywhere, it reports rather than guessing.
	touchWith(t, published, `not json`, base)
	touchWith(t, ws["alpha"], `also not json`, base.Add(time.Minute))
	_, err = sync.SyncOnce()
	require.Error(t, err)
}

func TestCredSyncIsANoOpWhenConverged(t *testing.T) {
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, `{"accessToken":"same"}`, base)
	touchWith(t, ws["alpha"], `{"accessToken":"same"}`, base)

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
	touchWith(t, ws["alpha"], `{"accessToken":"old"}`, base)
	touchWith(t, published, `{"accessToken":"new"}`, base.Add(time.Minute))

	before, err := os.Stat(ws["alpha"])
	require.NoError(t, err)
	_, err = sync.SyncOnce()
	require.NoError(t, err)
	after, err := os.Stat(ws["alpha"])
	require.NoError(t, err)
	assert.False(t, os.SameFile(before, after), "a workspace copy is replaced, not overwritten")
	assert.Equal(t, os.FileMode(credFileMode), after.Mode().Perm())
}

func TestCredSyncARepublishedCredentialBeatsANewerWorkspaceToken(t *testing.T) {
	// The scenario: the operator runs `claude /login` (a NEW session), then a workspace
	// that was still running refreshes a token from the OLD session. The workspace's file
	// has the newer mtime, but its token belongs to a session the login already replaced —
	// so "newest wins" would clobber the operator's fresh credential with a dead one.
	// A re-publish must therefore win on authority, not timestamp.
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, `{"accessToken":"pre-login"}`, base)
	touchWith(t, ws["alpha"], `{"accessToken":"pre-login"}`, base)

	// Establish the baseline identity (what a first tick does).
	_, err := sync.SyncOnce()
	require.NoError(t, err)

	// The workspace refreshes from the old session — NEWER mtime.
	touchWith(t, ws["alpha"], `{"accessToken":"refreshed-from-old-session"}`, base.Add(2*time.Minute))
	// And the operator re-publishes after a login: a NEW file at the same path, older mtime.
	require.NoError(t, os.Remove(published))
	touchWith(t, published, `{"accessToken":"post-login"}`, base.Add(time.Minute))

	n, err := sync.SyncOnce()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	data, err := os.ReadFile(ws["alpha"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"accessToken":"post-login"}`, string(data),
		"the re-published credential must win over a newer token from the session it replaced")
}

func TestCredSyncInPlaceUpdatesAreNotMistakenForARepublish(t *testing.T) {
	// This component updates the published file IN PLACE, which keeps its inode. If that
	// were read as a re-publish, every workspace refresh would make the publication
	// authoritative and immediately undo itself.
	sync, published, ws := credRig(t, "alpha")
	base := time.Now().Add(-time.Hour)
	touchWith(t, published, `{"accessToken":"v1"}`, base)
	touchWith(t, ws["alpha"], `{"accessToken":"v1"}`, base)
	_, err := sync.SyncOnce()
	require.NoError(t, err)

	touchWith(t, ws["alpha"], `{"accessToken":"v2-from-workspace"}`, base.Add(time.Minute))
	_, err = sync.SyncOnce() // writes the published file in place
	require.NoError(t, err)

	n, err := sync.SyncOnce() // the tick after: must be a no-op, not a reversal
	require.NoError(t, err)
	assert.Zero(t, n)
	data, err := os.ReadFile(published)
	require.NoError(t, err)
	assert.JSONEq(t, `{"accessToken":"v2-from-workspace"}`, string(data),
		"the workspace's refresh must stick, not be undone by a false republish detection")
}
