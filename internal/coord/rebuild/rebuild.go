// Package rebuild is APEX v1.5's rebuild protocol (stage 0, step 8;
// proposal §4.4): after a cluster failure the supervisor re-declares the
// layout and writes a new epoch; each ape replays what it alone wrote; each
// lease is re-claimed by token, the highest revision of the previous epoch
// winning; an ape without a token pauses; a token at or above the new epoch
// halts the rebuild for a person.
package rebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
)

var (
	// ErrHalt: a re-claim presented an epoch at or above the new one; the
	// supervisor lost its state, and a person decides (D567).
	ErrHalt = errors.New("rebuild: halted for a person")
	// ErrNoToken: this ape holds no token for the lease; it pauses, and a
	// person decides.
	ErrNoToken = errors.New("rebuild: no token to re-claim with; pausing for a person")
	// ErrOutbid: another ape presented a higher revision.
	ErrOutbid = errors.New("rebuild: another holder presented a higher revision")
	// ErrWindowClosed: the re-claim window of this epoch has closed; an
	// ape back late re-claims nothing, and its old token is stale.
	ErrWindowClosed = errors.New("rebuild: the re-claim window is closed")
)

// KV buckets and Object Stores of the layout besides leases and slots.
var (
	kvBuckets = []string{"ape-runs", "ape-attempts", "ape-usage", "ape-state", "ape-queue"}
	objStores = []string{"ape-transcripts", "ape-gatelogs"}
)

// Declare re-declares every bucket, stream and Object Store of the layout,
// idempotently (§4.4 step 1).
func Declare(ctx context.Context, js jetstream.JetStream, replicas int) error {
	if err := lease.Declare(ctx, js, replicas); err != nil {
		return err
	}
	if err := slots.Declare(ctx, js, replicas); err != nil {
		return err
	}
	if err := events.Declare(ctx, js, replicas); err != nil {
		return err
	}
	for _, b := range kvBuckets {
		if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b, Replicas: replicas, History: 1}); err != nil {
			return fmt.Errorf("rebuild: declare %s: %w", b, err)
		}
	}
	for _, o := range objStores {
		if _, err := js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: o, Replicas: replicas, Compression: false}); err != nil {
			return fmt.Errorf("rebuild: declare %s: %w", o, err)
		}
	}
	return nil
}

// EpochFile is the supervisor's local copy of the epoch: what it knows
// after a total loss of the cluster.
type EpochFile string

