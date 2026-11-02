// Package lease is APEX v1.5's leases (stage 0, step 3): a lease is a key
// in the ape-leases bucket, claimed by create, renewed by an update at the
// last revision, released by a delete at the last revision, and expired by
// the key's TTL. Every write a lease protects carries its token, the
// cluster's epoch and the key's revision, and a write with a stale token is
// refused (proposal §3.1, §4.4; docs/reference/coordination.md).
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/coord/kvttl"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
)

// Bucket names, from the layout.
const (
	LeasesBucket = "ape-leases"
	MetaBucket   = "ape-meta"
)

var (
	// ErrHeld: the lease is held by someone else (claim refused).
	ErrHeld = errors.New("lease: held")
	// ErrStale: the token is not the lease's current one, from an older
	// epoch, an older revision, or a lease that expired or moved on.
	ErrStale = errors.New("lease: stale token")
	// ErrNoEpoch: ape-meta has no epoch yet; the supervisor writes it.
	ErrNoEpoch = errors.New("lease: no epoch in ape-meta")
)

// Value is what a lease key holds.
type Value struct {
	Holder string    `json:"holder"`
	At     time.Time `json:"at"`
	// TTLSecs is the lease's TTL when it was written (FINDING-V15-1A-11,
	// research's defence in depth): a fence treats now > At+TTL−ExpirySkew
	// as not held, whatever the server still has. Absent (a value written
	// before), no client-side check.
	TTLSecs float64 `json:"ttl_secs,omitempty"` //nolint:tagliatelle // ape's NATS payloads are snake_case (events.md)
}

// ExpirySkew is the margin a fence gives clocks that disagree: a lease
// whose At+TTL is less than the margin away counts as already expired.
// The margin is ExpirySkew, at most a fifth of the lease's TTL (skewFor):
// 2 s against the milestone and close leases' 30 s TTL. It matters once
// members and the cluster run on other hosts (stage 2), where a member's
// clock may run behind the one that wrote At.
var ExpirySkew = 2 * time.Second

// skewFor is the expiry margin for a lease of ttl.
func skewFor(ttl time.Duration) time.Duration {
	return min(ExpirySkew, ttl/5)
}

// Lease is a held lease: its key, holder and current token.
type Lease struct {
	Key    string
	Holder string
	Token  layout.Token
	TTL    time.Duration
}

// Store is the leases of one cluster.
type Store struct {
	js     jetstream.JetStream
	leases jetstream.KeyValue
	meta   jetstream.KeyValue
	now    func() time.Time
}

// Declare creates (or updates to the layout) ape-leases and ape-meta,
// idempotently: the bucket a rebuild re-declares (§4.4 step 1). replicas
// is 3 on a team's cluster, 1 on a single server.
func Declare(ctx context.Context, js jetstream.JetStream, replicas int) error {
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: LeasesBucket, Replicas: replicas, History: 1,
		// Per-key TTLs, and a marker when one expires, so a watcher sees
		// the expiry (lease-expired).
		LimitMarkerTTL: time.Minute,
	}); err != nil {
		return fmt.Errorf("lease: declare %s: %w", LeasesBucket, err)
	}
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: MetaBucket, Replicas: replicas, History: 1,
	}); err != nil {
		return fmt.Errorf("lease: declare %s: %w", MetaBucket, err)
	}
	return nil
}

// Open binds the leases of the cluster js talks to. The buckets must exist
// (Declare).
func Open(ctx context.Context, js jetstream.JetStream) (*Store, error) {
	l, err := js.KeyValue(ctx, LeasesBucket)
	if err != nil {
		return nil, fmt.Errorf("lease: bind %s: %w", LeasesBucket, err)
	}
	m, err := js.KeyValue(ctx, MetaBucket)
	if err != nil {
		return nil, fmt.Errorf("lease: bind %s: %w", MetaBucket, err)
	}
	return &Store{js: js, leases: l, meta: m, now: time.Now}, nil
}

// Epoch is the cluster's current epoch.
func (s *Store) Epoch(ctx context.Context) (uint64, error) {
	e, err := s.meta.Get(ctx, layout.EpochKey)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return 0, ErrNoEpoch
	}
	if err != nil {
		return 0, fmt.Errorf("lease: read epoch: %w", err)
	}
	n, err := strconv.ParseUint(string(e.Value()), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("lease: epoch %q: %w", e.Value(), err)
	}
	return n, nil
}

// SetEpoch writes the epoch: the supervisor's act at a rebuild (§4.4 step
// 1), one higher than the last. It refuses to go backwards or stand still.
func (s *Store) SetEpoch(ctx context.Context, epoch uint64) error {
	cur, err := s.Epoch(ctx)
	switch {
	case errors.Is(err, ErrNoEpoch):
	case err != nil:
		return err
	case epoch <= cur:
		return fmt.Errorf("lease: epoch %d is not above the current %d", epoch, cur)
	}
	_, err = s.meta.Put(ctx, layout.EpochKey, []byte(strconv.FormatUint(epoch, 10)))
	return err
}

// Claim takes key for holder with a TTL. It fails with ErrHeld while
// someone holds it (an unexpired, unreleased key).
func (s *Store) Claim(ctx context.Context, key, holder string, ttl time.Duration) (Lease, error) {
	epoch, err := s.Epoch(ctx)
	if err != nil {
		return Lease{}, err
	}
	val, err := json.Marshal(Value{Holder: holder, At: s.now().UTC(), TTLSecs: ttl.Seconds()})
	if err != nil {
		return Lease{}, err
	}
	rev, err := s.leases.Create(ctx, key, val, jetstream.KeyTTL(ttl))
	if errors.Is(err, jetstream.ErrKeyExists) || kvttl.Conflict(err) {
		return Lease{}, fmt.Errorf("%w: %s", ErrHeld, key)
	}
	if err != nil {
		return Lease{}, fmt.Errorf("lease: claim %s: %w", key, err)
	}
	return Lease{Key: key, Holder: holder, Token: layout.Token{Epoch: epoch, Revision: rev}, TTL: ttl}, nil
}

