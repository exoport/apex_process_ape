package member

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// The milestone lease's timing: the proposal's defaults (section 3.1,
// OQ13). The lease lives only while a run renews it.
var (
	MilestoneTTL        = 30 * time.Second
	MilestoneRenewEvery = 10 * time.Second
)

// milestones is a member's view of its milestone leases: the cluster's
// store, this checkout's book, and the holder string it claims as.
type milestones struct {
	nc     *nats.Conn
	store  *lease.Store
	book   *lease.Book
	holder string
	m      Membership
}

// openMilestones connects for p's milestone leases. The holder is
// <user>/<checkout>: one person's two checkouts are two holders, so a
// second checkout cannot drive a milestone the first one runs. opts are
// the connection's (a renewer reconnects; a one-shot verb does not).
func openMilestones(ctx context.Context, p *apexv1.Project, opts ...nats.Option) (*milestones, error) {
	m, err := Load(p)
	if errors.Is(err, ErrNotMember) {
		return nil, err
	}
	if err != nil {
		return nil, &apexv1.FailError{Code: apexv1.CodeCoordMembership, Msg: err.Error() +
			" — a person fixes _apex/config.yaml's coordination: block or places the ensemble credential"}
	}
	id, err := natsconn.DecodeIdentity(m.Creds)
	if err != nil {
		return nil, &apexv1.FailError{Code: apexv1.CodeCoordMembership, Msg: err.Error()}
	}
	checkout, err := events.CheckoutID(p.StateDir())
	if err != nil {
		return nil, err
	}
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-milestone", opts...)
	if err != nil {
		_, _ = Down(p.Root, time.Now())
		return nil, &apexv1.FailError{Code: apexv1.CodeCoordNATSUnreachable, Action: apexv1.ActionRetry, Msg: fmt.Sprintf(
			"the milestone lease needs NATS, and it is unreachable (%v) — run this again once NATS is back", err)}
	}
	_ = Up(p.Root)
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	store, err := lease.Open(ctx, js)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("%w — the ensemble's NATS has no lease buckets yet: its supervisor declares them (`ape nats declare --new-epoch`)", err)
	}
	return &milestones{nc: nc, store: store, book: lease.OpenBook(runlog.CoordTokensPath(p.Root)), holder: id.SubjectToken + "/" + checkout, m: m}, nil
}

func (s *milestones) close() { s.nc.Close() }

func (s *milestones) key(ms string) string { return layout.MilestoneKey(s.m.Project, ms) }

func notHolder(msg string) error {
	return &apexv1.FailError{Code: apexv1.CodeNotLeaseHolder, Action: apexv1.ActionPerson, Msg: msg}
}

// claim takes ms's lease for this checkout, or keeps the one it already
// holds (its own live key, adopted at its revision). Held by another, it
// refuses, naming the holder. The token goes to the book.
func (s *milestones) claim(ctx context.Context, ms string) (lease.Lease, error) {
	key := s.key(ms)
	l, err := s.store.Claim(ctx, key, s.holder, MilestoneTTL)
	if errors.Is(err, lease.ErrHeld) {
		cur, tok, herr := s.store.Holder(ctx, key)
		switch {
		case herr != nil:
			return lease.Lease{}, herr
		case cur != s.holder:
			return lease.Lease{}, notHolder(fmt.Sprintf("%s is held by %s — a milestone has one holder; agree a handover with them, or claim another milestone", ms, cur))
		}
		l, err = s.store.Renew(ctx, lease.Lease{Key: key, Holder: s.holder, Token: tok, TTL: MilestoneTTL})
	}
	if err != nil {
		return lease.Lease{}, err
	}
	return l, s.book.Record(key, l, lease.Held)
}

// claimed reports whether this checkout's book holds ms (claimed and not
// released since).
func (s *milestones) claimed(ms string) (bool, error) {
	e, ok, err := s.book.Last(s.key(ms))
	return ok && e.State == lease.Held && e.Holder == s.holder, err
}