// Read is the last epoch the supervisor wrote, 0 when none.
func (f EpochFile) Read() (uint64, error) {
	data, err := os.ReadFile(string(f))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

func (f EpochFile) write(e uint64) error {
	if err := os.MkdirAll(filepath.Dir(string(f)), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(string(f), []byte(strconv.FormatUint(e, 10)+"\n"))
}

// windowKey is ape-meta's key holding an epoch's re-claim deadline.
func windowKey(epoch uint64) string { return "reclaim-until." + strconv.FormatUint(epoch, 10) }

// bidKey is ape-meta's key holding one holder's re-claim bid on a lease.
func bidKey(epoch uint64, key, holder string) string {
	return "bid." + strconv.FormatUint(epoch, 10) + "." + strings.ReplaceAll(key, ".", "_") + "." + holder
}

// NewEpoch is the supervisor's step: the epoch after the higher of its own
// record and the cluster's, written to both, and the re-claim window opened
// for window.
func NewEpoch(ctx context.Context, js jetstream.JetStream, ls *lease.Store, local EpochFile, window time.Duration) (uint64, error) {
	last, err := local.Read()
	if err != nil {
		return 0, err
	}
	if cur, err := ls.Epoch(ctx); err == nil && cur > last {
		last = cur
	}
	next := last + 1
	if err := ls.SetEpoch(ctx, next); err != nil {
		return 0, err
	}
	if err := local.write(next); err != nil {
		return 0, err
	}
	meta, err := js.KeyValue(ctx, lease.MetaBucket)
	if err != nil {
		return 0, err
	}
	until := time.Now().Add(window).UTC().Format(time.RFC3339Nano)
	if _, err := meta.Put(ctx, windowKey(next), []byte(until)); err != nil {
		return 0, err
	}
	return next, nil
}

// Reclaim re-claims one lease for holder after a rebuild (§4.4 step 4): it
// bids its last-known token from the previous epoch, waits for the window
// to close, and claims only if its revision is the highest bid. It records
// the outcome in book.
func Reclaim(ctx context.Context, js jetstream.JetStream, ls *lease.Store, book *lease.Book, key, holder string, ttl time.Duration) (lease.Lease, error) {
	epoch, err := ls.Epoch(ctx)
	if err != nil {
		return lease.Lease{}, err
	}
	meta, err := js.KeyValue(ctx, lease.MetaBucket)
	if err != nil {
		return lease.Lease{}, err
	}
	until, err := deadline(ctx, meta, epoch)
	if err != nil {
		return lease.Lease{}, err
	}
	if time.Now().After(until) {
		return lease.Lease{}, ErrWindowClosed
	}
	entry, ok, err := book.Last(key)
	if err != nil {
		return lease.Lease{}, err
	}
	if !ok || entry.State != lease.Held {
		return lease.Lease{}, fmt.Errorf("%w: %s", ErrNoToken, key)
	}
	if entry.Token.Epoch >= epoch {
		return lease.Lease{}, fmt.Errorf("%w: %s presents epoch %d, the new epoch is %d", ErrHalt, key, entry.Token.Epoch, epoch)
	}
	bid, err := json.Marshal(entry.Token)
	if err != nil {
		return lease.Lease{}, err
	}
	if _, err := meta.Put(ctx, bidKey(epoch, key, holder), bid); err != nil {
		return lease.Lease{}, err
	}
	select {
	case <-ctx.Done():
		return lease.Lease{}, ctx.Err()
	case <-time.After(time.Until(until)):
	}
	if best, err := highestBid(ctx, meta, epoch, key); err != nil {
		return lease.Lease{}, err
	} else if best != holder {
		return lease.Lease{}, fmt.Errorf("%w: %s goes to %s", ErrOutbid, key, best)
	}
	l, err := ls.Claim(ctx, key, holder, ttl)
	if err != nil {
		return lease.Lease{}, err
	}
	return l, book.Record(key, l, lease.Held)
}

func deadline(ctx context.Context, meta jetstream.KeyValue, epoch uint64) (time.Time, error) {
	e, err := meta.Get(ctx, windowKey(epoch))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w (epoch %d has no re-claim window)", ErrWindowClosed, epoch)
	}
	return time.Parse(time.RFC3339Nano, string(e.Value()))
}

// highestBid is the holder whose bid on key has the highest revision of the
// previous epoch; an exact tie cannot happen (two writes to one key never
// share a revision), so the holder name only orders a malformed store.
func highestBid(ctx context.Context, meta jetstream.KeyValue, epoch uint64, key string) (string, error) {
	prefix := "bid." + strconv.FormatUint(epoch, 10) + "." + strings.ReplaceAll(key, ".", "_") + "."
	lister, err := meta.ListKeys(ctx)
	if err != nil {
		return "", err
	}
	type bid struct {
		holder string
		tok    layout.Token
	}
	var bids []bid
	for k := range lister.Keys() {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		e, err := meta.Get(ctx, k)
		if err != nil {
			continue
		}
		var tok layout.Token
		if json.Unmarshal(e.Value(), &tok) != nil || tok.Epoch != epoch-1 {
			continue
		}
		bids = append(bids, bid{holder: strings.TrimPrefix(k, prefix), tok: tok})
	}
	if len(bids) == 0 {
		return "", errors.New("rebuild: no bids")
	}
	sort.Slice(bids, func(i, j int) bool {
		if bids[i].tok.Revision != bids[j].tok.Revision {
			return bids[i].tok.Revision > bids[j].tok.Revision
		}
		return bids[i].holder < bids[j].holder
	})
	return bids[0].holder, nil
}

// Records are what one ape replays: its own and nobody else's (§4.4 step 2).
type Records struct {
	User, Project, Checkout string
	// Events are its events.jsonl lines (the 90-day tail).
	Events []apexv1.Event
	// Runs are its run manifests, by run id.
	Runs map[string][]byte
	// Attempts are its attempt records, by key suffix (<change>.<n>).
	Attempts map[string][]byte
}

// Replay writes an ape's records back with idempotent keys: events by
// message id (deduplicated), runs and attempts by their ids. Replaying
// twice, or racing a live publish, stores each once.
func Replay(ctx context.Context, js jetstream.JetStream, r Records) error {
	for _, e := range r.Events {
		if _, err := events.PublishProject(ctx, js, r.User, r.Project, r.Checkout, e); err != nil {
			return err
		}
	}
	put := func(bucket, prefix string, recs map[string][]byte) error {
		kv, err := js.KeyValue(ctx, bucket)
		if err != nil {
			return err
		}
		for id, v := range recs {
			if _, err := kv.Put(ctx, prefix+id, v); err != nil {
				return err
			}
		}
		return nil
	}
	if err := put("ape-runs", "run."+r.Project+".", r.Runs); err != nil {
		return err
	}
	return put("ape-attempts", "attempt."+r.Project+".", r.Attempts)
}
