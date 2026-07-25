//go:build !unix

package apecmd

import "os"

// priorOwnership is unavailable off unix (no stat gid), so nothing is recorded and
// `revoke` simply skips the restore. Credential publishing targets the aped HOST, which
// is Linux; this exists so the CLI compiles everywhere.
func priorOwnership(string) *credentialOwnership { return nil }

// linkCount is unavailable off unix; the caller falls back to treating the file as a
// copy, which is the safe reading (it will not claim a link is live).
func linkCount(os.FileInfo) (uint64, bool) { return 0, false }
