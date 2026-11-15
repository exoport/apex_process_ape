package coordinator

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// V15-COORD-MU-1: keep held c.mu across its renewal, so status (and the
// notices' fence) waited behind a slow one — in production up to three
// calls of nats.go's 5 s. Here a renewal is held for as long as the test
// likes, and status answers meanwhile, with the token held before it; once
// the renewal lands, status says its token.
func TestCoordinator_StatusAnswersDuringASlowRenewal(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	natstest.WaitKV(t, js, lease.MetaBucket)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	testKeepHook = func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	}
	t.Cleanup(func() { testKeepHook = nil })
	cfg := Config{Ensemble: "ens", ID: "c1", TTL: 10 * time.Second, Renew: 200 * time.Millisecond}
	rctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(rctx, nc, cfg) }()
	t.Cleanup(func() { stop(); <-done })
	var released sync.Once
	let := func() { released.Do(func() { close(release) }) }
	t.Cleanup(let) // first: a failed check must not leave the tick held
	status := func() Status {
		t.Helper()
		m, err := nc.Request("ape.coord.ens.status", nil, time.Second)
		require.NoError(t, err, "status answers while a renewal is slow")
		var st Status
		require.NoError(t, json.Unmarshal(m.Data, &st))
		return st
	}
	require.Eventually(t, func() bool {
		m, err := nc.Request("ape.coord.ens.status", nil, time.Second)
		return err == nil && len(m.Data) > 0
	}, 10*time.Second, 50*time.Millisecond, "the coordinator serves")

	armed.Store(true)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no tick reached its renewal")
	}
	before := status()
	require.Equal(t, "c1", before.ID)
	time.Sleep(3 * cfg.Renew) // still held: no tick runs meanwhile
	require.Equal(t, before.Token, status().Token, "nothing renewed while it is held")
	let()

	require.Eventually(t, func() bool { return status().Token != before.Token }, 5*time.Second, 50*time.Millisecond,
		"once the renewal lands, status says the renewed lease")
	_, tok, err := ls.Holder(ctx, "coord.ens")
	require.NoError(t, err)
	require.Equal(t, "c1", before.ID)
	require.NotEqual(t, before.Token, tok.String(), "renewed in the store too")
}

// COORD-MU-R1 (research's probe, on 947f971): with keep no longer holding
// c.mu across its renewal, the notices' fence could run after the renewal
// landed in the store and before keep wrote it back, read the old token,
// find it stale, read the same old token again, and fence out the
// coordinator's own publish (S5-1). Here a fence runs in that window, while
// keep holds the tick 500 ms (a slow reply's tail): it waits for the write
// back, and passes.
func TestCoordinator_AFenceDuringARenewalsWriteBackPasses(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	natstest.WaitKV(t, js, lease.MetaBucket)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	var armed atomic.Bool
	res := make(chan error, 1)
	testAfterRenew = func(c *Coordinator) {
		if !armed.CompareAndSwap(true, false) {
			return
		}
		go func() { res <- publisher{c}.fence(ctx) }()
		time.Sleep(500 * time.Millisecond)
	}
	t.Cleanup(func() { testAfterRenew = nil })
	cfg := Config{Ensemble: "ens", ID: "c1", TTL: 10 * time.Second, Renew: 200 * time.Millisecond}
	rctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(rctx, nc, cfg) }()
	t.Cleanup(func() { stop(); <-done })
	require.Eventually(t, func() bool {
		m, err := nc.Request("ape.coord.ens.status", nil, time.Second)
		return err == nil && len(m.Data) > 0
	}, 10*time.Second, 50*time.Millisecond, "the coordinator serves")

	armed.Store(true)
	select {
	case err := <-res:
		require.NoError(t, err, "the coordinator does not fence out its own publish")
	case <-time.After(10 * time.Second):
		t.Fatal("no tick reached its renewal")
	}
}

// COORD-MU-R2 (research's probe, on d9a8546): the fence held renewMu across
// its checks, network calls of up to 5 s each, and keep takes renewMu for
// its whole tick, so a renewal waited behind a slow fence, long enough in
// production (TTL 30 s, renew 10 s) to lose coord.<ens>. Here a fence's
// check is held for five ticks: renewals go on meanwhile, and the fence,
// its copy stale by then, passes on its re-check.
func TestCoordinator_ASlowFenceDoesNotHoldTheRenewalBack(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	natstest.WaitKV(t, js, lease.MetaBucket)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	var co atomic.Pointer[Coordinator]
	testAfterRenew = func(c *Coordinator) { co.CompareAndSwap(nil, c) }
	t.Cleanup(func() { testAfterRenew = nil; afterHeldCopy = nil })
	cfg := Config{Ensemble: "ens", ID: "c1", TTL: 10 * time.Second, Renew: 200 * time.Millisecond}
	rctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(rctx, nc, cfg) }()
	t.Cleanup(func() { stop(); <-done })
	tok := func() string {
		m, err := nc.Request("ape.coord.ens.status", nil, time.Second)
		require.NoError(t, err)
		var st Status
		require.NoError(t, json.Unmarshal(m.Data, &st))
		return st.Token
	}
	require.Eventually(t, func() bool { return co.Load() != nil }, 10*time.Second, 20*time.Millisecond)

	inFence, letGo := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	afterHeldCopy = func() {
		if once.CompareAndSwap(false, true) {
			close(inFence)
			<-letGo
		}
	}
	var let sync.Once
	t.Cleanup(func() { let.Do(func() { close(letGo) }) }) // first: a red must not leave the fence held
	res := make(chan error, 1)
	go func() { res <- publisher{co.Load()}.fence(ctx) }()
	<-inFence
	before := tok()
	time.Sleep(5 * cfg.Renew) // five ticks while the fence's check is slow
	during := tok()
	let.Do(func() { close(letGo) })
	require.NoError(t, <-res, "the fence, its copy stale by then, passes on the re-check")
	require.NotEqual(t, before, during, "the renewal went on while a fence was slow")
}
