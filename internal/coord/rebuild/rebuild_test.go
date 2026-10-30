package rebuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

const window = 1500 * time.Millisecond

func cluster(t *testing.T, url string) (jetstream.JetStream, *lease.Store) {
	t.Helper()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	require.NoError(t, Declare(context.Background(), js, 1))
	require.NoError(t, Declare(context.Background(), js, 1), "re-declaring is a no-op")
	ls, err := lease.Open(context.Background(), js)
	require.NoError(t, err)
	return js, ls
}

// snapshot digests what the cluster holds of the apes' records: every
// event (subject + payload) and every run and attempt, order-free.
func snapshot(t *testing.T, js jetstream.JetStream) (n int, digest string) {
	t.Helper()
	ctx := context.Background()
	var rows []string
	s, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	info, err := s.Info(ctx)
	require.NoError(t, err)
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		m, err := s.GetMsg(ctx, seq)
		if err != nil {
			continue
		}
		rows = append(rows, "evt "+m.Subject+" "+string(m.Data))
	}
	for _, b := range []string{"ape-runs", "ape-attempts"} {
		kv, err := js.KeyValue(ctx, b)
		require.NoError(t, err)
		keys, err := kv.ListKeys(ctx)
		require.NoError(t, err)
		for k := range keys.Keys() {
			e, err := kv.Get(ctx, k)
			require.NoError(t, err)
			rows = append(rows, b+" "+k+" "+string(e.Value()))
		}
	}
	sort.Strings(rows)
	h := sha256.New()
	for _, r := range rows {
		h.Write([]byte(r + "\n"))
	}
	return len(rows), hex.EncodeToString(h.Sum(nil))
}

func records(user, checkout string, base int64) Records {
	exit := 0
	return Records{
		User: user, Project: "taskforge", Checkout: checkout,
		Events: []apexv1.Event{
			{Seq: base + 1, Event: apexv1.EventGateGreen, Milestone: "M2"},
			{Seq: base + 2, Event: apexv1.EventRunEnd, Run: "r-" + user, Exit: &exit},
		},
		Runs:     map[string][]byte{"r-" + user: []byte(`{"cost":1.5}`)},
		Attempts: map[string][]byte{"add-" + user + ".1": []byte(`{"red":true}`)},
	}
}

