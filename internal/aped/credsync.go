package aped

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
)

/*
One shared Claude session across the host and every workspace.

The hard part is not ACCESS to the credential, it is IDENTITY OF SESSION. OAuth
refresh tokens ROTATE: the moment any party refreshes, the token every other party
holds is dead. So "each workspace gets a copy" is not a lesser version of sharing —
it is a session that breaks a few hours after it is created.

Sharing one inode would solve rotation, and that is what the published hard link
does host-side. But it cannot reach into a workspace, because `claude` REPLACES its
credential file by rename (measured: a host login changes the inode) and a
single-file bind mount cannot be renamed over — the guest gets EBUSY. A workspace
whose credential is bind-mounted can therefore never write the token it just
refreshed.

So each workspace gets a real, writable copy (rename works in an ordinary directory)
and this component keeps every copy CONVERGED with the published credential:

	workspace refreshes or logs in
	  → its staging copy changes
	  → written IN PLACE to the published file, which is a hard link to the
	    operator's real ~/.claude/.credentials.json, so the host sees it too
	  → and out to every other workspace

	host logs in (this REPLACES the file, decoupling the link)
	  → any `ape sandbox` command re-publishes, so the published name points at the
	    new inode again
	  → propagated out to the workspaces from here

The in-place write to the published file is deliberate and load-bearing: a
temp-file-plus-rename there would create a NEW inode and silently break the link to
the operator's real file, which is the one thing making host-side sharing work at
all. Workspace copies are written with rename instead, because they are hard-linked
to nothing and rename is what makes a reader see the change atomically.

Bounds it respects: it never CREATES a credential where none exists (an absent file
means no grant, and a revoked one must stay revoked), and it never propagates
content that is not parseable JSON (a torn read must not be spread to every
workspace and the host).
*/

// DefaultCredSyncInterval is how often the sync loop runs. Token refreshes happen
// hours apart, so this only needs to be short enough that a login in one place shows
// up in another before someone notices — not a hot loop.
const DefaultCredSyncInterval = 3 * time.Second

// credFileMode is the mode a workspace's credential copy is written with. The
// published file keeps whatever mode the operator granted (see `ape sandbox
// credentials publish`), since it is their file.
const credFileMode = 0o600

// CredentialSync converges the published host credential and every workspace's copy.
type CredentialSync struct {
	// Published is the node's view of the operator's credential — normally a hard
	// link to their real ~/.claude/.credentials.json.
	Published string
	// StateDir is the aped state dir; workspace copies live under <StateDir>/homes/*.
	StateDir string
	// Interval is the poll period; 0 → DefaultCredSyncInterval.
	Interval time.Duration
	// Stderr receives one line per propagation; nil → os.Stderr.
	Stderr io.Writer
}

// Run syncs until ctx is cancelled. It is resilient by construction: every error is
// reported and the loop continues, because a transient failure (a file being
// replaced as we read it) must not stop credential sharing for the rest of the day.
func (c *CredentialSync) Run(ctx context.Context) {
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultCredSyncInterval
	}
	fmt.Fprintf(c.stderr(), "  credential sync: %s ↔ workspaces every %s\n", c.Published, interval)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := c.SyncOnce(); err != nil {
				fmt.Fprintf(c.stderr(), "! credential sync: %v\n", err)
			} else if n > 0 {
				fmt.Fprintf(c.stderr(), "  credential sync: propagated to %d peer(s)\n", n)
			}
		}
	}
}

func (c *CredentialSync) stderr() io.Writer {
	if c.Stderr != nil {
		return c.Stderr
	}
	return os.Stderr
}

// credPeer is one participant in the shared session.
type credPeer struct {
	path string
	// published marks the operator's file, which must be written in place to keep its
	// hard link intact, and which wins a modification-time tie.
	published bool
	mod       time.Time
	data      []byte
}

