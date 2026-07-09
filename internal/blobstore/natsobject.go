package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// DefaultBucket is the JetStream Object Store bucket transcripts land in.
const DefaultBucket = "ape-transcripts"

// NATSObjectStore stores blobs in a JetStream Object Store bucket, keyed by
// digest (object name "<algo>/<hex>"). Chunked transfer handles the largest
// transcripts comfortably (PLAN-13 dimensioning).
type NATSObjectStore struct {
	obs jetstream.ObjectStore
}

// NewNATSObjectStore binds (creating if absent) the given bucket on nc.
func NewNATSObjectStore(ctx context.Context, nc *nats.Conn, bucket string) (*NATSObjectStore, error) {
	if bucket == "" {
		bucket = DefaultBucket
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("blobstore: jetstream: %w", err)
	}
	obs, err := js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:      bucket,
		Description: "ape run transcript blobs (content-addressed, zstd)",
	})
	if err != nil {
		return nil, fmt.Errorf("blobstore: object store %q: %w", bucket, err)
	}
	return &NATSObjectStore{obs: obs}, nil
}

// Has reports whether the digest is already stored.
func (s *NATSObjectStore) Has(ctx context.Context, d Digest) (bool, error) {
	_, err := s.obs.GetInfo(ctx, d.ObjectName())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, jetstream.ErrObjectNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("blobstore: get info %s: %w", d, err)
}

// Put stores the compressed payload idempotently.
func (s *NATSObjectStore) Put(ctx context.Context, d Digest, _, _ int64, compressed io.Reader) (uri string, existed bool, err error) {
	uri = s.uri(d)
	has, err := s.Has(ctx, d)
	if err != nil {
		return "", false, err
	}
	if has {
		return uri, true, nil
	}
	if _, err := s.obs.Put(ctx, jetstream.ObjectMeta{Name: d.ObjectName()}, compressed); err != nil {
		return "", false, fmt.Errorf("blobstore: put %s: %w", d, err)
	}
	return uri, false, nil
}

// Get returns the decompressed payload for a digest (round-trip / integrity).
func (s *NATSObjectStore) Get(ctx context.Context, d Digest) ([]byte, error) {
	compressed, err := s.obs.GetBytes(ctx, d.ObjectName())
	if err != nil {
		return nil, fmt.Errorf("blobstore: get %s: %w", d, err)
	}
	return Decompress(compressed)
}

func (s *NATSObjectStore) uri(d Digest) string {
	return fmt.Sprintf("nats-object://%s/%s", DefaultBucket, d.ObjectName())
}
