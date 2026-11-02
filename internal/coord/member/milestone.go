package member

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
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
	l, _, err := s.claimFresh(ctx, ms)
	return l, err
}

// claimFresh is claim, reporting whether the key was created now (fresh)
// rather than this checkout's live key kept.
func (s *milestones) claimFresh(ctx context.Context, ms string) (lease.Lease, bool, error) {
	key := s.key(ms)
	l, err := s.store.Claim(ctx, key, s.holder, MilestoneTTL)
	fresh := err == nil
	if errors.Is(err, lease.ErrHeld) {
		cur, tok, herr := s.store.Holder(ctx, key)
		switch {
		case herr != nil:
			return lease.Lease{}, false, herr
		case cur != s.holder:
			return lease.Lease{}, false, notHolder(fmt.Sprintf("%s is held by %s — a milestone has one holder; agree a handover with them, or claim another milestone", ms, cur))
		}
		l, err = s.store.Renew(ctx, lease.Lease{Key: key, Holder: s.holder, Token: tok, TTL: MilestoneTTL})
	}
	if err != nil {
		return lease.Lease{}, false, err
	}
	return l, fresh, s.book.Record(key, l, lease.Held)
}

// claimed reports whether this checkout's book holds ms (claimed and not
// released since).
func (s *milestones) claimed(ms string) (bool, error) {
	e, ok, err := s.book.Last(s.key(ms))
	return ok && e.State == lease.Held && e.Holder == s.holder, err
}

// Claim is what `ape milestone claim` did: the lease, and in a project
// integrating through GitHub the milestone's branch (Created: this claim
// pushed it).
type Claim struct {
	Lease   lease.Lease
	Branch  string
	Created bool
	// Record is the claim record's PR into main (section 5.4), sent on a
	// fresh claim in a project integrating through GitHub.
	Record *apexv1.Integration
	// Note says what the claim did to a milestone/<M> the remote already
	// had: a leftover with nothing of its own fast-forwarded to main
	// (D736); "" otherwise.
	Note string
}

// ClaimMilestone is `ape milestone claim <M>`: the person's act that takes
// the milestone for this checkout (section 3.1). The lease then lives
// while a run for M renews it (HoldMilestone); between runs it may lapse,
// and the next run takes it again unless another member claimed it. In a
// project with coordination.github it also prepares milestone/<M>
// (apexv1.PrepareMilestoneBranch, section 5.1), after the lease: only the
// holder cuts the branch.
func ClaimMilestone(ctx context.Context, p *apexv1.Project, ms string) (Claim, error) {
	return ClaimMilestoneAdopt(ctx, p, ms, false)
}

// ClaimMilestoneAdopt is ClaimMilestone; adopt takes an existing remote
// milestone/<M> with commits of its own as it is (D736: `--adopt`, a
// person's decision), which a claim otherwise refuses
// (milestone.branch-exists) unless a claim of M is recorded on main.
func ClaimMilestoneAdopt(ctx context.Context, p *apexv1.Project, ms string, adopt bool) (Claim, error) {
	s, err := openMilestones(ctx, p, nats.NoReconnect())
	if err != nil {
		return Claim{}, err
	}
	defer s.close()
	l, fresh, err := s.claimFresh(ctx, ms)
	if err != nil {
		return Claim{}, err
	}
	branch, created, note, err := p.PrepareMilestoneBranchAdopt(ctx, ms, adopt)
	c := Claim{Lease: l, Branch: branch, Created: created, Note: note}
	if err != nil || !fresh {
		return c, err
	}
	c.Record, err = p.RecordClaim(ctx, ms, s.holder)
	return c, err
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
	before, _, _ := s.book.Last(s.key(ms))
	l, fresh, err := s.claimFresh(ctx, ms)
	if err != nil {
		s.close()
		return nil, err
	}
	// B3 (ANSWER-D751): claimed by this checkout, but the key was gone —
	// it lapsed between runs, free to another member meanwhile. Said, as
	// a lapse during a hold is (retake).
	if fresh && log != nil {
		fmt.Fprintf(log, "ape: %s's lease had lapsed (last renewed by this checkout %s, so free from about %s); claimed again\n",
			ms, before.At.UTC().Format(time.RFC3339), before.At.Add(MilestoneTTL).UTC().Format(time.RFC3339))
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
				retaken, keep := s.retake(rctx, ms, err, log)
				if !keep {
					return
				}
				if retaken != nil {
					l = *retaken
				}
				continue
			case err != nil:
				continue // a transient error: the next tick tries again within the TTL
			}
			l = next
			_ = s.book.Record(l.Key, l, lease.Held)
		}
	}()
	return func() { cancel(); <-done; s.close() }, nil
}

