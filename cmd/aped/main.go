// Command aped is the rootful Kata-QEMU VM-management daemon (PLAN-18 Phase 2).
// It is a separate binary from `ape` so `ape` stays dependency-light (LOCKED 8);
// all command wiring lives in internal/apedcmd.
package main

import "github.com/exoport/apex_process_ape/internal/apedcmd"

func main() { apedcmd.Execute() }