// SyncOnce converges every peer on the newest valid credential and returns how many
// peers it wrote. Exported so the behaviour is testable without a running daemon.
func (c *CredentialSync) SyncOnce() (int, error) {
	peers, err := c.peers()
	if err != nil {
		return 0, err
	}
	if len(peers) < 2 {
		return 0, nil // nothing to converge (no publication, or no workspace with one)
	}

	newest := newestPeer(peers)
	if newest == nil {
		return 0, errors.New("no peer holds parseable credential JSON — refusing to propagate")
	}

	written := 0
	for i := range peers {
		p := &peers[i]
		if p.path == newest.path || bytesEqual(p.data, newest.data) {
			continue
		}
		if err := writeCredential(p, newest.data); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// peers gathers the published credential and every workspace copy that EXISTS.
// A missing file is skipped, never created: absence is the absence of a grant.
func (c *CredentialSync) peers() ([]credPeer, error) {
	var out []credPeer
	if p, ok := readPeer(c.Published, true); ok {
		out = append(out, p)
	}
	matches, err := filepath.Glob(filepath.Join(c.StateDir, "homes", "*", ".claude", ".credentials.json"))
	if err != nil {
		return nil, fmt.Errorf("aped: scan workspace credentials: %w", err)
	}
	sort.Strings(matches) // deterministic order, so logs and tests are stable
	for _, m := range matches {
		if p, ok := readPeer(m, false); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// readPeer loads one peer, skipping anything unreadable or empty. A file being
// rewritten right now is simply skipped this tick and picked up on the next.
func readPeer(path string, published bool) (credPeer, bool) {
	if path == "" {
		return credPeer{}, false
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return credPeer{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return credPeer{}, false
	}
	return credPeer{path: path, published: published, mod: st.ModTime(), data: data}, true
}

// newestPeer returns the most recently modified peer whose content is valid JSON, or
// nil when none is. Validity is the gate that stops a torn read from being spread to
// every workspace and the host — the failure that would be hardest to recover from.
// The published file wins an exact tie: it is the operator's own file.
func newestPeer(peers []credPeer) *credPeer {
	var best *credPeer
	for i := range peers {
		p := &peers[i]
		if !json.Valid(p.data) {
			continue
		}
		switch {
		case best == nil:
			best = p
		case p.mod.After(best.mod):
			best = p
		case p.mod.Equal(best.mod) && p.published:
			best = p
		}
	}
	return best
}

// writeCredential updates one peer to the given content.
//
// The published file is written IN PLACE (truncate + write, same inode) because it is
// a hard link to the operator's real credential: replacing it via rename would create
// a new inode and quietly sever exactly the link that lets a workspace's refresh reach
// the host. Everything else is written temp + rename, which is atomic for a reader —
// and the guest sees it because its home is a bind-mounted DIRECTORY, where rename
// works.
func writeCredential(p *credPeer, data []byte) error {
	if p.published {
		return writeInPlace(p.path, data)
	}
	dir := filepath.Dir(p.path)
	tmp, err := os.CreateTemp(dir, ".credentials.*.tmp")
	if err != nil {
		return fmt.Errorf("aped: stage credential for %s: %w", p.path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op once the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("aped: write credential temp: %w", err)
	}
	if err := tmp.Chmod(credFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("aped: chmod credential temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("aped: close credential temp: %w", err)
	}
	if err := os.Rename(name, p.path); err != nil {
		return fmt.Errorf("aped: replace credential %s: %w", p.path, err)
	}
	return nil
}

// writeInPlace overwrites a file's contents without changing its inode.
//
// The write is one syscall and the truncate follows it, so a concurrent reader sees
// either the old bytes or the new ones for same-length content — which credential
// files, being the same shape each time, almost always are. A shorter new credential
// leaves a sub-millisecond window where trailing old bytes remain; the JSON-validity
// gate in newestPeer means such a read is never propagated onward.
func writeInPlace(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY, credFileMode)
	if err != nil {
		return fmt.Errorf("aped: open published credential %s: %w "+
			"(two causes: the unit needs ReadWritePaths for this directory — ProtectSystem=strict makes it read-only otherwise — or the ACL grant is missing; see `ape sandbox credentials status`)", path, err)
	}
	defer func() { _ = f.Close() }()
	n, err := f.Write(data)
	if err != nil {
		return fmt.Errorf("aped: write published credential: %w", err)
	}
	if err := f.Truncate(int64(n)); err != nil {
		return fmt.Errorf("aped: truncate published credential: %w", err)
	}
	return nil
}

// bytesEqual compares two credential payloads.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// WorkspaceCredentialPath returns where a workspace's credential copy lives on the
// host — inside the staging home the front owns, which is why the front can converge
// it without any extra privilege.
func WorkspaceCredentialPath(stateDir, workspace string) string {
	return filepath.Join(sandbox.StagingDirFor(stateDir, workspace), ".claude", ".credentials.json")
}
