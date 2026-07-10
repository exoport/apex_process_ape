package aped

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditorRecordBothSinks(t *testing.T) {
	var log bytes.Buffer
	var gotSubject string
	var gotData []byte
	a := NewAuditor(&log, func(subject string, data []byte) {
		gotSubject = subject
		gotData = data
	}, "node1")

	a.Record(AuditRecord{
		BoundaryPeer: &BoundaryPeer{UID: 1000, PID: 4242},
		Caller:       "alice",
		Op:           "CreateVM",
		Resolved:     ResolvedArgs{WorkspaceID: testWS, Image: testImage, Mount: "/home/alice/proj"},
		Policy:       PolicyDecision{Rule: "images", Decision: DecisionAllow},
		Outcome:      Outcome{OK: true, VMID: testWS},
	})

	// Log sink: one JSONL line with the resolved args + a stamped ts.
	line := strings.TrimSpace(log.String())
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("want exactly one JSONL line, got:\n%s", line)
	}
	var rec AuditRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line not valid JSON: %v", err)
	}
	if rec.TS == "" {
		t.Error("ts not stamped")
	}
	if rec.Resolved.Image != testImage || rec.Resolved.Mount != "/home/alice/proj" {
		t.Errorf("resolved args not recorded: %+v", rec.Resolved)
	}
	if rec.BoundaryPeer == nil || rec.BoundaryPeer.UID != 1000 {
		t.Errorf("boundary peer not recorded: %+v", rec.BoundaryPeer)
	}
	if rec.Policy.Decision != DecisionAllow {
		t.Errorf("policy decision = %q, want allow", rec.Policy.Decision)
	}

	// NATS sink: ape.audit.<node>.<event>.
	if gotSubject != "ape.audit.node1.createvm" {
		t.Errorf("subject = %q, want ape.audit.node1.createvm", gotSubject)
	}
	if !bytes.Equal(gotData, []byte(line)) {
		t.Error("published payload should equal the logged record")
	}
}

func TestAuditorAppendsMultiple(t *testing.T) {
	var log bytes.Buffer
	a := NewAuditor(&log, nil, "node1")
	a.Record(AuditRecord{Op: "CreateVM", Outcome: Outcome{OK: true}})
	a.Record(AuditRecord{Op: "DestroyVM", Outcome: Outcome{OK: true}})
	if got := strings.Count(strings.TrimSpace(log.String()), "\n"); got != 1 {
		t.Fatalf("want 2 JSONL lines (1 separator), got %d newlines:\n%s", got, log.String())
	}
}

//nolint:revive // t is required by the go-test signature; this asserts no panic
func TestAuditorNilSinks(t *testing.T) {
	// A no-sink auditor must not panic (auditing never fails the op).
	NewAuditor(nil, nil, "node1").Record(AuditRecord{Op: "StartVM"})
}
