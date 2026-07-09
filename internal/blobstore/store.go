// Package blobstore uploads a run's transcript set as deduplicated,
// content-addressed blobs (PLAN-13 D3). Addressing is sha256 over the
// uncompressed payload (stdlib, matching the existing fileDigest helper);
// payloads are stored zstd-compressed (level 3, cxdb parity). Two backends
// share the Store interface: a NATS JetStream Object Store (staging) and a
// URI-request offload flow (NATS request returns an upload URI; ape does the
// HTTPS PUT) so large fleets can land blobs in real object storage while the
// wire stays NATS+HTTPS.
//
// cxdb concepts borrowed: client-side hash, idempotent put, dedup — not the
// architecture (no turn-DAG, no cxdb server).
package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// ContentType is the stored payload's media type: newline-delimited JSON,
// zstd-compressed. Carried in the URI-offload request.
const ContentType = "application/x-ndjson+zstd"

// Digest is a content address: an algorithm plus lowercase hex.
type Digest struct {
	Algo string
	Hex  string
}

// String renders "algo:hex" (e.g. "sha256:abcd…").
func (d Digest) String() string { return d.Algo + ":" + d.Hex }

// ObjectName renders "algo/hex" for use as a store object key.
func (d Digest) ObjectName() string { return d.Algo + "/" + d.Hex }

// Store is a content-addressed blob sink. Put is idempotent: a blob already
// present is not re-uploaded (existed=true).
//
// This merges the plan's Has+Put sketch into one idempotent Put because the
// URI-offload backend's reply ("upload" vs "exists") is a single round trip —
// a separate Has would double the request. The concrete NATS backend still
// exposes Has for callers that want a cheap existence probe.
type Store interface {
	// Put stores the zstd-compressed payload (read from compressed)
	// addressed by d, which is the sha256 of the UNCOMPRESSED bytes.
	// sizeUncompressed/sizeCompressed describe the payload for the offload
	// contract. Returns a locator URI and whether the blob already existed.
	Put(ctx context.Context, d Digest, sizeUncompressed, sizeCompressed int64, compressed io.Reader) (uri string, existed bool, err error)
}

// Result is the outcome of uploading one file.
type Result struct {
	Digest         Digest
	URI            string
	Size           int64 // uncompressed
	CompressedSize int64
	Existed        bool // true when the blob was already present (dedup no-op)
}

// UploadFile content-addresses a file (sha256 of its raw bytes), zstd-
// compresses it, and uploads it idempotently through store. Transcript
// files are small (p99 ~1–2 MB compressed per PLAN-13 dimensioning), so
// buffering the payload in memory is deliberate and cheap.
func UploadFile(ctx context.Context, store Store, path string) (Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("blobstore: read %s: %w", path, err)
	}
	d := DigestOf(raw)
	comp, err := Compress(raw)
	if err != nil {
		return Result{}, fmt.Errorf("blobstore: compress %s: %w", path, err)
	}
	uri, existed, err := store.Put(ctx, d, int64(len(raw)), int64(len(comp)), bytes.NewReader(comp))
	if err != nil {
		return Result{}, err
	}
	return Result{
		Digest:         d,
		URI:            uri,
		Size:           int64(len(raw)),
		CompressedSize: int64(len(comp)),
		Existed:        existed,
	}, nil
}

// DigestOf returns the sha256 content address of raw.
func DigestOf(raw []byte) Digest {
	sum := sha256.Sum256(raw)
	return Digest{Algo: "sha256", Hex: hex.EncodeToString(sum[:])}
}

// Compress zstd-compresses src at level 3 (cxdb parity).
func Compress(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(src); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decompress inflates a zstd payload. Used by the object-store Get path and
// for round-trip verification.
func Decompress(compressed []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
