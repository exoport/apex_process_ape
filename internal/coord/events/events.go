// Package events is APEX v1.5's event stream (stage 0, step 4): the
// project, change, contract and coord kinds on APE_EVENTS, each event with
// a message id the stream deduplicates, so a replay that races a live
// publish stores once (OQ14, §4.4 step 3), and waits that subscribe instead
// of polling (§3.1 "events instead of polling").
package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// Streams, from the layout.
const (
	EventsStream  = "APE_EVENTS"
	MetricsStream = "APE_METRICS"
	// Retention is the streams' live retention (§4.3).
	Retention = 90 * 24 * time.Hour
	// DedupWindow is how long the stream remembers a message id: long
	// enough for a replay to race a live publish of the same event.
	DedupWindow = 10 * time.Minute
)

// Kinds v1.5 adds under ape.evt.
const (
	KindProject  = "project"
	KindChange   = "change"
	KindContract = "contract"
	KindCoord    = "coord"
)

// Declare creates (or updates to the layout) APE_EVENTS and APE_METRICS,
// idempotently.
func Declare(ctx context.Context, js jetstream.JetStream, replicas int) error {
	for name, subj := range map[string]string{EventsStream: "ape.evt.>", MetricsStream: "ape.metrics.>"} {
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name: name, Subjects: []string{subj}, Replicas: replicas,
			MaxAge: Retention, Duplicates: DedupWindow,
		}); err != nil {
			return fmt.Errorf("events: declare %s: %w", name, err)
		}
	}
	return nil
}

// Subject is an event's subject: ape.evt.<owner>.<scope>.<kind>.<id>.<event>,
// where owner is the user (or `coord`) and scope the project (or the
// ensemble, for contract and coord). Owner and scope are slugged by the
// subject token rule (R8); id and event keep their case; an empty id is "-".
func Subject(owner, scope, kind, id, event string) string {
	if id == "" {
		id = "-"
	}
	return strings.Join([]string{"ape.evt", tok(owner), tok(scope), kind, part(id), part(event)}, ".")
}

// part makes an id or event name one subject token, keeping its case
// (`M2` stays `M2`): the characters NATS reads as separators or wildcards
// become `-`.
func part(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', '*', '>', ' ', '\t', '\n':
			return '-'
		}
		return r
	}, s)
}

func tok(s string) string { return natsconn.SubjectToken(s) }

// Publish stores one event with its message id. dup reports that the
// stream already had that id (a replay of a stored event).
func Publish(ctx context.Context, js jetstream.JetStream, subject, msgID string, payload []byte) (dup bool, err error) {
	ack, err := js.Publish(ctx, subject, payload, jetstream.WithMsgID(msgID))
	if err != nil {
		return false, fmt.Errorf("events: publish %s: %w", subject, err)
	}
	return ack.Duplicate, nil
}

// checkoutIDBytes is a checkout id's randomness: 12 hex characters.
const checkoutIDBytes = 6

// CheckoutIDFile is the checkout's id, under its _apex/state (OQ14).
const CheckoutIDFile = "checkout-id"

// CheckoutID is this checkout's id, generated once into stateDir and read
// back ever after: two checkouts of one project never share a message id.
func CheckoutID(stateDir string) (string, error) {
	path := filepath.Join(stateDir, CheckoutIDFile)
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("events: read checkout id: %w", err)
	}
	b := make([]byte, checkoutIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return CheckoutID(stateDir) // another process won the race: read its id
	}
	if err != nil {
		return "", fmt.Errorf("events: write checkout id: %w", err)
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return "", err
	}
	return id, f.Close()
}

// MessageID is an event's id: <project>/<checkout-id>/<seq>.
func MessageID(project, checkout string, seq int64) string {
	return layout.MessageID(tok(project), checkout, uint64(seq)) //nolint:gosec // seq is events.jsonl's, never negative
}

// Wake signals on each message on filter and once after each reconnect
// (a plain subscription is not redelivered what was published while the
// link was down, so the waiter re-reads its own records instead). The
// channel holds one signal; stop unsubscribes and restores the
// connection's reconnect handler.
func Wake(nc *nats.Conn, filter string) (wake <-chan struct{}, stop func(), err error) {
	ch := make(chan struct{}, 1)
	poke := func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	sub, err := nc.Subscribe(filter, func(*nats.Msg) { poke() })
	if err != nil {
		return nil, nil, fmt.Errorf("events: subscribe %s: %w", filter, err)
	}
	if err := nc.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, nil, fmt.Errorf("events: subscribe %s: %w", filter, err)
	}
	prev := nc.Opts.ReconnectedCB
	nc.SetReconnectHandler(func(c *nats.Conn) {
		poke()
		if prev != nil {
			prev(c)
		}
	})
	return ch, func() {
		_ = sub.Unsubscribe()
		nc.SetReconnectHandler(prev)
	}, nil
}

// Wait blocks until done reports true. done reads the waiter's own truth
// (ape's records): it runs once at the start, once on each event on filter,
// and once after each reconnect (Wake). There is no timer: a connected
// waiter wakes only on events.
func Wait(ctx context.Context, nc *nats.Conn, filter string, done func() (bool, error)) error {
	wake, stop, err := Wake(nc, filter)
	if err != nil {
		return err
	}
	defer stop()
	for {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
