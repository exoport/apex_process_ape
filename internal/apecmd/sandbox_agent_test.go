package apecmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/sandbox"
)

// agentSourceFile is the one file the agent's command lives in. The safety-belt
// assertions below are about ITS code path, so they are scoped to it.
const agentSourceFile = "sandbox_agent.go"

// The second safety belt, asserted rather than asserted-in-a-comment (PLAN-24
// D6 trap 4).
//
// The first belt is the NATS server: a per-VM credential is denied ape.vmm.>
// outright and lives in an account management does not exist in. That is the
// enforcement, and it holds regardless of what this code does.
//
// This is the second, independent one: the agent command must carry no way to
// BUILD a management request in the first place, so a bug — or a compromised
// guest steering the agent — cannot issue one even before the server refuses it.
// A comment saying so decays; this does not.
func TestSandboxAgentHasNoVMMRequestPath(t *testing.T) {
	src, err := os.ReadFile(agentSourceFile)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, agentSourceFile, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	// (1) No import can reach the management client or the management contract's
	// request builders. vmmclient is the whole vmm surface in one package, so
	// importing it is the thing to forbid; vmmstream carries interactive session
	// stdio, which an agent has no business in either.
	forbiddenImports := []string{
		"internal/vmmclient",
		"internal/vmmstream",
		"internal/aped",
	}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		for _, bad := range forbiddenImports {
			if strings.HasSuffix(path, bad) {
				t.Errorf("%s imports %s — the agent must carry no vmm-request-builder code path",
					agentSourceFile, path)
			}
		}
	}

	// (2) No literal reference to the management subject root, in code OR in a
	// string. The agent publishes telemetry; naming ape.vmm at all in this file
	// means something is being built that should not be.
	code := stripComments(string(src), file, fset)
	for _, needle := range []string{"ape.vmm", "AttachOpen", "workspace.CreateRequest", "vmmclient."} {
		if strings.Contains(code, needle) {
			t.Errorf("%s references %q outside a comment — the agent must not be able to name a management verb",
				agentSourceFile, needle)
		}
	}
}

// stripComments returns the file's source with comment text removed, so an
// assertion about CODE is not tripped by a comment that (legitimately) explains
// what the code must not do.
func stripComments(src string, file *ast.File, fset *token.FileSet) string {
	out := []byte(src)
	base := fset.File(file.Pos()).Base()
	for _, group := range file.Comments {
		start := int(group.Pos()) - base
		end := int(group.End()) - base
		if start < 0 || end > len(out) || start >= end {
			continue
		}
		for i := start; i < end; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return string(out)
}

// The agent's publish subject is derived from its OWN credential, and the guard
// refuses anything else. The server enforces this too — this makes a bug fail
// where it can be seen instead of looking like a network problem.
func TestHeartbeatPublisherRefusesAForeignSubject(t *testing.T) {
	p := &heartbeatPublisher{
		token:   "vm-alpha",
		subject: sandbox.HeartbeatSubject("vm-beta"), // as a bug would compute it
		stderr:  &strings.Builder{},
	}
	err := p.publish(sandbox.Heartbeat{V: 1, Workspace: "vm-alpha"})
	if err == nil {
		t.Fatal("the agent published on another workspace's subject")
	}
	if !strings.Contains(err.Error(), "vm-alpha") {
		t.Errorf("the refusal does not name the subject this agent may use: %v", err)
	}
}

func TestHeartbeatBuildReportsBusyAgainstTheThreshold(t *testing.T) {
	p := &heartbeatPublisher{token: "vm-alpha", started: time.Now().Add(-time.Minute)}
	prev := sandbox.GuestSample{TotalJiffies: 1000, IdleJiffies: 900, PerProcess: map[string]uint64{"go": 10}}
	// 100 jiffies elapsed, 10 idle → 90% busy.
	cur := sandbox.GuestSample{TotalJiffies: 1100, IdleJiffies: 910, PerProcess: map[string]uint64{"go": 95}}

	h, ok := p.build(prev, cur, sandbox.DefaultBusyPercent)
	if !ok {
		t.Fatal("build reported no reading from a valid pair of samples")
	}
	if !h.Busy {
		t.Errorf("Busy = false at %.1f%% cpu", h.CPUPercent)
	}
	if h.CPUPercent < 89 || h.CPUPercent > 91 {
		t.Errorf("CPUPercent = %.1f, want ~90", h.CPUPercent)
	}
	if len(h.Top) == 0 || h.Top[0] != "go" {
		t.Errorf("Top = %v, want the process that burned the cycles first", h.Top)
	}
	if h.Workspace != "vm-alpha" {
		t.Errorf("Workspace = %q, want the agent's own token", h.Workspace)
	}
	if h.UptimeSeconds < 59 {
		t.Errorf("UptimeSeconds = %d, want the agent's own uptime", h.UptimeSeconds)
	}
}

// The case that must NOT produce a heartbeat: an unreadable guest. A fabricated
// 0% would be published as "idle" and would eventually stop a healthy workspace.
func TestHeartbeatBuildPublishesNothingWithoutAReading(t *testing.T) {
	p := &heartbeatPublisher{token: "vm-alpha"}
	empty := sandbox.GuestSample{PerProcess: map[string]uint64{}}
	if _, ok := p.build(empty, empty, sandbox.DefaultBusyPercent); ok {
		t.Error("build produced a heartbeat from no CPU reading — it would read as idle")
	}
}

func TestSandboxAgentNeedsTheGuestEnvironment(t *testing.T) {
	// Run outside a workspace: it must explain itself rather than fail on a dial.
	t.Setenv("APE_NATS_URL", "")
	t.Setenv("APE_NATS_CREDS", "")
	cmd := newSandboxAgentCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("running the agent outside a workspace succeeded")
	}
	if !strings.Contains(err.Error(), "APE_NATS_CREDS") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

func TestSandboxAgentIsHidden(t *testing.T) {
	// It is machinery aped launches, not a verb a human types — but running it by
	// hand must still work (see the test above), so it is hidden, not disabled.
	if !newSandboxAgentCmd().Hidden {
		t.Error("sandbox-agent should be hidden from the command list")
	}
}
