//go:build !linux

package aped

// The AF_UNIX SEQPACKET + SO_PEERCRED boundary is Linux-only. These stubs keep
// the package compiling (and the Windows cross-compile green); aped's daemon
// commands refuse to run on non-Linux at the command layer.

func listenPriv(string) (privListener, error) { return nil, ErrPrivUnsupported }

func dialPriv(string) (privConn, error) { return nil, ErrPrivUnsupported }

func socketActivatedListener() (privListener, bool, error) { return nil, false, ErrPrivUnsupported }
