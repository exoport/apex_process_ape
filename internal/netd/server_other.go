//go:build !linux

package netd

import (
	"context"
	"errors"
)

// ErrUnsupported is returned by Serve off Linux: named network namespaces, veth
// pairs, bridge port isolation and nftables are Linux-only, and so is every host
// aped runs on. The stub exists so the command layer compiles on the Windows CI
// leg (make xcompile-windows).
var ErrUnsupported = errors.New("netd: the sandbox network helper requires Linux")

// Serve is unavailable off Linux.
func Serve(context.Context, ServerConfig) error { return ErrUnsupported }
