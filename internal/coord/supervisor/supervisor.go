// Package supervisor is APEX v1.5's supervisor (stage 0, step 6): two or
// more on different hosts, one active by the `sup.leader` lease (OQ9). The
// leader starts an ensemble's coordinator when none answers, verifies a
// report that a coordinator is unhealthy before it acts on it, replaces a
// confirmed-dead one, and gives up after a restart budget (proposal §3.3,
// OQ13).
package supervisor

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

	"github.com/exoport/apex_process_ape/internal/coord/coordinator"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
)

// Owner is the <user> token supervisors publish under (reserved, R1).
const Owner = "sup"

// Handle is a started coordinator.
type Handle interface {
	// Stop ends the coordinator; for a process, a kill.
	Stop()
}

// Starter starts a coordinator for an ensemble: a process on this host in
// production, a goroutine in the tests.
type Starter func(ctx context.Context, ensemble string) (Handle, error)

// Config is one supervisor.
type Config struct {
	ID    string
	Start Starter
	// TTL and Renew time sup.leader; HealthDeadline bounds the ping; the
	// budget is RestartBudget starts per RestartWindow per ensemble.
	TTL, Renew, HealthDeadline time.Duration
	RestartBudget              int
	RestartWindow              time.Duration
	// Verify is the health check of §3.3. Off, a single report kills a
	// coordinator: the v1.4-shaped control the drills compare with.
	Verify bool
	// HeartbeatEvery is how often the leader sends each held conductor
	// lease a heartbeat event it must acknowledge (§3.1 requirement 2);
	// zero sends none.
	HeartbeatEvery time.Duration
	// Logf, when set, gets one line for what the supervisor rides out
	// (a NATS outage) or does unasked (a coordinator restarted).
	Logf func(format string, args ...any)
}

func (c *Config) defaults() {
	if c.TTL == 0 {
		c.TTL, c.Renew = layout.LeaseTTL, layout.LeaseRenew
	}
	if c.HealthDeadline == 0 {
		c.HealthDeadline = layout.HealthDeadline
	}
	if c.RestartBudget == 0 {
		c.RestartBudget, c.RestartWindow = layout.RestartBudget, layout.RestartWindow
	}
}

// EnsureRequest is ape.sup.ensure's body.
type EnsureRequest struct {
	Ensemble string `json:"ensemble"`
}

// HealthReport is ape.sup.health.report's body: a developer's ape whose
// request to the coordinator timed out.
//
//nolint:tagliatelle // ape's NATS payloads are snake_case (events.md)
type HealthReport struct {
	Ensemble string    `json:"ensemble"`
	Reporter string    `json:"reporter"`
	Subject  string    `json:"subject"`
	SentAt   time.Time `json:"sent_at"`
	What     string    `json:"what"`
}

// Verdict is the supervisor's answer to a health report.
type Verdict struct {
	// Action: "replaced" (confirmed and replaced), "unconfirmed" (the
	// coordinator looks healthy: the reporter's link is suspect), or
	// "gave-up" (confirmed, but the restart budget is spent).
	Action  string `json:"action"`
	Message string `json:"message"`
	// Checks are the verification's results (lease fresh, ping answered).
	Checks map[string]bool `json:"checks,omitempty"`
}

// Supervisor is a running supervisor.
type Supervisor struct {
	cfg    Config
	nc     *nats.Conn
	js     jetstream.JetStream
	leases *lease.Store

	mu       sync.Mutex
	handles  map[string]Handle
	starts   map[string][]time.Time
	isLeader bool
}

// Run contends for sup.leader and, while it holds it, serves ape.sup.*.
// A standby keeps trying to claim; it takes over when the leader's lease
// expires.
func Run(ctx context.Context, nc *nats.Conn, cfg Config) error {
	cfg.defaults()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	ls, err := lease.Open(ctx, js)
	if err != nil {
		return err
	}
	s := &Supervisor{cfg: cfg, nc: nc, js: js, leases: ls, handles: map[string]Handle{}, starts: map[string][]time.Time{}}
	defer s.stopAll()

	t := time.NewTicker(cfg.Renew)
	defer t.Stop()
	var lead leadership
	lastBeat := time.Time{}
	for {
		s.contend(ctx, &lead)
		if lead.svc != nil {
			s.keepAlive() //nolint:contextcheck // its two reads are bounded by HealthDeadline, as a health report's are
		}
		if lead.svc != nil && cfg.HeartbeatEvery > 0 && time.Since(lastBeat) >= cfg.HeartbeatEvery {
			s.heartbeat(ctx)
			lastBeat = time.Now()
		}
		select {
		case <-ctx.Done():
			if lead.svc != nil {
				_ = lead.svc.Stop()
				_ = ls.Release(context.WithoutCancel(ctx), lead.held)
			}
			return nil
		case <-t.C:
		}
	}
}