// ClaimMilestone is `ape milestone claim <M>`: the person's act that takes
// the milestone for this checkout (section 3.1). The lease then lives
// while a run for M renews it (HoldMilestone); between runs it may lapse,
// and the next run takes it again unless another member claimed it.
func ClaimMilestone(ctx context.Context, p *apexv1.Project, ms string) (lease.Lease, error) {
	s, err := openMilestones(ctx, p, nats.NoReconnect())
	if err != nil {
		return lease.Lease{}, err
	}
	defer s.close()
	return s.claim(ctx, ms)
}

// ReleaseMilestone is `ape milestone release <M>`: the key is deleted at
// its revision when this checkout still holds it, and the book marks M
// released, so no later run takes it again without a new claim.
func ReleaseMilestone(ctx context.Context, p *apexv1.Project, ms string) (held bool, err error) {
	s, err := openMilestones(ctx, p, nats.NoReconnect())
	if err != nil {
		return false, err
	}
	defer s.close()
	key := s.key(ms)
	cur, tok, err := s.store.Holder(ctx, key)
	if err != nil {
		return false, err
	}
	l := lease.Lease{Key: key, Holder: s.holder, Token: tok, TTL: MilestoneTTL}
	if cur == s.holder {
		if err := s.store.Release(ctx, l); err != nil {
			return false, err
		}
		held = true
	}
	return held, s.book.Record(key, l, lease.Released)
}

// HoldMilestone is an apex-loop run's hold on its milestone: it takes the
// lease again (only when this checkout claimed M and has not released it)
// and renews it every MilestoneRenewEvery until stop is called. A renewal
// the cluster refuses (the lease lapsed and moved on) is written to log and
// renewing ends: the run's next change new or start then refuses with
// session.not-lease-holder, at a change boundary.
func HoldMilestone(ctx context.Context, p *apexv1.Project, ms string, log io.Writer) (stop func(), err error) {
	s, err := openMilestones(ctx, p)
	if err != nil {
		return nil, err
	}
	ok, err := s.claimed(ms)
	if err != nil || !ok {
		s.close()
		if err == nil {
			err = notHolder(fmt.Sprintf("this checkout has not claimed %s — a milestone's loop runs on its claim: `ape milestone claim %s`, then start the loop again", ms, ms))
		}
		return nil, err
	}
	l, err := s.claim(ctx, ms)
	if err != nil {
		s.close()
		return nil, err
	}
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(MilestoneRenewEvery)
		defer t.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case <-t.C:
			}
			next, err := s.store.Renew(rctx, l)
			switch {
			case errors.Is(err, lease.ErrStale):
				fmt.Fprintf(log, "ape: the lease on %s was lost (%v): the run's next change new or start refuses\n", ms, err)
				return
			case err != nil:
				continue // a transient error: the next tick tries again within the TTL
			}
			l = next
			_ = s.book.Record(l.Key, l, lease.Held)
		}
	}()
	return func() { cancel(); <-done; s.close() }, nil
}

// requireMilestoneLease is Gate's lease check for a change of ms: this
// checkout holds ms's lease now (a run renewing it, or a fresh claim).
func requireMilestoneLease(ctx context.Context, p *apexv1.Project, op, ms string) error {
	s, err := openMilestones(ctx, p, nats.NoReconnect())
	if err != nil {
		return err
	}
	defer s.close()
	cur, _, err := s.store.Holder(ctx, s.key(ms))
	if err != nil {
		return err
	}
	if cur == s.holder {
		return nil
	}
	// Lapsed between runs, and still this checkout's claim: taken again.
	if ok, err := s.claimed(ms); cur == "" && err == nil && ok {
		_, err := s.claim(ctx, ms)
		return err
	}
	if cur == "" {
		return notHolder(fmt.Sprintf("%s refused: this checkout holds no lease on %s (never claimed, released, or lapsed with no run renewing it) — `ape milestone claim %s`, then run its loop", op, ms, ms))
	}
	return notHolder(fmt.Sprintf("%s refused: %s is held by %s, not this checkout — its changes are theirs to start", op, ms, cur))
}
