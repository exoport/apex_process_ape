package blobstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
)

// URIRequestSubject is the request/reply subject an offload service answers
// (PLAN-13 D3). The service is out of ape's tree; ape ships this client half
// and the documented contract (docs/reference/blob-offload.md).
const URIRequestSubject = "ape.blob.uri-request"

// UploadRequest is the JSON body ape sends on URIRequestSubject.
type UploadRequest struct {
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	CompressedSize int64  `json:"compressed_size"`
	ContentType    string `json:"content_type"`
	Project        string `json:"project"`
	RunID          string `json:"run_id"`
}

// UploadReply is the offload service's response. Status is "upload" (do an
// HTTPS PUT to URI) or "exists" (dedup short-circuit).
type UploadReply struct {
	Status  string            `json:"status"`
	URI     string            `json:"uri"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
}

// URIOffloadStore implements the URI-request offload flow: a NATS request
// returns an upload URI, then ape performs the HTTPS PUT (presigned-URL
// pattern — S3/GCS/Azure all fit).
type URIOffloadStore struct {
	nc       *nats.Conn
	subject  string
	project  string
	runID    string
	http     *http.Client
	requestT time.Duration
}

// URIOffloadConfig configures a URIOffloadStore.
type URIOffloadConfig struct {
	Subject        string // default URIRequestSubject
	Project        string
	RunID          string
	HTTPClient     *http.Client
	RequestTimeout time.Duration // NATS request timeout (default 10s)
}

// NewURIOffloadStore builds a URI-offload store on nc.
func NewURIOffloadStore(nc *nats.Conn, cfg URIOffloadConfig) *URIOffloadStore {
	s := &URIOffloadStore{
		nc:       nc,
		subject:  cfg.Subject,
		project:  cfg.Project,
		runID:    cfg.RunID,
		http:     cfg.HTTPClient,
		requestT: cfg.RequestTimeout,
	}
	if s.subject == "" {
		s.subject = URIRequestSubject
	}
	if s.http == nil {
		s.http = &http.Client{Timeout: 30 * time.Second}
	}
	if s.requestT == 0 {
		s.requestT = 10 * time.Second
	}
	return s
}

// Put asks the offload service for an upload URI and, unless the blob
// already exists, PUTs the compressed payload over HTTPS.
func (s *URIOffloadStore) Put(ctx context.Context, d Digest, sizeUncompressed, sizeCompressed int64, compressed io.Reader) (uri string, existed bool, err error) {
	body, err := io.ReadAll(compressed)
	if err != nil {
		return "", false, fmt.Errorf("blobstore: read payload: %w", err)
	}
	reqBytes, err := json.Marshal(UploadRequest{
		Digest:         d.String(),
		Size:           sizeUncompressed,
		CompressedSize: sizeCompressed,
		ContentType:    ContentType,
		Project:        s.project,
		RunID:          s.runID,
	})
	if err != nil {
		return "", false, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, s.requestT)
	defer cancel()
	msg, err := s.nc.RequestWithContext(reqCtx, s.subject, reqBytes)
	if err != nil {
		return "", false, fmt.Errorf("blobstore: uri-request: %w", err)
	}
	var reply UploadReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return "", false, fmt.Errorf("blobstore: decode uri reply: %w", err)
	}

	switch reply.Status {
	case "exists":
		return reply.URI, true, nil
	case "upload":
		if err := s.httpPut(ctx, reply, body); err != nil {
			return "", false, err
		}
		return reply.URI, false, nil
	default:
		return "", false, fmt.Errorf("blobstore: unexpected uri-request status %q", reply.Status)
	}
}

func (s *URIOffloadStore) httpPut(ctx context.Context, reply UploadReply, body []byte) error {
	method := reply.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, reply.URI, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("blobstore: build upload request: %w", err)
	}
	req.Header.Set("Content-Type", ContentType)
	for k, v := range reply.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("blobstore: http upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("blobstore: http upload status %d", resp.StatusCode)
	}
	return nil
}
