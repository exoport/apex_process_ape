//go:build !linux

package sandbox

import "context"

// The egress-proxy supervisor re-execs a detached daemon via setsid — a
// Linux-only mechanism, matching the Kata runner. On other platforms Start
// fails with ErrUnsupported and Stop is a no-op, so the cross-platform CLI
// wiring compiles on the Windows CI leg. (The whole sandbox feature is
// Linux-only at run time.)

func (s *ProxySupervisor) Start(_ context.Context, _ ProxyStartOptions) (ProxyState, error) {
	return ProxyState{}, ErrUnsupported
}

func (s *ProxySupervisor) Stop(_ ProxyState) error { return nil }
