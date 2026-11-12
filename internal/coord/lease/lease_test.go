package lease

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func store(t *testing.T, urls string, replicas int) *Store {
	t.Helper()
	nc, err := nats.Connect(urls, nats.MaxReconnects(-1))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, Declare(ctx, js, replicas))
	require.NoError(t, Declare(ctx, js, replicas), "declaring again is a no-op")
	s, err := Open(ctx, js)
	require.NoError(t, err)
	_, err = s.Epoch(ctx)
	require.ErrorIs(t, err, ErrNoEpoch)
	require.NoError(t, s.SetEpoch(ctx, 1))
	return s
}

// The lease rules of §3.1, on one server and on an R3 cluster.
func TestLease(t *testing.T) {
	for name, s := range map[string]func(*testing.T) *Store{
		"single": func(t *testing.T) *Store { t.Helper(); return store(t, natstest.RunJetStreamServer(t), 1) },
		"r3":     func(t *testing.T) *Store { t.Helper(); return store(t, natstest.RunCluster(t, 3).URLs(), 3) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Run("claim conflicts while held", func(t *testing.T) { testClaim(t, s(t)) })
			t.Run("renew and release by token", func(t *testing.T) { testRenewRelease(t, s(t)) })
			t.Run("expiry frees the key; renewal keeps it", func(t *testing.T) { testExpiry(t, s(t)) })
			t.Run("handover is recorded by both sides", func(t *testing.T) { testHandover(t, s(t)) })
			t.Run("an older epoch is stale", func(t *testing.T) { testEpoch(t, s(t)) })
			t.Run("an owner key is create-only and handed over by its owner", func(t *testing.T) { testOwner(t, s(t)) })
		})
	}
}

func testClaim(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	key := layout.MilestoneKey("taskforge", "M2")
	a, err := s.Claim(ctx, key, "ann", layout.LeaseTTL)
	require.NoError(t, err)
	require.Equal(t, uint64(1), a.Token.Epoch)
	_, err = s.Claim(ctx, key, "bob", layout.LeaseTTL)
	require.ErrorIs(t, err, ErrHeld)
	h, tok, err := s.Holder(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "ann", h)
	require.Equal(t, a.Token, tok)
}

func testRenewRelease(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	key := layout.StewardKey("atomix")
	a, err := s.Claim(ctx, key, "ann", layout.LeaseTTL)
	require.NoError(t, err)
	a2, err := s.Renew(ctx, a)
	require.NoError(t, err)
	require.True(t, a2.Token.Newer(a.Token))
	_, err = s.Renew(ctx, a)
	require.ErrorIs(t, err, ErrStale, "renewing at the old revision")
	require.ErrorIs(t, s.Release(ctx, a), ErrStale, "releasing with the old token releases nothing")
	h, _, err := s.Holder(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "ann", h)
	require.NoError(t, s.Release(ctx, a2))
	h, _, err = s.Holder(ctx, key)
	require.NoError(t, err)
	require.Empty(t, h)
	require.ErrorIs(t, s.Fence(ctx, key, a2.Token), ErrStale, "a released lease protects nothing")
	b, err := s.Claim(ctx, key, "bob", layout.LeaseTTL)
	require.NoError(t, err, "a released key is claimable")
	require.True(t, b.Token.Newer(a2.Token))
}

func testExpiry(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	const ttl = 2 * time.Second
	kept, err := s.Claim(ctx, layout.ConductorKey("ann", "atomix"), "ann", ttl)
	require.NoError(t, err)
	lapsed, err := s.Claim(ctx, layout.ConductorKey("bob", "atomix"), "bob", ttl)
	require.NoError(t, err)
	deadline := time.Now().Add(3 * ttl)
	for time.Now().Before(deadline) {
		time.Sleep(ttl / 4)
		kept, err = s.Renew(ctx, kept)
		require.NoError(t, err, "a renewed lease outlives its first TTL")
	}
	h, _, err := s.Holder(ctx, lapsed.Key)
	require.NoError(t, err)
	require.Empty(t, h, "an unrenewed lease expired")
	require.ErrorIs(t, s.Fence(ctx, lapsed.Key, lapsed.Token), ErrStale)
	_, err = s.Claim(ctx, lapsed.Key, "bob-2", ttl)
	require.NoError(t, err, "an expired key is claimable")
}

func testHandover(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	key := layout.MilestoneKey("atomix", "M5")
	a, err := s.Claim(ctx, key, "ann", layout.LeaseTTL)
	require.NoError(t, err)
	b, err := s.Handover(ctx, a, "bob")
	require.NoError(t, err)
	require.Equal(t, "bob", b.Holder)
	require.True(t, b.Token.Newer(a.Token), "the new holder's token is newer than the old holder's released-at token")
	require.ErrorIs(t, s.Fence(ctx, key, a.Token), ErrStale, "the old holder's writes are fenced off")
	require.NoError(t, s.Fence(ctx, key, b.Token))
}

func testEpoch(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	key := layout.CoordKey("atomix")
	a, err := s.Claim(ctx, key, "coord", layout.LeaseTTL)
	require.NoError(t, err)
	require.Error(t, s.SetEpoch(ctx, 1), "an epoch never stands still")
	require.NoError(t, s.SetEpoch(ctx, 2))
	require.ErrorIs(t, s.Fence(ctx, key, a.Token), ErrStale, "a token of epoch 1 is stale in epoch 2")
	_, err = s.Renew(ctx, a)
	require.ErrorIs(t, err, ErrStale)
	_, err = s.Claim(ctx, layout.CoordKey("other"), "coord", layout.LeaseTTL)
	require.NoError(t, err)
}

