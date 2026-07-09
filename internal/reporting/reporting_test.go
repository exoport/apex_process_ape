package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/blobstore"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
)

func mustDecode(t *testing.T, credsPath string) natsconn.Identity {
	t.Helper()
	id, err := natsconn.DecodeIdentity(credsPath)
	if err != nil {
		t.Fatalf("DecodeIdentity: %v", err)
	}
	return id
}

// collect subscribes to subj on a fresh connection and returns a func that
// waits for one message's payload.
func collect(t *testing.T, url, subj string) (subject func() (string, []byte)) {
	t.Helper()
	sub, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("subscriber connect: %v", err)
	}
	t.Cleanup(sub.Close)
	ch := make(chan *nats.Msg, 8)
	s, err := sub.ChanSubscribe(subj, ch)
	if err != nil {
		t.Fatalf("subscribe %s: %v", subj, err)
	}
	_ = sub.Flush()
	_ = s
	return func() (string, []byte) {
		select {
		case m := <-ch:
			return m.Subject, m.Data
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for a message on %s", subj)
			return "", nil
		}
	}
}

func TestReporterEventSubjectAndPayload(t *testing.T) {
	url := natstest.RunServer(t)
	credsPath, pub := natstest.MintStandaloneCreds(t, "alice")
	id := mustDecode(t, credsPath)

	next := collect(t, url, "ape.evt.>")
	r, err := Connect(context.Background(), natsconn.Config{URL: url}, "test", Options{Identity: id, Project: "/tmp/myproj"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer r.Close()

	if err := r.Event("sess-123", "status", json.RawMessage(`{"phase":"implement","pct":60}`)); err != nil {
		t.Fatalf("Event: %v", err)
	}
	subject, data := next()
	if subject != "ape.evt.alice.myproj.session.sess-123.status" {
		t.Fatalf("subject = %q", subject)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if got["session_id"] != "sess-123" || got["event"] != "status" {
		t.Fatalf("payload missing session_id/event: %v", got)
	}
	user, _ := got["user"].(map[string]any)
	if user["name"] != "alice" || user["public_key"] != pub {
		t.Fatalf("user block = %v (want name=alice public_key=%s)", user, pub)
	}
	payload, _ := got["payload"].(map[string]any)
	if payload["phase"] != "implement" {
		t.Fatalf("payload not carried through: %v", got["payload"])
	}
}

func TestReporterLogSubject(t *testing.T) {
	url := natstest.RunServer(t)
	credsPath, _ := natstest.MintStandaloneCreds(t, "bob")
	id := mustDecode(t, credsPath)

	next := collect(t, url, "ape.log.>")
	r, err := Connect(context.Background(), natsconn.Config{URL: url}, "test", Options{Identity: id, Project: "/tmp/proj"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer r.Close()

	if err := r.Log("sess-9", LevelWarn, "disk almost full", map[string]string{"pct": "92"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	subject, data := next()
	if subject != "ape.log.bob.proj.sess-9.warn" {
		t.Fatalf("subject = %q", subject)
	}
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if got["level"] != "warn" || got["msg"] != "disk almost full" {
		t.Fatalf("log payload = %v", got)
	}
	fields, _ := got["fields"].(map[string]any)
	if fields["pct"] != "92" {
		t.Fatalf("fields = %v", got["fields"])
	}
}

func TestReporterMetricsRepriceRoundTrip(t *testing.T) {
	url := natstest.RunServer(t)
	credsPath, _ := natstest.MintStandaloneCreds(t, "carol")
	id := mustDecode(t, credsPath)

	scan := cost.ScanPaths([]string{filepath.Join("..", "cost", "testdata", "session-golden.jsonl")})
	payload := BuildMetrics(scan, "")

	next := collect(t, url, "ape.metrics.>")
	r, err := Connect(context.Background(), natsconn.Config{URL: url}, "test", Options{Identity: id, Project: "/tmp/proj"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer r.Close()
	if err := r.Metrics("sess-m", payload); err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	subject, data := next()
	if subject != "ape.metrics.carol.proj.sess-m" {
		t.Fatalf("subject = %q", subject)
	}

	// Reprice the published per_model tokens with the same date-aware table
	// and assert it equals the scanner's cost_usd (the "convert to API
	// prices any moment" requirement). Decode per_model into the real
	// eventing.ModelMetrics type so the test carries no wire tags of its own.
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("payload: %v", err)
	}
	var perModel map[string]eventing.ModelMetrics
	if err := json.Unmarshal(env["per_model"], &perModel); err != nil {
		t.Fatalf("per_model: %v", err)
	}
	var repriced float64
	for model, m := range perModel {
		price, ok := cost.LookupAt(model, scan.FirstTurnAt)
		if !ok {
			t.Fatalf("model %q unpriced", model)
		}
		repriced += cost.TurnCost(cost.UsageBlock{
			InputTokens:  m.InputTokens,
			OutputTokens: m.OutputTokens,
			CacheRead:    m.CacheReadInputTokens,
			CacheCreation: cost.CacheCreation{
				Ephemeral5m: m.CacheCreation5m,
				Ephemeral1h: m.CacheCreation1h,
			},
		}, price)
	}
	if d := repriced - scan.Totals.CostUSD; d > 1e-9 || d < -1e-9 {
		t.Fatalf("repriced %.10f != scanner cost_usd %.10f", repriced, scan.Totals.CostUSD)
	}
}

// TestReporterPermissionDenied is the server-enforced-identity acceptance:
// with creds scoped to ape.*.alice.>, publishing under the real token
// succeeds and a forged --debug-subject-user token is rejected by the
// server (surfaced as a publish error → the command exits 1).
func TestReporterPermissionDenied(t *testing.T) {
	url, acct := natstest.StartOperator(t)
	credsPath, _ := natstest.MintCreds(t, acct, "alice", []string{"ape.*.alice.>"}, nil)
	cfg := natsconn.Config{URL: url, CredsFile: credsPath}
	id, err := cfg.Identity()
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}

	// Real token → allowed.
	r, err := Connect(context.Background(), cfg, "test", Options{Identity: id, Project: "/tmp/proj"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer r.Close()
	if err := r.Event("sess", "status", nil); err != nil {
		t.Fatalf("real-token publish should succeed, got: %v", err)
	}

	// Forged token → server rejects the publish.
	rf, err := Connect(context.Background(), cfg, "test", Options{Identity: id, Project: "/tmp/proj", SubjectUser: "forged"})
	if err != nil {
		t.Fatalf("Connect forged: %v", err)
	}
	defer rf.Close()
	if err := rf.Event("sess", "status", nil); err == nil {
		t.Fatal("forged-token publish should be rejected by the server, got nil error")
	}
}

func TestConnectDisabled(t *testing.T) {
	_, err := Connect(context.Background(), natsconn.Config{}, "test", Options{})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

// TestMetricsSchemaIdentical proves the runner path (New over a borrowed
// conn) and the standalone path (Connect) emit byte-identical metrics
// payloads for the same scan — the PLAN-17 exit gate ("differ only in
// subject kind/id"; metrics share one root, so they are fully identical).
func TestMetricsSchemaIdentical(t *testing.T) {
	url := natstest.RunServer(t)
	credsPath, _ := natstest.MintStandaloneCreds(t, "dave")
	id := mustDecode(t, credsPath)
	fixedClock := func() time.Time { return time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC) }
	scan := cost.ScanPaths([]string{filepath.Join("..", "cost", "testdata", "session-golden.jsonl")})
	payload := BuildMetrics(scan, "")

	// Standalone reporter (owns its conn).
	standalone, err := Connect(context.Background(), natsconn.Config{URL: url}, "cli", Options{Identity: id, Project: "/tmp/proj"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer standalone.Close()
	standalone.now = fixedClock

	// Runner reporter (borrowed conn).
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("runner connect: %v", err)
	}
	defer nc.Close()
	runner := New(nc, Options{Identity: id, Project: "/tmp/proj"})
	runner.now = fixedClock

	next := collect(t, url, "ape.metrics.>")
	if err := standalone.Metrics("sess-x", payload); err != nil {
		t.Fatalf("standalone Metrics: %v", err)
	}
	_, a := next()
	if err := runner.Metrics("sess-x", payload); err != nil {
		t.Fatalf("runner Metrics: %v", err)
	}
	_, b := next()

	if !bytes.Equal(a, b) {
		t.Fatalf("payloads differ:\n standalone=%s\n runner    =%s", a, b)
	}
}

func TestTranscriptUploadDedupAndEvent(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	credsPath, _ := natstest.MintStandaloneCreds(t, "erin")
	id := mustDecode(t, credsPath)

	// Two small transcript files (a main + a sub-agent).
	dir := t.TempDir()
	main := filepath.Join(dir, "sess-t.jsonl")
	sub := filepath.Join(dir, "agent-1.jsonl")
	if err := os.WriteFile(main, []byte(`{"type":"assistant","message":{"id":"m1","model":"claude-opus-4-8","usage":{"output_tokens":1}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte(`{"type":"assistant","message":{"id":"s1","model":"claude-haiku-4-5","usage":{"output_tokens":1}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []cost.SessionFile{
		{Path: main, SessionID: "sess-t", Kind: cost.SessionMain},
		{Path: sub, SessionID: "1", Kind: cost.SessionSubagent},
	}

	r, err := Connect(context.Background(), natsconn.Config{URL: url}, "test", Options{Identity: id, Project: "/tmp/proj"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer r.Close()

	store, err := blobstore.NewNATSObjectStore(context.Background(), r.Conn(), "")
	if err != nil {
		t.Fatalf("object store: %v", err)
	}

	res1, err := r.UploadTranscripts(context.Background(), store, "sess-t", files)
	if err != nil {
		t.Fatalf("upload 1: %v", err)
	}
	if len(res1.Files) != 2 {
		t.Fatalf("want 2 files, got %d", len(res1.Files))
	}
	for _, f := range res1.Files {
		if f.Existed {
			t.Errorf("first upload of %s should not pre-exist", f.Path)
		}
	}

	// Second upload → all no-ops (dedup), same digests.
	next := collect(t, url, "ape.evt.>")
	res2, err := r.UploadTranscripts(context.Background(), store, "sess-t", files)
	if err != nil {
		t.Fatalf("upload 2: %v", err)
	}
	for i, f := range res2.Files {
		if !f.Existed {
			t.Errorf("second upload of %s should dedup (Existed)", f.Path)
		}
		if f.Digest != res1.Files[i].Digest {
			t.Errorf("digest changed across uploads: %s vs %s", res1.Files[i].Digest, f.Digest)
		}
	}

	// Companion event carries the digest map.
	if err := r.PublishTranscriptUploaded("sess-t", res2); err != nil {
		t.Fatalf("PublishTranscriptUploaded: %v", err)
	}
	subject, data := next()
	if subject != "ape.evt.erin.proj.session.sess-t.transcript-uploaded" {
		t.Fatalf("companion subject = %q", subject)
	}
	var got struct {
		Payload map[string]struct {
			Digest string `json:"digest"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("companion payload: %v", err)
	}
	if _, ok := got.Payload["sess-t.jsonl"]; !ok {
		t.Fatalf("companion payload missing main blob: %v", got.Payload)
	}
	if _, ok := got.Payload["agent-1.jsonl"]; !ok {
		t.Fatalf("companion payload missing sub-agent blob: %v", got.Payload)
	}
}