// Renew extends l's TTL by an update at its revision, and returns the
// lease with its new token. A lease that expired, moved on or belongs to
// an older epoch is ErrStale.
func (s *Store) Renew(ctx context.Context, l Lease) (Lease, error) {
	return s.write(ctx, l, l.Holder)
}

// Handover moves l to another holder at its revision (a recorded
// handover: the old holder keeps l's token as "released at"). It returns
// the new holder's lease.
func (s *Store) Handover(ctx context.Context, l Lease, to string) (Lease, error) {
	return s.write(ctx, l, to)
}

// write is a compare-and-swap at l's revision that resets the key's TTL.
func (s *Store) write(ctx context.Context, l Lease, holder string) (Lease, error) {
	if err := s.Fence(ctx, l.Key, l.Token); err != nil {
		return Lease{}, err
	}
	val, err := json.Marshal(Value{Holder: holder, At: s.now().UTC(), TTLSecs: l.TTL.Seconds()})
	if err != nil {
		return Lease{}, err
	}
	rev, err := kvttl.Update(ctx, s.js, LeasesBucket, l.Key, val, l.Token.Revision, l.TTL)
	if err != nil {
		if kvttl.Conflict(err) {
			return Lease{}, fmt.Errorf("%w: %s moved on", ErrStale, l.Key)
		}
		return Lease{}, fmt.Errorf("lease: write %s: %w", l.Key, err)
	}
	return Lease{Key: l.Key, Holder: holder, Token: layout.Token{Epoch: l.Token.Epoch, Revision: rev}, TTL: l.TTL}, nil
}

// Release deletes l at its revision. A stale token is ErrStale and
// releases nothing.
func (s *Store) Release(ctx context.Context, l Lease) error {
	if err := s.Fence(ctx, l.Key, l.Token); err != nil {
		return err
	}
	if err := s.leases.Delete(ctx, l.Key, jetstream.LastRevision(l.Token.Revision)); err != nil {
		if kvttl.Conflict(err) {
			return fmt.Errorf("%w: %s moved on", ErrStale, l.Key)
		}
		return fmt.Errorf("lease: release %s: %w", l.Key, err)
	}
	return nil
}

// Fence checks that tok is key's current token: the cluster's epoch and the
// key's latest revision. Every write a lease protects calls it first.
func (s *Store) Fence(ctx context.Context, key string, tok layout.Token) error {
	epoch, err := s.Epoch(ctx)
	if err != nil {
		return err
	}
	if tok.Epoch != epoch {
		return fmt.Errorf("%w: %s token %s is of epoch %d, the cluster's is %d", ErrStale, key, tok, tok.Epoch, epoch)
	}
	e, err := s.leases.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("%w: %s is not held (released or expired)", ErrStale, key)
	}
	if err != nil {
		return fmt.Errorf("lease: read %s: %w", key, err)
	}
	if e.Revision() != tok.Revision {
		return fmt.Errorf("%w: %s is at revision %d, the token says %d", ErrStale, key, e.Revision(), tok.Revision)
	}
	return nil
}

// Live is a fence's reading of a lease: who holds it, its token, when it
// was last written, and whether it is past At+TTL−ExpirySkew though the
// server still has it (then Holder is "").
type Live struct {
	Holder  string
	Token   layout.Token
	At      time.Time
	Expired bool
}

// LiveHolder is Holder for a fence (FINDING-V15-1A-11): a lease whose
// At+TTL−ExpirySkew has passed is not held, whatever the server still
// has — a fence fails safe if the server's expiry lags or a member's
// clock is ahead. Claims read Holder, the server's truth.
func (s *Store) LiveHolder(ctx context.Context, key string) (Live, error) {
	e, err := s.leases.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return Live{}, nil
	}
	if err != nil {
		return Live{}, err
	}
	var v Value
	if err := json.Unmarshal(e.Value(), &v); err != nil {
		return Live{}, fmt.Errorf("lease: %s value: %w", key, err)
	}
	epoch, err := s.Epoch(ctx)
	if err != nil {
		return Live{}, err
	}
	l := Live{Holder: v.Holder, Token: layout.Token{Epoch: epoch, Revision: e.Revision()}, At: v.At}
	if ttl := time.Duration(v.TTLSecs * float64(time.Second)); ttl > 0 && s.now().After(v.At.Add(ttl-skewFor(ttl))) {
		l.Holder, l.Expired = "", true
	}
	return l, nil
}

// Holder reads key's holder, "" when nobody holds it.
func (s *Store) Holder(ctx context.Context, key string) (string, layout.Token, error) {
	e, err := s.leases.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return "", layout.Token{}, nil
	}
	if err != nil {
		return "", layout.Token{}, err
	}
	var v Value
	if err := json.Unmarshal(e.Value(), &v); err != nil {
		return "", layout.Token{}, fmt.Errorf("lease: %s value: %w", key, err)
	}
	epoch, err := s.Epoch(ctx)
	if err != nil {
		return "", layout.Token{}, err
	}
	return v.Holder, layout.Token{Epoch: epoch, Revision: e.Revision()}, nil
}
