package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/nats-io/nats.go"
	"gopkg.in/yaml.v3"
)

func collectEvents(t *testing.T, sub *nats.Subscription) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for {
		m, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			return out
		}
		var p map[string]any
		if err := json.Unmarshal(m.Data, &p); err != nil {
			t.Fatalf("payload unmarshal (%s): %v", m.Subject, err)
		}
		event, _ := p["event"].(string)
		p["__subject"] = m.Subject
		out[event] = p
	}
}

// TestInteractiveCoreLifecycleEvents drives the interactive core's event
// taps (the novel PLAN-13 wiring) against an embedded server and asserts the
// stage/step/hook events land on the expected subjects.
func TestInteractiveCoreLifecycleEvents(t *testing.T) {
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

	conn, err := natsconn.Connect(context.Background(), natsconn.Config{URL: url}, "ape/test")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Drain() }()

	core := newInteractiveCore(func() {}, func() *runlog.Writer { return nil })
	pub := newEventPublisher(conn,
		natsconn.Identity{Name: "Dev", Subject: "UABC", SubjectToken: "dev"},
		"/home/x/myproj", "run-xyz", runConfig{})
	core.setPublisher(pub)

	// run-start fires from onRunDir in production; mirror it here.
	pub.RunStart("design", 1)
	core.ResetStageTelemetry("architecture") // → stage-start
	core.OnStepStart(pipeline.InteractiveStepInfo{Stage: "architecture", StepIdx: 0, Skill: "apex-x", Agent: "a", Model: "m"})
	core.FeedHook(orchestrator.HookEvent{Event: "PreToolUse", SessionID: "sid-1", Step: "architecture/1-apex-x", At: time.Now()})
	_ = core.StepTelemetry("architecture", 0) // → step-end (diagnostic note; empty metrics)
	core.OnStepEnd(pipeline.InteractiveStepInfo{Stage: "architecture", StepIdx: 0, Skill: "apex-x"})
	pub.Close()

	events := collectEvents(t, sub)
	const prefix = "ape.evt.dev.myproj.pipeline.run-xyz."
	for _, want := range []string{"run-start", "stage-start", "step-start", "step-end", "hook"} {
		p, ok := events[want]
		if !ok {
			t.Errorf("missing event %q (got %v)", want, keys(events))
			continue
		}
		if p["__subject"] != prefix+want {
			t.Errorf("%s subject = %q, want %q", want, p["__subject"], prefix+want)
		}
	}
	// step-start carries the step metadata.
	if ss := events["step-start"]; ss["skill"] != "apex-x" || jNum(t, ss, "step") != 1 {
		t.Errorf("step-start payload = %v", ss)
	}
	// hook carries the hook name.
	if h := events["hook"]; h["hook"] != "PreToolUse" {
		t.Errorf("hook payload = %v", h)
	}
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// jNum / jMap are comma-ok JSON field accessors (errcheck runs with
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

