// Package coordinator is APEX v1.5's coordinator core (stage 0, step 6):
// one per ensemble, holding the `coord.<ens>` lease, serving slot requests
// on `ape.coord.<ens>.<verb>` as a NATS-micro service, and recording a slot
// it granted that expired (proposal §3.1). The assessor (§3.2) is a hook
// here; stage 0 runs no model session.
package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nats.go/micro"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
)

// Owner is the <user> token the coordinator publishes under (reserved, R1).
const Owner = "coord"

// Config is one coordinator.
type Config struct {
	Ensemble string
	// ID names this coordinator process (the lease holder and the
	// granting coordinator in slot values).
	ID string
	// TTL and Renew time the coord.<ens> lease (layout's defaults).
	TTL, Renew time.Duration
	// Heavy guards `heavy` slots; zero is slots.DefaultHeavyGuard.
	Heavy slots.HeavyGuard
	// Probe reads the host's free memory and pressure for the guard
	// (-1, -1 when unknown); nil never blocks.
	Probe func() (availBytes int64, psiAvg10 float64)
	// Projects are the ensemble's members, whose milestone leases it
	// watches (the ensemble view, step 9).
	Projects []string
}

// AcquireRequest is ape.coord.<ens>.slot.acquire's body.
//
//nolint:tagliatelle // ape's NATS payloads are snake_case (events.md)
type AcquireRequest struct {
	Host     string `json:"host"`
	Resource string `json:"resource"`
	Capacity int    `json:"capacity"`
	Holder   string `json:"holder"`
	Check    string `json:"check,omitempty"`
	TTLSecs  int    `json:"ttl_secs"`
}

// SlotReply is a granted slot.
//
//nolint:tagliatelle // ape's NATS payloads are snake_case (events.md)
type SlotReply struct {
	Key      string `json:"key"`
	Revision uint64 `json:"revision"`
	TTLSecs  int    `json:"ttl_secs"`
	Granted  string `json:"granted_by"`
}

// ReleaseRequest is ape.coord.<ens>.slot.release's body.
type ReleaseRequest struct {
	Key      string `json:"key"`
	Revision uint64 `json:"revision"`
}

// Status is ape.coord.<ens>.status's reply.
type Status struct {
	Ensemble string `json:"ensemble"`
	ID       string `json:"id"`
	Token    string `json:"token"`
}

// Coordinator is a running coordinator.
type Coordinator struct {
	cfg    Config
	nc     *nats.Conn
	js     jetstream.JetStream
	leases *lease.Store
	slots  *slots.Store

	mu   sync.Mutex
	held lease.Lease
}

// ErrNotLeader: another coordinator holds coord.<ens>.
var ErrNotLeader = errors.New("coordinator: another coordinator holds the ensemble")

// Run claims coord.<ens>, serves until ctx ends or the lease is lost, and
// releases the lease on a clean exit. A process killed without returning
// leaves the lease to expire: the supervisor's verification sees it stale.
func Run(ctx context.Context, nc *nats.Conn, cfg Config) error {
	if cfg.TTL == 0 {
		cfg.TTL, cfg.Renew = layout.LeaseTTL, layout.LeaseRenew
	}
	if cfg.Heavy == (slots.HeavyGuard{}) {
		cfg.Heavy = slots.DefaultHeavyGuard
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	ls, err := lease.Open(ctx, js)
	if err != nil {
		return err
	}
	ss, err := slots.Open(ctx, js)
	if err != nil {
		return err
	}
	l, err := ls.Claim(ctx, layout.CoordKey(cfg.Ensemble), cfg.ID, cfg.TTL)
	if errors.Is(err, lease.ErrHeld) {
		return ErrNotLeader
	}
	if err != nil {
		return err
	}
	c := &Coordinator{cfg: cfg, nc: nc, js: js, leases: ls, slots: ss, held: l}

	svc, err := micro.AddService(nc, micro.Config{Name: "ape-coord", Version: "0.0.0", Description: "APEX v1.5 coordinator, " + cfg.Ensemble})
	if err != nil {
		return err
	}
	defer func() { _ = svc.Stop() }()
	grp := svc.AddGroup("ape.coord." + cfg.Ensemble)
	for name, h := range map[string]micro.HandlerFunc{
		"slot.acquire": c.acquire,
		"slot.release": c.release,
		"status":       c.status,
		"hold.ack":     func(r micro.Request) { _ = r.Respond([]byte(`{"ok":true}`)) },
	} {
		// An endpoint's name may not hold a dot; its subject does.
		if err := grp.AddEndpoint(strings.ReplaceAll(name, ".", "-"), h, micro.WithEndpointSubject(name)); err != nil {
			return err
		}
	}

	wctx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() {
		_ = ss.WatchExpiries(wctx, cfg.ID, func(r slots.Reclaimed) {
			c.publish(wctx, "slot-reclaimed", r.Key, r)
		})
	}()
	go c.watchLeaseExpiries(wctx)

	t := time.NewTicker(cfg.Renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			l := c.held
			c.mu.Unlock()
			_ = ls.Release(context.WithoutCancel(ctx), l)
			return nil
		case <-t.C:
			c.mu.Lock()
			l, err := ls.Renew(ctx, c.held)
			if err == nil {
				c.held = l
			}
			c.mu.Unlock()
			if err != nil {
				return fmt.Errorf("coordinator: lost coord.%s: %w", cfg.Ensemble, err)
			}
		}
	}
}