// retake is the run's answer to a renewal refused as stale (FINDING-V15-1A-11):
// a lease that lapsed — a NATS outage longer than its TTL, the 1a run's
// drill — and that nobody holds now, while this checkout's claim stands, is
// taken again, as change new and start already do; held by another member,
// or released, the run stops renewing and says so. A holder that cannot be
// read is tried again next tick (keep, no lease).
func (s *milestones) retake(ctx context.Context, ms string, stale error, log io.Writer) (l *lease.Lease, keep bool) {
	cur, _, err := s.store.Holder(ctx, s.key(ms))
	if err != nil {
		return nil, true
	}
	claimed, err := s.claimed(ms)
	switch {
	case err != nil:
		return nil, true
	case !claimed:
		if e, ok, _ := s.book.Last(s.key(ms)); ok && e.State == lease.Released && e.Holder == s.holder {
			fmt.Fprintf(log, "%s ape: the lease on %s was released by this checkout (the milestone closed): the run stops renewing it\n", time.Now().UTC().Format(time.RFC3339), ms)
			return nil, false
		}
		fmt.Fprintf(log, "ape: the lease on %s was lost (%v) and this checkout no longer claims it: the run's next change new or start refuses\n", ms, stale)
		return nil, false
	case cur != "" && cur != s.holder:
		fmt.Fprintf(log, "ape: the lease on %s was lost (%v) and %s holds it now: the run's next change new or start refuses\n", ms, stale, cur)
		return nil, false
	}
	next, err := s.claim(ctx, ms)
	if err != nil {
		return nil, true // taken by another between the read and the claim shows next tick
	}
	if cur == "" {
		fmt.Fprintf(log, "ape: the lease on %s lapsed (%v) and was taken again: this checkout's claim stands\n", ms, stale)
	}
	// cur is this checkout: another keeper of its own renewed first (a
	// close inside a loop's run): adopted quietly.
	return &next, true
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

// FenceMilestone is apexv1.FenceMilestone (D702): right before ape pushes
// milestone/<M>, this checkout must hold M's lease now — a holder whose
// renewal failed, and whose lease another member took, does not push. A
// project in no ensemble is not checked.
func FenceMilestone(ctx context.Context, p *apexv1.Project, ms string) error {
	s, err := openMilestones(ctx, p, nats.NoReconnect())
	if errors.Is(err, ErrNotMember) {
		return nil
	}
	if err != nil {
		return err
	}
	defer s.close()
	l, err := s.store.LiveHolder(ctx, s.key(ms))
	if err != nil {
		return err
	}
	if l.Holder != s.holder {
		return notHolder(fmt.Sprintf("%s's lease is held by %s, not this checkout, so ape does not push milestone/%s: nothing landed — `ape milestone claim %s` if it is still yours to drive", ms, whoHolds(l), ms, ms))
	}
	fenceLogf("milestone/%s: pushing under %s, held by this checkout (%s), last renewed %s", ms, s.key(ms), tokenText(l.Token), l.At.UTC().Format(time.RFC3339))
	return nil
}

// FenceLog gets one line per fenced push or enqueue (D732): the lease, its
// holder and token, when it was last renewed — so a question about a push
// is read off the run's log. A var so tests can read it.
var FenceLog io.Writer = os.Stderr

func fenceLogf(format string, args ...any) {
	line := fmt.Sprintf("%s ape: fence: "+format, append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...)
	apexv1.NoteFence(line) // B1: the run's result.json keeps it too
	if FenceLog != nil {
		fmt.Fprintln(FenceLog, line)
	}
}

// whoHolds names a fence's reading of a lease that is not this checkout's.
func whoHolds(l lease.Live) string {
	switch {
	case l.Expired:
		return "nobody (its at+ttl has passed, with a clock skew margin of up to " + lease.ExpirySkew.String() + ", though the server still has it)"
	case l.Holder == "":
		return "nobody (it lapsed)"
	}
	return l.Holder
}

func tokenText(t layout.Token) string {
	return fmt.Sprintf("epoch %d, revision %d", t.Epoch, t.Revision)
}

// HoldClose is a coordinated close's hold on the project's close lease,
// close.<project> (D709): taken before the close fetches main, renewed
// while it gates on the merge, enqueues and waits for the merge, and
// released by the returned func. Another checkout's close holding it is
// coord.close-busy (retry); this checkout's own live key (a close run
// again) is kept. Nothing for a project in no ensemble.
func HoldClose(ctx context.Context, p *apexv1.Project, log io.Writer, onLost func()) (release func(), err error) {
	s, err := openMilestones(ctx, p)
	if errors.Is(err, ErrNotMember) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	key := layout.CloseKey(s.m.Project)
	l, err := s.store.Claim(ctx, key, s.holder, MilestoneTTL)
	if errors.Is(err, lease.ErrHeld) {
		cur, tok, herr := s.store.Holder(ctx, key)
		switch {
		case herr != nil:
			err = herr
		case cur != s.holder:
			s.close()
			return nil, &apexv1.FailError{Code: apexv1.CodeCloseBusy, Action: apexv1.ActionRetry, Msg: fmt.Sprintf(
				"another close of this project is in progress (%s holds %s): closes take main in, gate and merge one at a time — close again once it is done", cur, key)}
		default:
			l, err = s.store.Renew(ctx, lease.Lease{Key: key, Holder: s.holder, Token: tok, TTL: MilestoneTTL})
		}
	}
	if err != nil {
		s.close()
		return nil, err
	}
	h := &closeHold{s: s, key: key, l: l}
	activeClose.Store(h)
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
			if !h.renew(rctx, log) {
				if onLost != nil {
					onLost()
				}
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
		activeClose.CompareAndSwap(h, nil)
		h.mu.Lock()
		_ = s.store.Release(context.WithoutCancel(ctx), h.l)
		h.mu.Unlock()
		s.close()
	}, nil
}

// closeHold is a running close's hold on close.<project> (D709): the lease,
// and how many times it was taken again after lapsing (FINDING-V15-1A-11,
// research's close-lease finding). One close runs per process.
type closeHold struct {
	s       *milestones
	key     string
	mu      sync.Mutex
	l       lease.Lease
	retakes int
}

// activeClose is this process's close hold, nil outside a close.
var activeClose atomic.Pointer[closeHold]

// renew is one tick of the close lease's keeper: renewed; or, refused as
// stale, taken again when nobody holds it (counted: the close re-checks
// main before it enqueues) or adopted when this checkout does; false when
// another checkout holds it — the close is then cancelled before it
// enqueues anything.
func (h *closeHold) renew(ctx context.Context, log io.Writer) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	next, err := h.s.store.Renew(ctx, h.l)
	switch {
	case err == nil:
		h.l = next
		return true
	case !errors.Is(err, lease.ErrStale):
		return true // transient: the next tick tries again within the TTL
	}
	cur, tok, herr := h.s.store.Holder(ctx, h.key)
	switch {
	case herr != nil:
		return true
	case cur == "":
		l, cerr := h.s.store.Claim(ctx, h.key, h.s.holder, MilestoneTTL)
		if cerr != nil {
			return true // taken by another between the read and the claim shows next tick
		}
		h.l = l
		h.retakes++
		fmt.Fprintf(log, "ape: the close lease %s lapsed (%v) and was taken again: the close checks main again before it enqueues\n", h.key, err)
		return true
	case cur == h.s.holder:
		if l, rerr := h.s.store.Renew(ctx, lease.Lease{Key: h.key, Holder: h.s.holder, Token: tok, TTL: MilestoneTTL}); rerr == nil {
			h.l = l
		}
		return true
	}
	fmt.Fprintf(log, "ape: the close lease %s was lost (%v) and %s holds it now: this close stops before it enqueues anything\n", h.key, err, cur)
	return false
}

