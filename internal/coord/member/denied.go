package member

import (
	"errors"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// denials keeps the first permission violation a connection's server
// reported (V12d, R1). A request the grant denies is never answered: it
// ends at its deadline, which reads as NATS not answering, and a run
// would wait out its bound for a grant gap. The violation arrives on the
// connection's error handler instead, and this is where a caller looks.
type denials struct {
	mu  sync.Mutex
	err error
}

// option is the connection option that records violations, and drops
// the rest (natsconn's default prints them).
func (d *denials) option() nats.Option {
	return nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		if !errors.Is(err, nats.ErrPermissionViolation) {
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.err == nil {
			d.err = err
		}
	})
}

// gap is a refusal for a call the server denied this credential, nil when
// it denied nothing: a grant gap, which a person fixes, not an outage.
func (d *denials) gap(what string, cause error) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err == nil {
		return nil
	}
	return &apexv1.FailError{Code: apexv1.CodeCoordMembership, Action: apexv1.ActionPerson, Msg: fmt.Sprintf(
		"%s: the server refused this credential's call (%v; then %v) — a grant that does not cover what ape does, not NATS away, so ape does not wait it out; a person re-mints the credential (`ape nats mint user`)", what, d.err, cause)}
}
