package eventing_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
)

// jNum / jMap / jStr are comma-ok JSON field accessors (errcheck runs with
// check-type-assertions, so bare x.(T) is a lint error even in tests).
func jNum(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s: want number, got %T", key, m[key])
	}
	return v
}

func jMap(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%s: want object, got %T", key, m[key])
	}
	return v
}

func jStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s: want string, got %T", key, m[key])
	}
	return v
}

// collect drains every message on sub until a quiet window elapses.
func collect(t *testing.T, sub *nats.Subscription) []*nats.Msg {
	t.Helper()
	var msgs []*nats.Msg
	for {
		m, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			return msgs
		}
		msgs = append(msgs, m)
	}
}

func TestPublisherLifecycle(t *testing.T) {
	url := natstest.RunServer(t)

	obs, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("observer connect: %v", err)
	}
	defer obs.Close()
	sub, err := obs.SubscribeSync("ape.evt.>")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_ = obs.Flush()

	nc, err := natsconn.Connect(context.Background(), natsconn.Config{URL: url}, "ape/test")
	if err != nil {
		t.Fatalf("publisher connect: %v", err)
	}
	defer func() { _ = nc.Drain() }()

	id := natsconn.Identity{Name: "Dev User", Subject: "UABC", SubjectToken: "dev-user"}
	pub := eventing.New(nc, eventing.Options{
		Identity: id,
		Project:  "/home/x/myproj",
		Kind:     eventing.KindPipeline,
		ID:       "run-123",
	})

	pub.RunStart("design", 2)
	pub.StageStart("architecture")
	pub.StepStart("architecture", 1, "apex-create-architecture", "architect", "opus")
	pub.StepEnd("architecture", 1, "apex-create-architecture", "sid-abc", 12.5, eventing.StepMetrics{
		CostUSD:     0.42,
		TokensInput: 100,
		NumTurns:    3,
		PerModel:    map[string]eventing.ModelMetrics{"claude-opus": {CostUSD: 0.42, InputTokens: 100, Turns: 3}},
	})
	pub.Commit("architecture", 1, "abc123", "feat: architecture")
	pub.StageEnd("architecture", 30.0, false)
	pub.Hook("Stop", "sid-abc", "", "architecture/1-apex-create-architecture")
	pub.RunEnd("completed",
		eventing.RunTotals{CostUSD: 0.42, TokensInput: 100, NumTurns: 3, StepsRun: 1, CommitsMade: 1},
		map[string]eventing.TranscriptBlob{"sid-abc.jsonl": {SessionID: "sid-abc", Digest: "sha256:deadbeef", URI: "nats://obj"}},
		"ok")
	pub.Close()

	msgs := collect(t, sub)
	if len(msgs) != 8 {
		t.Fatalf("want 8 events, got %d", len(msgs))
	}

	byEvent := map[string]map[string]any{}
	const wantPrefix = "ape.evt.dev-user.myproj.pipeline.run-123."
	for _, m := range msgs {
		var p map[string]any
		if err := json.Unmarshal(m.Data, &p); err != nil {
			t.Fatalf("payload unmarshal (%s): %v", m.Subject, err)
		}
		event, _ := p["event"].(string)
		if m.Subject != wantPrefix+event {
			t.Errorf("subject = %q, want %q", m.Subject, wantPrefix+event)
		}
		// Common envelope invariants.
		if jNum(t, p, "v") != 1 {
			t.Errorf("%s: v = %v, want 1", event, p["v"])
		}
		if _, err := time.Parse(time.RFC3339Nano, jStr(t, p, "ts")); err != nil {
			t.Errorf("%s: ts not RFC3339: %v", event, p["ts"])
		}
		if p["project"] != "myproj" {
			t.Errorf("%s: project = %v, want myproj", event, p["project"])
		}
		if p["run_id"] != "run-123" {
			t.Errorf("%s: run_id = %v, want run-123", event, p["run_id"])
		}
		user := jMap(t, p, "user")
		if user["name"] != "Dev User" || user["public_key"] != "UABC" {
			t.Errorf("%s: user = %v", event, user)
		}
		byEvent[event] = p
	}

	for _, want := range []string{"run-start", "stage-start", "step-start", "step-end", "commit", "stage-end", "hook", "run-end"} {
		if _, ok := byEvent[want]; !ok {
			t.Errorf("missing event %q", want)
		}
	}

	// run-start specifics.
	if rs := byEvent["run-start"]; rs["pipeline"] != "design" || jNum(t, rs, "stages") != 2 {
		t.Errorf("run-start payload = %v", rs)
	}
	// step-end carries session_id + metrics with a per-model block.
	se := byEvent["step-end"]
	if se["session_id"] != "sid-abc" {
		t.Errorf("step-end session_id = %v", se["session_id"])
	}
	metrics := jMap(t, se, "metrics")
	if jNum(t, metrics, "cost_usd") != 0.42 {
		t.Errorf("step-end cost_usd = %v", metrics["cost_usd"])
	}
	if _, ok := jMap(t, metrics, "per_model")["claude-opus"]; !ok {
		t.Errorf("step-end missing per_model[claude-opus]: %v", metrics["per_model"])
	}
	// run-end carries totals + transcript_blobs.
	re := byEvent["run-end"]
	if re["status"] != "completed" || re["upload_status"] != "ok" {
		t.Errorf("run-end status/upload = %v / %v", re["status"], re["upload_status"])
	}
	if jNum(t, jMap(t, re, "totals"), "steps_run") != 1 {
		t.Errorf("run-end totals = %v", re["totals"])
	}
	if _, ok := jMap(t, re, "transcript_blobs")["sid-abc.jsonl"]; !ok {
		t.Errorf("run-end missing transcript blob: %v", re["transcript_blobs"])
	}
}

func TestNilPublisherNoop(t *testing.T) {
	if got := eventing.New(nil, eventing.Options{}); got != nil {
		t.Fatal("New(nil) must return a nil Publisher")
	}
	var p *eventing.Publisher // nil receiver
	// None of these must panic.
	p.RunStart("x", 1)
	p.StageStart("s")
	p.StepStart("s", 1, "k", "a", "m")
	p.StepEnd("s", 1, "k", "", 0, eventing.StepMetrics{})
	p.Commit("s", 1, "sha", "msg")
	p.StageEnd("s", 0, false)
	p.Hook("Stop", "", "", "")
	p.Error("boom")
	p.RunEnd("failed", eventing.RunTotals{}, nil, "")
	p.Emit("custom", map[string]any{"k": "v"})
	p.Close()
	if p.Dropped() != 0 {
		t.Errorf("nil publisher Dropped = %d", p.Dropped())
	}
}
