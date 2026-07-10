// Package aped is the rootful Kata-QEMU VM-management daemon (PLAN-18 Phase 2).
//
// aped runs as two processes joined by a typed AF_UNIX command boundary
// (PLAN-18 D1):
//
//   - `aped run`   — the network-less root executor. It serves the AF_UNIX
//     SEQPACKET socket /run/aped/priv.sock (SO_PEERCRED-gated), re-validates
//     every fully-resolved command against policy (D9), drives the workspace
//     Backend (the PLAN-16/18 shellDriver for the non-device tier), and writes
//     an append-only audit record per privileged op. It holds no network
//     address family beyond AF_UNIX.
//   - `aped front` — the de-privileged NATS surface. It embeds nats-server in
//     operator/JWT mode with two accounts (HOST_OPS, TELEMETRY), runs the
//     `vmm` NATS-micro service on ape.vmm.<node>.> (D2), pre-checks policy,
//     resolves the thin wire CreateRequest into a fully-resolved spec, mints
//     per-VM telemetry credentials, and forwards typed commands to the
//     executor over the priv socket.
//
// The guest→host-escape barrier (the primary threat, LOCKED 7) is enforced by
// NATS account isolation: a per-VM credential lives in the TELEMETRY account
// and cannot even name a management subject in HOST_OPS, let alone reach the
// network-less executor behind SO_PEERCRED.
//
// This package builds on the transport-agnostic contract in internal/workspace
// (the Backend interface + the ape.vmm wire types + the req.Error code set) and
// reuses the PLAN-16 pure layers in internal/sandbox (compose/proxy/profile)
// via the server-side SpecResolver. The pure cores here — authz grants, cred
// minting, policy, the command codec — are cross-platform and unit-tested; the
// AF_UNIX/SO_PEERCRED transport is Linux-only (priv_linux.go) with a portable
// stub (priv_other.go) so the Windows cross-compile stays green.
package aped