// FenceClose is apexv1.FenceClose: right before a close enqueues its PR,
// this close must hold close.<project> now; it returns how many times the
// lease was taken again after lapsing, so the close re-checks main when
// that changed. Outside a close (no hold, a project in no ensemble),
// nothing is checked.
func FenceClose(ctx context.Context, _ *apexv1.Project) (retakes int, err error) {
	h := activeClose.Load()
	if h == nil {
		return 0, nil
	}
	return h.fence(ctx)
}

// fence is FenceClose for this hold.
func (h *closeHold) fence(ctx context.Context) (retakes int, err error) {
	l, err := h.s.store.LiveHolder(ctx, h.key)
	if err != nil {
		return 0, &apexv1.FailError{Code: apexv1.CodeCloseLost, Action: apexv1.ActionRetry, Msg: fmt.Sprintf("the close lease %s could not be read before the enqueue (%v): nothing was enqueued — close again", h.key, err)}
	}
	if l.Holder != h.s.holder {
		return 0, &apexv1.FailError{Code: apexv1.CodeCloseLost, Action: apexv1.ActionRetry, Msg: fmt.Sprintf(
			"the close lease %s is held by %s, not this close, so its PR was not enqueued: closes merge one at a time onto the main they gated (D709) — close again once that close is done", h.key, whoHolds(l))}
	}
	fenceLogf("close PR: enqueueing under %s, held by this checkout (%s), last renewed %s", h.key, tokenText(l.Token), l.At.UTC().Format(time.RFC3339))
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.retakes, nil
}