func (c *Coordinator) acquire(r micro.Request) {
	var req AcquireRequest
	if err := json.Unmarshal(r.Data(), &req); err != nil || req.Host == "" || req.Resource == "" {
		_ = r.Error("400", "bad slot.acquire request", nil)
		return
	}
	capacity := req.Capacity
	if req.Resource == slots.Heavy {
		held := c.heldCount(req.Host, req.Resource, c.cfg.Heavy.Capacity)
		avail, psi := int64(-1), -1.0
		if c.cfg.Probe != nil {
			avail, psi = c.cfg.Probe()
		}
		if c.cfg.Heavy.Room(held, avail, psi) == 0 {
			_ = r.Error("409", "no heavy slot free on "+req.Host+" (capacity, memory or pressure)", nil)
			return
		}
		capacity = c.cfg.Heavy.Capacity
	}
	if capacity <= 0 {
		capacity = 1
	}
	ttl := time.Duration(req.TTLSecs) * time.Second
	if ttl <= 0 {
		ttl = c.cfg.TTL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sl, err := c.slots.Acquire(ctx, req.Host, req.Resource, capacity, slots.Value{Holder: req.Holder, Coordinator: c.cfg.ID, Check: req.Check}, ttl)
	if errors.Is(err, slots.ErrFull) {
		_ = r.Error("409", err.Error(), nil)
		return
	}
	if err != nil {
		_ = r.Error("500", err.Error(), nil)
		return
	}
	c.publish(ctx, "slot-granted", sl.Key, sl.Value)
	_ = r.RespondJSON(SlotReply{Key: sl.Key, Revision: sl.Revision, TTLSecs: int(ttl / time.Second), Granted: c.cfg.ID})
}

func (c *Coordinator) heldCount(host, resource string, capacity int) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	kv, err := c.js.KeyValue(ctx, slots.Bucket)
	if err != nil {
		return 0
	}
	n := 0
	for i := range capacity {
		if _, err := kv.Get(ctx, layout.SlotKey(host, resource, i)); err == nil {
			n++
		}
	}
	return n
}

func (c *Coordinator) release(r micro.Request) {
	var req ReleaseRequest
	if err := json.Unmarshal(r.Data(), &req); err != nil || req.Key == "" {
		_ = r.Error("400", "bad slot.release request", nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.slots.Release(ctx, slots.Slot{Key: req.Key, Revision: req.Revision}); err != nil {
		_ = r.Error("409", err.Error(), nil)
		return
	}
	_ = r.Respond([]byte(`{"ok":true}`))
}

func (c *Coordinator) status(r micro.Request) {
	c.mu.Lock()
	tok := c.held.Token.String()
	c.mu.Unlock()
	_ = r.RespondJSON(Status{Ensemble: c.cfg.Ensemble, ID: c.cfg.ID, Token: tok})
}

func (c *Coordinator) publish(ctx context.Context, event, id string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	subj := events.Subject(Owner, c.cfg.Ensemble, events.KindCoord, id, event)
	_, _ = events.Publish(ctx, c.js, subj, c.cfg.ID+"/"+event+"/"+id+"/"+time.Now().UTC().Format(time.RFC3339Nano), data)
}

// watchLeaseExpiries publishes lease-expired for every lease of the
// ensemble whose TTL ran out (a release is a delete; an expiry is the
// server's purge marker): a conductor that went silent, a milestone whose
// orchestrator stopped renewing. The event names the key (§3.1).
func (c *Coordinator) watchLeaseExpiries(ctx context.Context) {
	kv, err := c.js.KeyValue(ctx, lease.LeasesBucket)
	if err != nil {
		return
	}
	w, err := kv.WatchAll(ctx, jetstream.UpdatesOnly())
	if err != nil {
		return
	}
	defer func() { _ = w.Stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-w.Updates():
			if !ok {
				return
			}
			if e != nil && e.Operation() == jetstream.KeyValuePurge && c.ours(e.Key()) {
				c.publish(ctx, "lease-expired", e.Key(), map[string]string{"key": e.Key()})
			}
		}
	}
}

// ours reports whether a lease key belongs to this coordinator's ensemble.
func (c *Coordinator) ours(key string) bool {
	ens := c.cfg.Ensemble
	switch {
	case key == layout.StewardKey(ens) || key == layout.CoordKey(ens):
		return true
	case strings.HasPrefix(key, "conductor.") && strings.HasSuffix(key, "."+ens):
		return true
	}
	for _, p := range c.cfg.Projects {
		if strings.HasPrefix(key, "milestone."+p+".") {
			return true
		}
	}
	return false
}