// §4.4 step 4: each side of a handover records it — the old holder
// "released at" its last token, the new one "held" at its new token.
func TestBook_HandoverRecordedByBothSides(t *testing.T) {
	s := store(t, natstest.RunJetStreamServer(t), 1)
	ctx := context.Background()
	annBook := OpenBook(t.TempDir() + "/ann/tokens.json")
	bobBook := OpenBook(t.TempDir() + "/bob/tokens.json")
	key := layout.MilestoneKey("atomix", "M5")
	a, err := s.Claim(ctx, key, "ann", layout.LeaseTTL)
	require.NoError(t, err)
	require.NoError(t, annBook.Record(key, a, Held))
	b, err := s.Handover(ctx, a, "bob")
	require.NoError(t, err)
	require.NoError(t, annBook.Record(key, a, Released))
	require.NoError(t, bobBook.Record(key, b, Held))

	ea, ok, err := annBook.Last(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, Released, ea.State)
	require.Equal(t, a.Token, ea.Token)
	eb, ok, err := bobBook.Last(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, Held, eb.State)
	require.True(t, eb.Token.Newer(ea.Token))
	_, ok, err = bobBook.Last(layout.StewardKey("atomix"))
	require.NoError(t, err)
	require.False(t, ok, "a lease never held has no entry: the ape pauses rather than re-claims")
}

// D876 (c): Own creates once, with no TTL, and reports the owner after;
// HandOwner moves it only from its owner, at its revision.
func testOwner(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	const key = "milestone.p._trunk-owner"
	cur, created, err := s.Own(ctx, key, "ann")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "ann", cur)
	cur, created, err = s.Own(ctx, key, "bob")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "ann", cur, "the second owner finds the first")
	cur, created, err = s.Own(ctx, key, "ann")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "ann", cur)

	require.ErrorIs(t, s.HandOwner(ctx, key, "bob", "carol", "x"), ErrHeld, "only from its owner")
	require.ErrorIs(t, s.HandOwner(ctx, "milestone.p.none", "ann", "bob", "x"), ErrHeld, "no owner to hand over")
	require.NoError(t, s.HandOwner(ctx, key, "ann", "bob", "diegos"))
	got, _, err := s.Holder(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "bob", got)
	keys, err := s.Keys(ctx, "milestone.p.")
	require.NoError(t, err)
	require.Equal(t, []string{key}, keys)
}

// V15-REL-1: the store tells a release from a lapse, on one server and on
// an R3 cluster, and a release made on another connection (another
// process of the same checkout) reads the same: a KV delete's
// KV-Operation DEL, where a TTL expiry leaves the server's MaxAge marker.
func TestReleased_TellsAReleaseFromALapse(t *testing.T) {
	for _, n := range []int{1, 3} {
		t.Run(map[int]string{1: "one-server", 3: "r3"}[n], func(t *testing.T) {
			var urls string
			if n == 1 {
				urls = natstest.RunJetStreamServer(t)
			} else {
				urls = natstest.RunCluster(t, 3).URLs()
			}
			open := func() *Store {
				nc, err := nats.Connect(urls)
				require.NoError(t, err)
				t.Cleanup(nc.Close)
				js, err := jetstream.New(nc)
				require.NoError(t, err)
				s, err := Open(t.Context(), js)
				require.NoError(t, err)
				return s
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			nc, err := nats.Connect(urls)
			require.NoError(t, err)
			defer nc.Close()
			js, err := jetstream.New(nc)
			require.NoError(t, err)
			require.NoError(t, Declare(ctx, js, n))
			s, err := Open(ctx, js)
			require.NoError(t, err)
			require.NoError(t, s.SetEpoch(ctx, 1))
			other := open() // a second process of the same checkout

			rel, err := s.Released(ctx, "milestone.p.never")
			require.NoError(t, err)
			require.False(t, rel, "never written: not released")

			held, err := s.Claim(ctx, "milestone.p.held", "ann/1", time.Minute)
			require.NoError(t, err)
			rel, err = s.Released(ctx, held.Key)
			require.NoError(t, err)
			require.False(t, rel, "held: not released")

			a, err := s.Claim(ctx, "milestone.p.a", "ann/1", time.Minute)
			require.NoError(t, err)
			require.NoError(t, other.Release(ctx, a))
			for _, by := range []*Store{s, other} {
				rel, err = by.Released(ctx, a.Key)
				require.NoError(t, err)
				require.True(t, rel, "released on another connection: released, read from either")
			}

			_, err = s.Claim(ctx, "milestone.p.b", "ann/1", time.Second)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				h, _, err := s.Holder(ctx, "milestone.p.b")
				return err == nil && h == ""
			}, 20*time.Second, 100*time.Millisecond, "expired")
			rel, err = s.Released(ctx, "milestone.p.b")
			require.NoError(t, err)
			require.False(t, rel, "expired by its TTL: a lapse, not a release")

			again, err := s.Claim(ctx, "milestone.p.a", "ann/1", time.Minute)
			require.NoError(t, err, "a released key can be claimed again")
			rel, err = s.Released(ctx, again.Key)
			require.NoError(t, err)
			require.False(t, rel, "and is then held, not released")
		})
	}
}
