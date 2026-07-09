package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/nats-io/nats.go"
)

// TestStartEventingExportsChildEnv verifies the D4 child-env export: after
// startEventing resolves a NATS config from flags, the resolved URL/creds
// are in ape's environment so the spawned claude inherits them (a nested
// `ape event` needs no re-specification).
func TestStartEventingExportsChildEnv(t *testing.T) {
	url := natstest.RunServer(t)
	t.Setenv(natsconn.EnvURL, "")   // ensure the export, not a pre-set env, is what lands
	t.Setenv(natsconn.EnvCreds, "") // (t.Setenv restores both after the test)

	conn, _ := startEventing(context.Background(), runConfig{natsURL: url})
	if conn != nil {
		defer func() { _ = conn.Drain() }()
	}
	if got := os.Getenv(natsconn.EnvURL); got != url {
		t.Fatalf("APE_NATS_URL = %q, want %q (child-env export)", got, url)
	}
}

// TestPublishSessionMetricsMatchesStandalone is the D4 exit gate: the runner
// finalize path publishes an ape.metrics message for the run's main session,
// through the same reporting builder + scan path as a standalone `ape
// metrics` — so the payloads are schema-identical (they differ only in ts).
func TestPublishSessionMetricsMatchesStandalone(t *testing.T) {
	// A fake ~/.claude tree so sessionref.FindTranscript resolves the main
	// session's transcript (a copy of the golden fixture).
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	projectDir := "/home/u/proj"
	placeTranscript(t, home, projectDir, "sess-main")

	url := natstest.RunServer(t)
	obs, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("observer connect: %v", err)
	}
	defer obs.Close()
	sub, err := obs.SubscribeSync("ape.metrics.>")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_ = obs.Flush()

	conn, err := natsconn.Connect(context.Background(), natsconn.Config{URL: url}, "ape/test")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Drain() }()

	// A manifest recording one main session (empty parent) + one sub.
	m := pipeline.Manifest{
		Stages: []pipeline.StageRecord{{
			Steps: []pipeline.StepRecord{{
				Sessions: []pipeline.SessionUsageRecord{
					{SessionID: "sess-main"},                             // main (no parent)
					{SessionID: "agent-x", ParentSessionID: "sess-main"}, // sub (skipped)
				},
			}},
		}},
	}
	id := natsconn.Identity{Name: "Dev", Subject: "UABC", SubjectToken: "dev"}
	publishSessionMetrics(conn, id, projectDir, runConfig{}, m)

	msg, err := sub.NextMsg(3 * time.Second)
	if err != nil {
		t.Fatalf("no metrics message published: %v", err)
	}
	if msg.Subject != "ape.metrics.dev.proj.sess-main" {
		t.Fatalf("subject = %q", msg.Subject)
	}
	var got map[string]any
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if got["session_id"] != "sess-main" {
		t.Fatalf("session_id = %v", got["session_id"])
	}
	if _, ok := got["per_model"]; !ok {
		t.Fatalf("payload missing per_model: %v", got)
	}
	// Exactly one metrics message (the sub session must be skipped).
	if _, err := sub.NextMsg(300 * time.Millisecond); err == nil {
		t.Fatal("expected only the main session's metrics; got a second message")
	}
}

// placeTranscript writes a copy of the golden fixture as the main
// transcript for (projectDir, sid) under a fake ~/.claude tree.
func placeTranscript(t *testing.T, home, projectDir, sid string) {
	t.Helper()
	data, err := os.ReadFile(goldenFixture())
	if err != nil {
		t.Fatal(err)
	}
	// sessionref.ProjectSlug is the directory Claude would use for projectDir.
	dir := filepath.Join(home, ".claude", "projects", claudeSlug(projectDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeSlug mirrors sessionref.ProjectSlug (non-alphanumeric → '-') for the
// fake tree; FindTranscript globs projects/*/<sid>.jsonl so any dir name
// works, but this keeps the fixture realistic.
func claudeSlug(p string) string {
	b := make([]rune, 0, len(p))
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b = append(b, r)
		default:
			b = append(b, '-')
		}
	}
	return string(b)
}
