package rebuild

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// A just-started cluster answers the layout's first creates slowly: an R3
// create routinely outlasts nats.go's 5 s default (eval: 101 first tries in
// 133 timed out, `lease: declare ape-leases: context deadline exceeded`),
// and a read straight after the creates can meet "no responders" while the
// new stream elects its leader (V15-DECLARE-1). So each step of a declare
// and of a new epoch gets declareTry, and a step the cluster did not yet
// answer is tried again every declareEvery, for at most declareBudget in
// all. Vars so tests can shorten them.
var (
	declareTry    = 15 * time.Second
	declareEvery  = 2 * time.Second
	declareBudget = 90 * time.Second
)

// Retry runs step until it succeeds, returns an error that is not the
// cluster's not-yet (transient), or declareBudget is spent. Every step it
// is given must be idempotent: a try cut by its deadline may have landed.
// A refusal — a permission, JetStream not enabled, a bad config — is
// returned at once.
func Retry(ctx context.Context, what string, step func(ctx context.Context) error) error {
	deadline := time.Now().Add(declareBudget)
	for tries := 1; ; tries++ {
		tctx, cancel := context.WithTimeout(ctx, declareTry)
		err := step(tctx)
		cancel()
		if err == nil || !transient(ctx, err) {
			return err
		}
		wait := min(declareEvery, time.Until(deadline))
		if wait <= 0 {
			return fmt.Errorf("%s: the cluster did not answer in %d tries over %s: %w", what, tries, declareBudget, err)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}

// transient reports whether err is the cluster not answering yet, rather
// than refusing: a try's own deadline (not the caller's), a timeout, no
// responders, or JetStream saying it is not ready (temporarily unavailable,
// no leader). "No suitable peers" is not among them: --replicas 3 on one
// server says it too, and waiting would only hide that.
func transient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var api *jetstream.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode {
		case 10008, 10009: // JSClusterNotAvailErr, JSClusterNotLeaderErr
			return true
		}
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, nats.ErrTimeout) ||
		errors.Is(err, nats.ErrNoResponders)
}
