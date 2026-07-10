//go:build !linux

package sandbox

import "context"

// The Kata runner is Linux-only (Kata/KVM is Linux). These stubs let the
// module compile — and its pure cross-platform tests run — on the Windows
// CI leg; the CLI turns ErrUnsupported into a clear message. The pure
// command-construction helpers (RunArgs, ExecArgs, …) and the registry in
// kata.go still work everywhere and are unit-tested on every platform.

func (r *Runner) Provision(_ context.Context, _ WorkspaceSpec) error { return ErrUnsupported }

func (r *Runner) Exec(_ context.Context, _ string, _ bool, _ []string) error { return ErrUnsupported }

func (r *Runner) Attach(_ context.Context, _, _ string) error { return ErrUnsupported }

func (r *Runner) Freeze(_ context.Context, _ string) error { return ErrUnsupported }

func (r *Runner) Unfreeze(_ context.Context, _ string) error { return ErrUnsupported }

func (r *Runner) Start(_ context.Context, _ string) error { return ErrUnsupported }

func (r *Runner) Stop(_ context.Context, _ string) error { return ErrUnsupported }

func (r *Runner) Down(_ context.Context, _ string) error { return ErrUnsupported }