// leadership is what a supervisor holds while it leads.
type leadership struct {
	held lease.Lease
	svc  micro.Service
}

// contend is one tick: a standby tries to claim sup.leader and serve; the
// leader renews, and stops serving when the renewal fails (the lease
// expired or was fenced off). A claim or serve that fails for any other
// reason than another's lead — NATS unreachable, a timed-out read — is
// tried again next tick (FINDING-V15-1A-10): before, it ended the
// supervisor, and its coordinators with it, in the 1a run's outage.
func (s *Supervisor) contend(ctx context.Context, lead *leadership) {
	if lead.svc != nil {
		l, err := s.leases.Renew(ctx, lead.held)
		if err == nil {
			lead.held = l
			return
		}
		_ = lead.svc.Stop()
		lead.svc = nil
		s.setLeader(false)
		s.logf("supervisor %s: lost sup.leader (%v); contending again", s.cfg.ID, err)
		return
	}
	l, err := s.leases.Claim(ctx, layout.SupLeaderKey, s.cfg.ID, s.cfg.TTL)
	if errors.Is(err, lease.ErrHeld) {
		return
	}
	if err != nil {
		s.logf("supervisor %s: cannot claim sup.leader (%v); trying again", s.cfg.ID, err)
		return
	}
	svc, err := s.serve()
	if err != nil {
		_ = s.leases.Release(ctx, l)
		s.logf("supervisor %s: cannot serve ape.sup.* (%v); trying again", s.cfg.ID, err)
		return
	}
	lead.held, lead.svc = l, svc
	s.setLeader(true)
	s.logf("supervisor %s: leads", s.cfg.ID)
}

// keepAlive is the leader's tick for each ensemble whose coordinator it
// started (FINDING-V15-1A-10): one whose lease is stale and whose ping goes
// unanswered — a health report's own two checks — is replaced, within the
// restart budget. A coordinator that lost its lease in a NATS outage exits;
// once the leader leads again (NATS is back), it is started again without
// anyone asking. A standby does nothing: its NATS may be the one down.
func (s *Supervisor) keepAlive() {
	s.mu.Lock()
	ensembles := make([]string, 0, len(s.handles))
	for ens := range s.handles {
		// A coordinator started within its lease TTL may not have claimed
		// its lease yet: not judged until it had the time.
		if st := s.starts[ens]; len(st) > 0 && time.Since(st[len(st)-1]) < s.cfg.TTL {
			continue
		}
		ensembles = append(ensembles, ens)
	}
	s.mu.Unlock()
	for _, ens := range ensembles {
		if s.leaseFresh(ens) || s.ping(ens) {
			continue
		}
		if err := s.replace(ens); err != nil {
			s.logf("supervisor %s: %s's coordinator is gone and was not restarted: %v", s.cfg.ID, ens, err)
			continue
		}
		s.logf("supervisor %s: %s's coordinator was gone (lease stale, ping unanswered); restarted", s.cfg.ID, ens)
	}
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

// Leader reports whether this supervisor leads (for tests and status).
func (s *Supervisor) Leader() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isLeader
}

func (s *Supervisor) setLeader(v bool) {
	s.mu.Lock()
	s.isLeader = v
	s.mu.Unlock()
}

func (s *Supervisor) serve() (micro.Service, error) {
	svc, err := micro.AddService(s.nc, micro.Config{Name: "ape-sup", Version: "0.0.0", Description: "APEX v1.5 supervisor " + s.cfg.ID})
	if err != nil {
		return nil, err
	}
	grp := svc.AddGroup("ape.sup")
	for name, h := range map[string]micro.HandlerFunc{
		"ensure":        s.ensure,
		"health.report": s.health,
		"status":        func(r micro.Request) { _ = r.RespondJSON(map[string]string{"id": s.cfg.ID, "role": "leader"}) },
	} {
		// An endpoint's name may not hold a dot; its subject does.
		if err := grp.AddEndpoint(strings.ReplaceAll(name, ".", "-"), h, micro.WithEndpointSubject(name)); err != nil {
			_ = svc.Stop()
			return nil, err
		}
	}
	return svc, nil
}

func (s *Supervisor) ensure(r micro.Request) {
	var req EnsureRequest
	if err := json.Unmarshal(r.Data(), &req); err != nil || req.Ensemble == "" {
		_ = r.Error("400", "bad ensure request", nil)
		return
	}
	if s.ping(req.Ensemble) {
		_ = r.RespondJSON(map[string]string{"action": "running"})
		return
	}
	if err := s.start(req.Ensemble); err != nil {
		_ = r.Error("503", err.Error(), nil)
		return
	}
	_ = r.RespondJSON(map[string]string{"action": "started"})
}

