//go:build linux || darwin

package aped

import (
	"encoding/json"
	"testing"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

// Endpoint-level behaviour of the verbs PLAN-24 added. The rig has no attach
// bridge (no NatsConn/Socket) and no cost reporter, which is exactly the shape
// of a node that does not offer these — so it also proves the UNSUPPORTED path,
// which is the one a caller hits on a shell-driver node.

func TestForwardOpenValidatesThePort(t *testing.T) {
	r := startVMMRig(t)
	for _, port := range []int{0, -1, 65536} {
		msg := r.req(t, "forward.open", workspace.ForwardOpenReq{
			V: workspace.WireVersion, ID: testWS, ForwardRequest: workspace.ForwardRequest{Port: port},
		})
		if got := vmmErrCode(msg); got != workspace.CodeValidation {
			t.Errorf("forward.open port %d → code %q, want %s", port, got, workspace.CodeValidation)
		}
	}
}

func TestForwardOpenRequiresAnID(t *testing.T) {
	r := startVMMRig(t)
	msg := r.req(t, "forward.open", workspace.ForwardOpenReq{
		V: workspace.WireVersion, ForwardRequest: workspace.ForwardRequest{Port: 8080},
	})
	if got := vmmErrCode(msg); got != workspace.CodeValidation {
		t.Errorf("forward.open with no id → code %q, want %s", got, workspace.CodeValidation)
	}
}

// A node without the interactive bridge cannot forward, and must say so rather
// than fail obscurely — the CLI turns this into "needs aped run --driver
// containerd".
func TestForwardOpenUnsupportedWithoutTheBridge(t *testing.T) {
	r := startVMMRig(t)
	msg := r.req(t, "forward.open", workspace.ForwardOpenReq{
		V: workspace.WireVersion, ID: testWS, ForwardRequest: workspace.ForwardRequest{Port: 8080},
	})
	if got := vmmErrCode(msg); got != workspace.CodeUnsupported {
		t.Errorf("forward.open on a bridgeless node → code %q, want %s", got, workspace.CodeUnsupported)
	}
}

// The port is validated BEFORE the availability check, so a caller sending a
// nonsense port is told about the port rather than about the node — the error
// they can act on.
func TestForwardOpenValidatesBeforeReportingUnsupported(t *testing.T) {
	r := startVMMRig(t)
	msg := r.req(t, "forward.open", workspace.ForwardOpenReq{
		V: workspace.WireVersion, ID: testWS, ForwardRequest: workspace.ForwardRequest{Port: 0},
	})
	if got := vmmErrCode(msg); got != workspace.CodeValidation {
		t.Errorf("code = %q, want the port error rather than %s", got, workspace.CodeUnsupported)
	}
}

// A node that composes no homes reports no costs, rather than reporting zero.
func TestCostsUnsupportedWithoutAReporter(t *testing.T) {
	r := startVMMRig(t)
	msg := r.req(t, "costs", workspace.CostsReq{V: workspace.WireVersion})
	if got := vmmErrCode(msg); got != workspace.CodeUnsupported {
		t.Errorf("costs with no reporter → code %q, want %s", got, workspace.CodeUnsupported)
	}
}

// Capabilities now carries the capacity block. The fake backend reports a zero
// one, which is the point: the field is present on the wire whatever a backend
// fills in, so a client can rely on it existing.
func TestCapabilitiesCarriesCapacity(t *testing.T) {
	r := startVMMRig(t)
	msg := r.req(t, "capabilities", struct{}{})
	// Present as a key, not merely as a zero value a decoder invented.
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(msg.Data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if _, ok := raw["capacity"]; !ok {
		t.Error("capabilities reply carries no `capacity` key")
	}
}

// The idle-stop setting is part of the create request and comes back on the
// workspace record, so an operator can see why a workspace is or is not being
// reaped without reading the project's descriptor.
func TestCreateCarriesIdleStopThroughTheWire(t *testing.T) {
	var req workspace.CreateRequest
	data := []byte(`{"v":1,"name":"dev","idle_stop":"off"}`)
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	if req.IdleStop != "off" {
		t.Errorf("IdleStop = %q, want off", req.IdleStop)
	}
	out, err := json.Marshal(workspace.Workspace{Name: "dev", IdleStop: "4h"})
	if err != nil {
		t.Fatal(err)
	}
	var back workspace.Workspace
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.IdleStop != "4h" {
		t.Errorf("round-tripped IdleStop = %q, want 4h", back.IdleStop)
	}
}
