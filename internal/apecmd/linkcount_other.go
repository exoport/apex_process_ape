//go:build !unix

package apecmd

import "os"

// linkCount is unavailable off unix; the caller falls back to treating the file as a
// copy, which is the safe reading (it will not claim a link is live).
func linkCount(os.FileInfo) (uint64, bool) { return 0, false }