func (s *Supervisor) health(r micro.Request) {
	var rep HealthReport
	if err := json.Unmarshal(r.Data(), &rep); err != nil || rep.Ensemble == "" {
		_ = r.Error("400", "bad health report", nil)
		return
	}
	_ = r.RespondJSON(s.judge(rep))
}

// judge is §3.3: verify before acting on a report.
func (s *Supervisor) judge(rep HealthReport) Verdict {
	checks := map[string]bool{}
	if s.cfg.Verify {
		checks["lease_fresh"] = s.leaseFresh(rep.Ensemble)
		checks["ping_answered"] = s.ping(rep.Ensemble)
		if checks["lease_fresh"] && checks["ping_answered"] {
			s.publish(rep.Ensemble, "health-unconfirmed", rep)
			return Verdict{
				Action: "unconfirmed", Checks: checks,
				Message: "the coordinator answers and renews its lease: your link to it looks unhealthy",
			}
		}
	}
	if err := s.replace(rep.Ensemble); err != nil {
		return Verdict{Action: "gave-up", Checks: checks, Message: err.Error()}
	}
	return Verdict{Action: "replaced", Checks: checks, Message: "the coordinator was replaced"}
}

// leaseFresh: coord.<ens> is held, and was written within its TTL.
func (s *Supervisor) leaseFresh(ens string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.HealthDeadline)
	defer cancel()
	kv, err := s.js.KeyValue(ctx, lease.LeasesBucket)
	if err != nil {
		return false
	}
	e, err := kv.Get(ctx, layout.CoordKey(ens))
	if err != nil {
		return false
	}
	return time.Since(e.Created()) < s.cfg.TTL
}

// ping asks the coordinator for its status within the deadline.
func (s *Supervisor) ping(ens string) bool {
	m, err := s.nc.Request("ape.coord."+ens+".status", nil, s.cfg.HealthDeadline)
	if err != nil {
		return false
	}
	var st coordinator.Status
	return json.Unmarshal(m.Data, &st) == nil && st.ID != ""
}

// replace revokes the coordinator's lease (fencing makes the old one
// harmless), stops what this supervisor started, and starts another,
// within the restart budget.
func (s *Supervisor) replace(ens string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.HealthDeadline)
	defer cancel()
	if kv, err := s.js.KeyValue(ctx, lease.LeasesBucket); err == nil {
		_ = kv.Delete(ctx, layout.CoordKey(ens))
	}
	s.mu.Lock()
	if h := s.handles[ens]; h != nil {
		h.Stop()
		delete(s.handles, ens)
	}
	s.mu.Unlock()
	if err := s.start(ens); err != nil {
		return err
	}
	s.publish(ens, "coordinator-replaced", map[string]string{"ensemble": ens, "by": s.cfg.ID})
	return nil
}

func (s *Supervisor) start(ens string) error {
	s.mu.Lock()
	now := time.Now()
	var recent []time.Time
	for _, t := range s.starts[ens] {
		if now.Sub(t) < s.cfg.RestartWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= s.cfg.RestartBudget {
		s.starts[ens] = recent
		s.mu.Unlock()
		return fmt.Errorf("supervisor: %s's coordinator was started %d times in %s; giving up — a person looks", ens, len(recent), s.cfg.RestartWindow)
	}
	s.starts[ens] = append(recent, now)
	s.mu.Unlock()
	h, err := s.cfg.Start(context.Background(), ens)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.handles[ens] = h
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ens, h := range s.handles {
		h.Stop()
		delete(s.handles, ens)
	}
}

func (s *Supervisor) publish(ens, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subj := events.Subject(coordinator.Owner, ens, events.KindCoord, s.cfg.ID, event)
	_, _ = events.Publish(ctx, s.js, subj, s.cfg.ID+"/"+event+"/"+time.Now().UTC().Format(time.RFC3339Nano), data)
}

// heartbeat publishes a heartbeat to every held conductor lease: kind
// coord, on the lease's ensemble, its <id> the lease key (S-1). The
// conductor receives it like any event and must acknowledge it, so a
// session alive but silent stops renewing even when nothing else happens.
func (s *Supervisor) heartbeat(ctx context.Context) {
	kv, err := s.js.KeyValue(ctx, lease.LeasesBucket)
	if err != nil {
		return
	}
	keys, err := kv.ListKeys(ctx)
	if err != nil {
		return
	}
	for key := range keys.Keys() {
		if !strings.HasPrefix(key, "conductor.") {
			continue
		}
		ens := key[strings.LastIndex(key, ".")+1:]
		data, err := json.Marshal(map[string]string{"lease": key, "by": s.cfg.ID})
		if err != nil {
			continue
		}
		subj := events.Subject(coordinator.Owner, ens, events.KindCoord, key, "heartbeat")
		_, _ = events.Publish(ctx, s.js, subj, s.cfg.ID+"/heartbeat/"+key+"/"+time.Now().UTC().Format(time.RFC3339Nano), data)
	}
}
