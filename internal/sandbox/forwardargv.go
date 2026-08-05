package sandbox

import (
	"fmt"
	"path"
	"strconv"
)

// ForwardArgv builds the in-guest command that serves one port-forward (PLAN-24
// D2): the delivered `ape` piping its stdio to a guest-local TCP port.
//
// It lives beside the agent's argv because both are the same idea — the node
// runs its OWN verified binary inside the guest rather than depending on
// anything the image happens to ship — and because both must be built from
// validated input by the process that has the authority to run them, never
// handed in as a command.
func ForwardArgv(port int) ([]string, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("sandbox: forward port %d is not a TCP port (1..65535)", port)
	}
	return []string{path.Join(ApeBinDest, "ape"), "sandbox-connect", strconv.Itoa(port)}, nil
}
