package blobstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/blobstore"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/nats-io/nats.go"
)

func writeTranscript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompressRoundTrip(t *testing.T) {
	orig := []byte(`{"type":"assistant","message":{"id":"m1"}}` + "\n")
	comp, err := blobstore.Compress(orig)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	got, err := blobstore.Decompress(comp)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("round trip mismatch: %q != %q", got, orig)
	}
	if blobstore.DigestOf(orig).Algo != "sha256" {
		t.Error("digest algo should be sha256")
	}
}

func TestNATSObjectStore_UploadDedupRoundTrip(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	ctx := context.Background()
	store, err := blobstore.NewNATSObjectStore(ctx, nc, "")
	if err != nil {
		t.Fatalf("new object store: %v", err)
	}

	content := `{"type":"assistant","message":{"id":"m1","model":"claude-opus"}}` + "\n"
	path := writeTranscript(t, content)

	// First upload stores the blob.
	r1, err := blobstore.UploadFile(ctx, store, path)
	if err != nil {
		t.Fatalf("upload 1: %v", err)
	}
	if r1.Existed {
		t.Error("first upload should not report existed")
	}
	if r1.Digest.String() != blobstore.DigestOf([]byte(content)).String() {
		t.Errorf("digest mismatch: %s", r1.Digest)
	}

	// Second upload of identical content is a dedup no-op with same digest.
	r2, err := blobstore.UploadFile(ctx, store, path)
	if err != nil {
		t.Fatalf("upload 2: %v", err)
	}
	if !r2.Existed {
		t.Error("second upload should report existed (dedup)")
	}
	if r2.Digest != r1.Digest {
		t.Errorf("digests differ across runs: %s vs %s", r1.Digest, r2.Digest)
	}

	// Stored bytes round-trip back to the original content.
	got, err := store.Get(ctx, r1.Digest)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != content {
		t.Fatalf("stored content mismatch: %q", got)
	}

	if has, _ := store.Has(ctx, r1.Digest); !has {
		t.Error("Has should be true after upload")
	}
}

func TestURIOffload_UploadThenExists(t *testing.T) {
	url := natstest.RunServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	// Stub HTTPS target that records the PUT body.
	var putBody atomic.Value
	var putCount atomic.Int64
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		putBody.Store(b)
		putCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer httpSrv.Close()

	// Stub offload service: first request → upload, later → exists.
	var gotReq atomic.Value
	var callCount atomic.Int64
	sub, err := nc.Subscribe(blobstore.URIRequestSubject, func(m *nats.Msg) {
		var req blobstore.UploadRequest
		_ = json.Unmarshal(m.Data, &req)
		gotReq.Store(req)
		n := callCount.Add(1)
		var reply blobstore.UploadReply
		if n == 1 {
			reply = blobstore.UploadReply{Status: "upload", URI: httpSrv.URL + "/blob", Method: http.MethodPut}
		} else {
			reply = blobstore.UploadReply{Status: "exists", URI: httpSrv.URL + "/blob"}
		}
		data, _ := json.Marshal(reply)
		_ = m.Respond(data)
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	ctx := context.Background()
	store := blobstore.NewURIOffloadStore(nc, blobstore.URIOffloadConfig{
		Project:        "myproj",
		RunID:          "run-1",
		RequestTimeout: 3 * time.Second,
	})

	content := `{"type":"assistant"}` + "\n"
	path := writeTranscript(t, content)

	// First upload: request → upload → HTTPS PUT of the compressed payload.
	r1, err := blobstore.UploadFile(ctx, store, path)
	if err != nil {
		t.Fatalf("upload 1: %v", err)
	}
	if r1.Existed {
		t.Error("first upload should not be existed")
	}
	if putCount.Load() != 1 {
		t.Fatalf("expected exactly 1 HTTPS PUT, got %d", putCount.Load())
	}

	// The request payload carried the digest + sizes + content type.
	req, ok := gotReq.Load().(blobstore.UploadRequest)
	if !ok {
		t.Fatalf("offload service saw no request")
	}
	if req.Digest != r1.Digest.String() {
		t.Errorf("request digest = %q, want %q", req.Digest, r1.Digest.String())
	}
	if req.ContentType != blobstore.ContentType {
		t.Errorf("request content_type = %q", req.ContentType)
	}
	if req.Size != r1.Size || req.CompressedSize != r1.CompressedSize {
		t.Errorf("request sizes = (%d,%d), want (%d,%d)", req.Size, req.CompressedSize, r1.Size, r1.CompressedSize)
	}
	if req.Project != "myproj" || req.RunID != "run-1" {
		t.Errorf("request project/run = %q/%q", req.Project, req.RunID)
	}

	// The PUT body is the compressed payload and decompresses to the original.
	body, ok := putBody.Load().([]byte)
	if !ok {
		t.Fatalf("no PUT body captured")
	}
	if int64(len(body)) != r1.CompressedSize {
		t.Errorf("PUT body size = %d, want compressed %d", len(body), r1.CompressedSize)
	}
	if dec, err := blobstore.Decompress(body); err != nil || string(dec) != content {
		t.Errorf("PUT body did not decompress to original: err=%v got=%q", err, dec)
	}

	// Second upload: service replies "exists" → short-circuit, no PUT.
	r2, err := blobstore.UploadFile(ctx, store, path)
	if err != nil {
		t.Fatalf("upload 2: %v", err)
	}
	if !r2.Existed {
		t.Error("second upload should be existed (exists short-circuit)")
	}
	if putCount.Load() != 1 {
		t.Errorf("exists short-circuit should not PUT again, got %d PUTs", putCount.Load())
	}
}
