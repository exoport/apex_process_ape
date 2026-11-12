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

// Delete writes key's delete (KV-Operation DEL) as a compare-and-swap at
// rev, the subject's last sequence (0: no message on it), as Update does
// for a value: it fails with ErrConflict when anything was written since.
// A KV delete with LastRevision cannot say "at 0" (V15-REL-1).
func Delete(ctx context.Context, js jetstream.JetStream, bucket, key string, rev uint64) error {
	msg := nats.NewMsg("$KV." + bucket + "." + key)
	msg.Header.Set("KV-Operation", "DEL")
	msg.Header.Set(jetstream.ExpectedLastSubjSeqHeader, strconv.FormatUint(rev, 10))
	if _, err := js.PublishMsg(ctx, msg); err != nil {
		if Conflict(err) {
			return ErrConflict
		}
		return err
	}
	return nil
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
