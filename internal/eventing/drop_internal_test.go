package eventing

import (
	"context"
	"testing"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// TestDropCounter is a white-box check of the overflow accounting: with the
// drain loop deliberately not running, the buffer fills and further Emits
// drop with a counter — the fire-and-forget "never block a run" guarantee.
func TestDropCounter(t *testing.T) {
	url := natstest.RunServer(t)
	nc, err := natsconn.Connect(context.Background(), natsconn.Config{URL: url}, "ape/test")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = nc.Drain() }()

	// Constructed directly (not via New) so the drain goroutine never starts.
	p := &Publisher{
		nc:      nc,
		prefix:  DefaultPrefix,
		user:    "u",
		project: "p",
		kind:    KindPipeline,
		id:      "r",
		ch:      make(chan message, 2),
	}
	p.Emit("a", nil) // buffered
	p.Emit("b", nil) // buffered (channel now full)
	p.Emit("c", nil) // overflow → dropped
	p.Emit("d", nil) // overflow → dropped

	if got := p.Dropped(); got != 2 {
		t.Fatalf("Dropped = %d, want 2", got)
	}
}

// TestEmitAfterCloseIsSafe locks in the guard against a late Emit (e.g. a
// trailing bridge hook after the run returns) racing Close — a send on a
// closed channel would panic. Run under -race.
func TestEmitAfterCloseIsSafe(t *testing.T) {
	url := natstest.RunServer(t)
	nc, err := natsconn.Connect(context.Background(), natsconn.Config{URL: url}, "ape/test")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = nc.Drain() }()

	p := New(nc, Options{Identity: natsconn.Identity{SubjectToken: "u"}, Project: "p", Kind: KindPipeline, ID: "r"})

	done := make(chan struct{})
	go func() {
		for range 2000 {
			p.Emit("hook", nil)
		}
		close(done)
	}()
	p.Close()
	<-done

	// Emits after Close are safe no-ops (dropped), never a panic.
	p.Emit("late", nil)
}
