//go:build unix

package apecmd

import (
	"os"
	"syscall"
)

// priorOwnership records a file's group and mode before publishing grants the daemon
// access, so `revoke` can put them back rather than leaving the operator's credential
// permanently group-writable. Unix-only: it reads the stat gid.
func priorOwnership(path string) *credentialOwnership {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return &credentialOwnership{GID: int(sys.Gid), Mode: uint32(st.Mode().Perm())}
}

// linkCount returns a file's hard-link count. It is used only to distinguish a
// DECOUPLED hard link (count > 1, but no longer sharing the source's inode — the
// shape left behind when the host replaced its credential file) from a deliberate
// copy, so a wrong answer degrades the diagnostic rather than the behaviour.
func linkCount(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	// The conversion looks redundant on linux/amd64 (Nlink is already uint64) but is
	// REQUIRED on darwin, where it is uint16 — the goreleaser darwin build catches its
	// removal, which is why the linter is silenced rather than obeyed here.
	return uint64(st.Nlink), true //nolint:unconvert // uint16 on darwin
}
