package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/blobstore"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/nats-io/nats.go"
)

// TranscriptFile is one uploaded transcript in the result object.
//
//nolint:tagliatelle // snake_case is the JSON wire contract
type TranscriptFile struct {
	Path      string `json:"path"`
	SessionID string `json:"session_id"`
	Digest    string `json:"digest"`
	URI       string `json:"uri,omitempty"`
	Bytes     int64  `json:"bytes"`
	Existed   bool   `json:"existed"` // true when the blob was already present (dedup no-op)
}

// TranscriptResult is `ape transcript upload`'s result object (stdout).
//
//nolint:tagliatelle // snake_case is the JSON wire contract
type TranscriptResult struct {
	SessionID string           `json:"session_id"`
	Files     []TranscriptFile `json:"files"`
}

// Conn exposes the underlying connection so a caller can build a blobstore
// backend against the same connection (the standalone `ape transcript`
// command selects nats-object vs uri-offload and passes the store to
// UploadTranscripts).
func (r *Reporter) Conn() *nats.Conn { return r.nc }

// UploadTranscripts uploads a session's file set (main + sub-agents,
// content-addressed, zstd, idempotent) through store. Re-uploading a blob
// already present is a cheap no-op (Existed=true, same digest). It does not
// publish — call PublishTranscriptUploaded with the result to emit the
// companion event.
func (r *Reporter) UploadTranscripts(ctx context.Context, store blobstore.Store, sessionID string, files []cost.SessionFile) (TranscriptResult, error) {
	res := TranscriptResult{SessionID: sessionID}
	for _, f := range files {
		up, err := blobstore.UploadFile(ctx, store, f.Path)
		if err != nil {
			return res, fmt.Errorf("reporting: upload %s: %w", filepath.Base(f.Path), err)
		}
		res.Files = append(res.Files, TranscriptFile{
			Path:      f.Path,
			SessionID: f.SessionID,
			Digest:    up.Digest.String(),
			URI:       up.URI,
			Bytes:     up.Size,
			Existed:   up.Existed,
		})
	}
	return res, nil
}

// PublishTranscriptUploaded emits the companion
// ape.evt.<user>.<project>.session.<sid>.transcript-uploaded event carrying
// the uploaded blobs' digest map (keyed by file base name) so consumers
// learn about the blobs without polling the store.
func (r *Reporter) PublishTranscriptUploaded(sessionID string, result TranscriptResult) error {
	blobs := make(map[string]eventing.TranscriptBlob, len(result.Files))
	for _, f := range result.Files {
		blobs[filepath.Base(f.Path)] = eventing.TranscriptBlob{
			SessionID: f.SessionID,
			Digest:    f.Digest,
			URI:       f.URI,
			Bytes:     f.Bytes,
		}
	}
	payload, err := json.Marshal(blobs)
	if err != nil {
		return fmt.Errorf("reporting: marshal transcript blobs: %w", err)
	}
	return r.Event(sessionID, EventTranscriptUploaded, payload)
}
