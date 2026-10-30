// Package slots is APEX v1.5's resource slots (stage 0, step 5): named,
// counted resources on a host — a fixed port, a shared outside service,
// `heavy` (a CPU-bound test run) — each unit a TTL lease in ape-slots
// (proposal §3.1, OQ12, OQ13; docs/reference/coordination.md).
package slots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/kvttl"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// Bucket is the slots' KV bucket.
const Bucket = "ape-slots"

// Heavy is the resource name of a CPU-bound test run.
const Heavy = "heavy"

var (
	// ErrFull: every unit of the resource is held.
	ErrFull = errors.New("slots: every unit is held")
	// ErrLost: the slot expired or moved on before this write.
	ErrLost = errors.New("slots: slot lost")
)

// Value is what a slot key holds. Coordinator names the coordinator that
// granted it, the one that reclaims it (R7).
type Value struct {
	Holder      string    `json:"holder"`
	Coordinator string    `json:"coordinator"`
	Check       string    `json:"check,omitempty"`
	At          time.Time `json:"at"`
}

// Slot is a held unit.
type Slot struct {
	Key      string
	Value    Value
	Revision uint64
	TTL      time.Duration
}

// Declare creates (or updates) ape-slots, with per-key TTLs and expiry
// markers, idempotently.
func Declare(ctx context.Context, js jetstream.JetStream, replicas int) error {
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: Bucket, Replicas: replicas, History: 1, LimitMarkerTTL: time.Minute,
	}); err != nil {
		return fmt.Errorf("slots: declare: %w", err)
	}
	return nil
}

// Store is the slots of one cluster.
type Store struct {
	js jetstream.JetStream
	kv jetstream.KeyValue
}

// Open binds ape-slots (Declare first).
func Open(ctx context.Context, js jetstream.JetStream) (*Store, error) {
	kv, err := js.KeyValue(ctx, Bucket)
	if err != nil {
		return nil, fmt.Errorf("slots: bind: %w", err)
	}
	return &Store{js: js, kv: kv}, nil
}

// Acquire takes the first free unit of resource on host, out of capacity,
// for ttl: the coordinator's slot.acquire. ErrFull when all are held.
func (s *Store) Acquire(ctx context.Context, host, resource string, capacity int, v Value, ttl time.Duration) (Slot, error) {
	if v.At.IsZero() {
		v.At = time.Now().UTC()
	}
	val, err := json.Marshal(v)
	if err != nil {
		return Slot{}, err
	}
	for n := range capacity {
		key := layout.SlotKey(natsconn.SubjectToken(host), resource, n)
		rev, err := s.kv.Create(ctx, key, val, jetstream.KeyTTL(ttl))
		if kvttl.Conflict(err) {
			continue
		}
		if err != nil {
			return Slot{}, fmt.Errorf("slots: acquire %s: %w", key, err)
		}
		return Slot{Key: key, Value: v, Revision: rev, TTL: ttl}, nil
	}
	return Slot{}, fmt.Errorf("%w: %s on %s (%d)", ErrFull, resource, host, capacity)
}

// Renew extends a held slot's TTL (a long check).
func (s *Store) Renew(ctx context.Context, sl Slot) (Slot, error) {
	val, err := json.Marshal(sl.Value)
	if err != nil {
		return Slot{}, err
	}
	rev, err := kvttl.Update(ctx, s.js, Bucket, sl.Key, val, sl.Revision, sl.TTL)
	if err != nil {
		if kvttl.Conflict(err) {
			return Slot{}, fmt.Errorf("%w: %s", ErrLost, sl.Key)
		}
		return Slot{}, err
	}
	sl.Revision = rev
	return sl, nil
}

// Release frees a held slot at its revision.
func (s *Store) Release(ctx context.Context, sl Slot) error {
	if err := s.kv.Delete(ctx, sl.Key, jetstream.LastRevision(sl.Revision)); err != nil {
		if kvttl.Conflict(err) {
			return fmt.Errorf("%w: %s", ErrLost, sl.Key)
		}
		return fmt.Errorf("slots: release %s: %w", sl.Key, err)
	}
	return nil
}

// Reclaimed is a slot whose TTL ran out while it was held.
type Reclaimed struct {
	Key   string
	Value Value
}

// WatchExpiries calls onReclaim for each slot granted by coordinator that
// expired while held — a release is a delete, an expiry is the server's
// purge marker — so that coordinator records slot-reclaimed (R7). It runs
// until ctx ends.
func (s *Store) WatchExpiries(ctx context.Context, coordinator string, onReclaim func(Reclaimed)) error {
	w, err := s.kv.WatchAll(ctx)
	if err != nil {
		return fmt.Errorf("slots: watch: %w", err)
	}
	defer func() { _ = w.Stop() }()
	granted := map[string]Value{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-w.Updates():
			if !ok {
				return nil
			}
			if e == nil {
				continue // the initial values are in
			}
			switch e.Operation() {
			case jetstream.KeyValuePut:
				var v Value
				if json.Unmarshal(e.Value(), &v) == nil && v.Coordinator == coordinator {
					granted[e.Key()] = v
				} else {
					delete(granted, e.Key())
				}
			case jetstream.KeyValueDelete:
				delete(granted, e.Key())
			case jetstream.KeyValuePurge:
				if v, ok := granted[e.Key()]; ok {
					delete(granted, e.Key())
					onReclaim(Reclaimed{Key: e.Key(), Value: v})
				}
			}
		}
	}
}

// Proposal is a slot ape proposes for a check from what v1.4 saw it do
// (OQ12); a person accepts it with the plan.
type Proposal struct {
	Resource string
	Why      string
}

// Propose reads a check's observation: a service seen listening on a fixed
// port proposes that port's slot; containers propose `containers` (the
// host's container engine is shared). In-process listeners on OS-assigned
// ports propose nothing: they cannot collide.
func Propose(o apexv1.Observed) []Proposal {
	var out []Proposal
	seen := map[string]bool{}
	add := func(res, why string) {
		if !seen[res] {
			seen[res] = true
			out = append(out, Proposal{Resource: res, Why: why})
		}
	}
	for _, svc := range o.Services {
		i := strings.LastIndex(svc, ":")
		if i < 0 || i == len(svc)-1 {
			continue
		}
		port := svc[i+1:]
		add("port-"+port, "a service listened on :"+port+" ("+strings.TrimSpace(svc[:i])+")")
	}
	if o.Containers > 0 {
		add("containers", fmt.Sprintf("it started %d container(s)", o.Containers))
	}
	return out
}
