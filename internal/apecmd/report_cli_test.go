package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/exoport/apex_process_ape/internal/reporting"
	"github.com/nats-io/nats.go"
)

// testSessionID is the explicit session id the event/log subtests report for.
const testSessionID = "sess1"

// goldenFixture is the shared cost transcript fixture, reachable from the
// apecmd test dir.
func goldenFixture() string {
	return filepath.Join("..", "cost", "testdata", "session-golden.jsonl")
}

// subCollector subscribes to subj and returns a waiter for one message.
func subCollector(t *testing.T, nc *nats.Conn, subj string) func() *nats.Msg {
	t.Helper()
	ch := make(chan *nats.Msg, 8)
	if _, err := nc.ChanSubscribe(subj, ch); err != nil {
		t.Fatalf("subscribe %s: %v", subj, err)
	}
	_ = nc.Flush()
	return func() *nats.Msg {
		select {
		case m := <-ch:
			return m
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting on %s", subj)
			return nil
		}
	}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("error is not an *exitError: %v", err)
	}
	return ee.code
}

// TestReportingCommandsE2E drives the four command cores in-process against
// an embedded operator server, with only a scoped .creds file — the PLAN-17
// acceptance. Creds are scoped to ape.*.agent1.>, so the run also proves
// server-enforced identity (the forged-token subtest).
func TestReportingCommandsE2E(t *testing.T) {
	url, acct := natstest.StartOperator(t)
	credsPath, _ := natstest.MintCreds(t, acct, "agent1", []string{"ape.*.agent1.>"}, nil)

	sub, err := nats.Connect(url, nats.UserCredentials(credsPath))
	if err != nil {
		t.Fatalf("subscriber connect: %v", err)
	}
	t.Cleanup(sub.Close)

	base := func() reportFlags {
		// A fixed cwd makes the <project> subject segment deterministic
		// ("myproj") regardless of where the test binary runs.
		return reportFlags{natsURL: url, natsCreds: credsPath, cwd: "/tmp/myproj", outputFormat: "json", quiet: true}
	}
	ctx := context.Background()

	t.Run("event", func(t *testing.T) {
		wait := subCollector(t, sub, "ape.evt.>")
		f := base()
		f.sessionID = testSessionID
		var out bytes.Buffer
		if err := runEvent(ctx, &out, strings.NewReader(""), &f, "status", `{"pct":60}`); err != nil {
			t.Fatalf("runEvent: %v", err)
		}
		m := wait()
		if m.Subject != "ape.evt.agent1.myproj.session.sess1.status" {
			t.Fatalf("event subject = %q", m.Subject)
		}
		assertJSONField(t, out.Bytes(), "session_id", "sess1")
	})

	t.Run("log", func(t *testing.T) {
		wait := subCollector(t, sub, "ape.log.>")
		f := base()
		f.sessionID = testSessionID
		var out bytes.Buffer
		if err := runLog(ctx, &out, &f, "info", "step done", []string{"k=v"}); err != nil {
			t.Fatalf("runLog: %v", err)
		}
		m := wait()
		if !strings.HasPrefix(m.Subject, "ape.log.agent1.") || !strings.HasSuffix(m.Subject, ".sess1.info") {
			t.Fatalf("log subject = %q", m.Subject)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		wait := subCollector(t, sub, "ape.metrics.>")
		f := base()
		f.transcript = goldenFixture()
		var out bytes.Buffer
		if err := runMetrics(ctx, &out, &f, ""); err != nil {
			t.Fatalf("runMetrics: %v", err)
		}
		m := wait()
		if !strings.HasPrefix(m.Subject, "ape.metrics.agent1.") {
			t.Fatalf("metrics subject = %q", m.Subject)
		}
		// session id is parsed from the transcript filename.
		if !strings.HasSuffix(m.Subject, ".session-golden") {
			t.Fatalf("metrics subject session = %q", m.Subject)
		}
	})

	t.Run("forged identity is rejected (exit 1)", func(t *testing.T) {
		f := base()
		f.sessionID = testSessionID
		f.debugSubjectUser = "forged"
		var out bytes.Buffer
		err := runEvent(ctx, &out, strings.NewReader(""), &f, "status", "")
		if err == nil {
			t.Fatal("forged identity should fail")
		}
		if code := exitCode(t, err); code != ExitRunFailed {
			t.Fatalf("forged exit code = %d, want %d", code, ExitRunFailed)
		}
	})

	t.Run("no NATS config (exit 2)", func(t *testing.T) {
		f := reportFlags{outputFormat: "human"}
		var out bytes.Buffer
		err := runEvent(ctx, &out, strings.NewReader(""), &f, "status", "")
		if code := exitCode(t, err); code != ExitUsage {
			t.Fatalf("no-NATS exit code = %d, want %d", code, ExitUsage)
		}
	})

	t.Run("unresolvable session (exit 2)", func(t *testing.T) {
		f := base()
		f.transcript = filepath.Join(t.TempDir(), "does-not-exist.jsonl")
		var out bytes.Buffer
		err := runEvent(ctx, &out, strings.NewReader(""), &f, "status", "")
		if code := exitCode(t, err); code != ExitUsage {
			t.Fatalf("unresolvable exit code = %d, want %d", code, ExitUsage)
		}
	})
}

// TestTranscriptUploadCommandE2E drives `ape transcript upload` against an
// embedded JetStream server (the nats-object backend).
func TestTranscriptUploadCommandE2E(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	f := reportFlags{natsURL: url, transcript: goldenFixture(), outputFormat: "json", quiet: true}
	var out bytes.Buffer
	if err := runTranscriptUpload(context.Background(), &out, &f, "nats-object"); err != nil {
		t.Fatalf("runTranscriptUpload: %v", err)
	}
	var res reporting.TranscriptResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("result JSON: %v", err)
	}
	if res.SessionID != "session-golden" || len(res.Files) == 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.Files[0].Digest == "" {
		t.Fatalf("missing digest: %+v", res.Files[0])
	}
}

// assertJSONField checks a top-level string field in a JSON object.
func assertJSONField(t *testing.T, data []byte, key, want string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("stdout not JSON: %v (%s)", err, data)
	}
	if m[key] != want {
		t.Fatalf("stdout %s = %v, want %q", key, m[key], want)
	}
}
