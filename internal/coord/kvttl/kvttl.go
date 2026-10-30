// Package kvttl is the one KV write the JetStream client lacks: an update at
// an expected revision that also resets the key's TTL. Leases and slots
// renew with it (v1.5 stage 0).
package kvttl

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ErrConflict: the key is no longer at the expected revision.
var ErrConflict = errors.New("kvttl: revision moved on")

// Update writes val to bucket's key if its last revision is rev, with a new
// TTL, and returns the new revision. KV Update takes no TTL, so it
// publishes to the bucket's subject with the expected-last-subject-sequence
// and per-message TTL headers (the bucket needs LimitMarkerTTL).
func Update(ctx context.Context, js jetstream.JetStream, bucket, key string, val []byte, rev uint64, ttl time.Duration) (uint64, error) {
	msg := nats.NewMsg("$KV." + bucket + "." + key)
	msg.Data = val
	msg.Header.Set(jetstream.ExpectedLastSubjSeqHeader, strconv.FormatUint(rev, 10))
	msg.Header.Set(jetstream.MsgTTLHeader, ttl.String())
	ack, err := js.PublishMsg(ctx, msg)
	if err != nil {
		if Conflict(err) {
			return 0, ErrConflict
		}
		return 0, err
	}
	return ack.Sequence, nil
}

// Conflict reports a compare-and-swap conflict: 10071, or 10164 on a
// replicated stream, or the KV client's own ErrKeyExists.
func Conflict(err error) bool {
	if errors.Is(err, jetstream.ErrKeyExists) || errors.Is(err, ErrConflict) {
		return true
	}
	var apiErr *jetstream.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence ||
		apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequenceConstant)
}