// Drill 3 (Tier 1): wipe and rebuild. Records come back equal and once;
// the lease goes to the highest revision of the previous epoch; an ape
// with no token pauses; one back late is refused; a supervisor that lost
// its epoch halts the rebuild.
func TestDrill_WipeAndRebuild(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	supLocal := EpochFile(filepath.Join(dir, "sup", "epoch"))
	annBook := lease.OpenBook(filepath.Join(dir, "ann", "tokens.json"))
	bobBook := lease.OpenBook(filepath.Join(dir, "bob", "tokens.json"))
	carlBook := lease.OpenBook(filepath.Join(dir, "carl", "tokens.json"))
	lateBook := lease.OpenBook(filepath.Join(dir, "late", "tokens.json"))

	// Before: epoch 1. ann holds M2, hands it to bob; ann's own book was
	// never told (it says held, at the older revision).
	js1, ls1 := cluster(t, natstest.RunJetStreamServer(t))
	e1, err := NewEpoch(ctx, js1, ls1, supLocal, time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), e1)
	key := layout.MilestoneKey("taskforge", "M2")
	a, err := ls1.Claim(ctx, key, "ann", time.Minute)
	require.NoError(t, err)
	require.NoError(t, annBook.Record(key, a, lease.Held))
	b, err := ls1.Handover(ctx, a, "bob")
	require.NoError(t, err)
	require.NoError(t, bobBook.Record(key, b, lease.Held))
	late, err := ls1.Renew(ctx, b)
	require.NoError(t, err)
	late.Holder = "late"
	require.NoError(t, lateBook.Record(key, late, lease.Held), "an ape that saw the newest revision but sleeps through the rebuild")
	require.NoError(t, Replay(ctx, js1, records("ann", "c-ann", 0)))
	require.NoError(t, Replay(ctx, js1, records("bob", "c-bob", 0)))
	n1, d1 := snapshot(t, js1)

	// The wipe: a new, empty cluster. The supervisor re-declares and writes
	// epoch 2 from its own record.
	js2, ls2 := cluster(t, natstest.RunJetStreamServer(t))
	e2, err := NewEpoch(ctx, js2, ls2, supLocal, window)
	require.NoError(t, err)
	require.Equal(t, uint64(2), e2)

	// Each ape replays what it wrote — twice, as a replay racing itself.
	for range 2 {
		require.NoError(t, Replay(ctx, js2, records("ann", "c-ann", 0)))
		require.NoError(t, Replay(ctx, js2, records("bob", "c-bob", 0)))
	}
	n2, d2 := snapshot(t, js2)
	require.Equal(t, n1, n2, "every record back, each once")
	require.Equal(t, d1, d2, "and the same")

	// Re-claims, concurrently: bob's revision is higher than ann's.
	var wg sync.WaitGroup
	results := map[string]error{}
	var mu sync.Mutex
	for _, ape := range []struct {
		name string
		book *lease.Book
	}{{"ann", annBook}, {"bob", bobBook}, {"carl", carlBook}} {
		wg.Go(func() {
			_, err := Reclaim(ctx, js2, ls2, ape.book, key, ape.name, time.Minute)
			mu.Lock()
			results[ape.name] = err
			mu.Unlock()
		})
	}
	wg.Wait()
	require.NoError(t, results["bob"], "the highest revision of the previous epoch wins")
	require.ErrorIs(t, results["ann"], ErrOutbid)
	require.ErrorIs(t, results["carl"], ErrNoToken, "no token: pause for a person")
	h, tok, err := ls2.Holder(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "bob", h)
	require.Equal(t, uint64(2), tok.Epoch)

	// The one back late, with the highest revision of all: the window has
	// closed, and its epoch-1 token is fenced off.
	_, err = Reclaim(ctx, js2, ls2, lateBook, key, "late", time.Minute)
	require.ErrorIs(t, err, ErrWindowClosed)
	require.ErrorIs(t, ls2.Fence(ctx, key, late.Token), lease.ErrStale)

	// A supervisor that lost its record rebuilds a wiped cluster at epoch
	// 1 again: a re-claim presenting epoch 1 halts for a person.
	js3, ls3 := cluster(t, natstest.RunJetStreamServer(t))
	e3, err := NewEpoch(ctx, js3, ls3, EpochFile(filepath.Join(dir, "lost", "epoch")), window)
	require.NoError(t, err)
	require.Equal(t, uint64(1), e3)
	b2 := lease.Lease{Key: key, Holder: "bob", Token: tok}
	require.NoError(t, bobBook.Record(key, b2, lease.Held))
	_, err = Reclaim(ctx, js3, ls3, bobBook, key, "bob", time.Minute)
	require.ErrorIs(t, err, ErrHalt)
}

// Drill 3's controls: without message ids a replay duplicates; comparing
// revisions without the epoch lets a stale holder outrank a fresh one.
func TestDrill_WipeAndRebuild_Controls(t *testing.T) {
	ctx := context.Background()
	js, _ := cluster(t, natstest.RunJetStreamServer(t))
	for range 2 {
		_, err := js.Publish(ctx, "ape.evt.ann.taskforge.project.M2.gate-green", []byte(`{"seq":1}`))
		require.NoError(t, err)
	}
	n, _ := snapshot(t, js)
	require.Equal(t, 2, n, "the control: a replay without message ids stores twice")

	stale, fresh := layout.Token{Epoch: 1, Revision: 900}, layout.Token{Epoch: 2, Revision: 3}
	require.Greater(t, stale.Revision, fresh.Revision, "the control: by revision alone the stale holder wins")
	require.True(t, fresh.Newer(stale), "by (epoch, revision) the fresh one does")
}