// writeRun creates a minimal finalized run dir (manifest + transcript
// snapshots) for the finalize tests.
func writeRun(t *testing.T, projectRoot string) string {
	t.Helper()
	runDir := filepath.Join(t.TempDir(), "20260709-000000-abc1234")
	if err := os.MkdirAll(filepath.Join(runDir, "transcripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := pipeline.Manifest{
		SchemaVersion: pipeline.ManifestSchemaVersion,
		RunID:         filepath.Base(runDir),
		ProjectRoot:   projectRoot,
		Status:        pipeline.StatusCompleted,
		Totals:        pipeline.ManifestTotals{CostUSD: 1.5, NumTurns: 5, StepsRun: 2},
	}
	data, err := yaml.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "manifest.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sess-1.jsonl", "agent-aaa.jsonl"} {
		if err := os.WriteFile(filepath.Join(runDir, "transcripts", name), []byte(`{"type":"assistant"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runDir
}

// TestFinalizeRunUploadsStampsAndPublishes is the transcript-blob half of the
// PLAN-13 exit gate: --upload-transcripts lands content-addressed blobs,
// stamps the manifest, is idempotent on re-run, and run-end carries totals +
// the blob map.
func TestFinalizeRunUploadsStampsAndPublishes(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
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

	conn, err := natsconn.Connect(context.Background(), natsconn.Config{URL: url}, "ape/test")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Drain() }()

	projectRoot := "/home/x/myproj"
	runDir := writeRun(t, projectRoot)
	cfg := runConfig{uploadTranscripts: true, transcriptStore: "nats-object"}

	pub := newEventPublisher(conn, natsconn.Identity{SubjectToken: "dev"}, projectRoot, filepath.Base(runDir), cfg)
	finalizeRun(context.Background(), pub, conn, runDir, projectRoot, cfg, nil)
	pub.Close()

	// Manifest stamped with 2 blobs + upload_status ok.
	m, err := pipeline.LoadManifest(runDir)
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	if m.UploadStatus != uploadStatusOK {
		t.Errorf("upload_status = %q, want ok", m.UploadStatus)
	}
	if len(m.TranscriptBlobs) != 2 {
		t.Fatalf("want 2 transcript blobs, got %d: %v", len(m.TranscriptBlobs), m.TranscriptBlobs)
	}
	firstDigest := m.TranscriptBlobs["sess-1.jsonl"].Digest
	if firstDigest == "" {
		t.Error("sess-1.jsonl blob missing a digest")
	}

	// run-end event carries totals + the blob map.
	events := collectEvents(t, sub)
	re, ok := events["run-end"]
	if !ok {
		t.Fatalf("no run-end event (got %v)", keys(events))
	}
	if re["status"] != "completed" || re["upload_status"] != uploadStatusOK {
		t.Errorf("run-end status/upload = %v/%v", re["status"], re["upload_status"])
	}
	if jNum(t, jMap(t, re, "totals"), "steps_run") != 2 {
		t.Errorf("run-end totals = %v", re["totals"])
	}
	if len(jMap(t, re, "transcript_blobs")) != 2 {
		t.Errorf("run-end transcript_blobs = %v", re["transcript_blobs"])
	}

	// Idempotent re-run: dedup no-op, identical digests, still ok.
	pub2 := newEventPublisher(conn, natsconn.Identity{SubjectToken: "dev"}, projectRoot, filepath.Base(runDir), cfg)
	finalizeRun(context.Background(), pub2, conn, runDir, projectRoot, cfg, nil)
	pub2.Close()
	m2, _ := pipeline.LoadManifest(runDir)
	if m2.UploadStatus != uploadStatusOK {
		t.Errorf("re-run upload_status = %q, want ok", m2.UploadStatus)
	}
	if m2.TranscriptBlobs["sess-1.jsonl"].Digest != firstDigest {
		t.Errorf("digest changed across runs: %q vs %q", m2.TranscriptBlobs["sess-1.jsonl"].Digest, firstDigest)
	}
}

// TestFinalizeRunDegradesWhenNATSUnavailable is the degradation half of the
// gate: with upload requested but NATS unreachable, the run does not fail,
// nothing panics, and the manifest records upload_status: failed.
func TestFinalizeRunDegradesWhenNATSUnavailable(t *testing.T) {
	projectRoot := "/home/x/myproj"
	runDir := writeRun(t, projectRoot)

	// pub nil + conn nil = NATS off/unreachable; upload was requested.
	finalizeRun(context.Background(), nil, nil, runDir, projectRoot,
		runConfig{uploadTranscripts: true}, errors.New("run failed"))

	m, err := pipeline.LoadManifest(runDir)
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	if m.UploadStatus != uploadStatusFailed {
		t.Errorf("upload_status = %q, want failed", m.UploadStatus)
	}
	if len(m.TranscriptBlobs) != 0 {
		t.Errorf("no blobs should be recorded on failure, got %v", m.TranscriptBlobs)
	}
}
