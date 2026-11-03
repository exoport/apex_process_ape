package member

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// publishTimeout bounds one event's publish: Emit waits for it.
const publishTimeout = 2 * time.Second

// Publish publishes one logged event of a member project as kind project,
// message id <project>/<checkout-id>/<seq> (plan step 4, OQ14): it is
// apexv1.EventSink. Nothing for a project in no ensemble; nothing during a
// recorded outage, and a failure records one: `ape nats replay` writes
// such events back, deduplicated by their ids.
func Publish(ctx context.Context, p *apexv1.Project, ev apexv1.Event) {
	m, err := Load(p)
	if err != nil || !ReadLink(p.Root).DownSince.IsZero() {
		return
	}
	id, err := natsconn.DecodeIdentity(m.Creds)
	if err != nil {
		return
	}
	checkout, err := events.CheckoutID(p.StateDir())
	if err != nil {
		return
	}
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-event", nats.NoReconnect())
	if err != nil {
		_, _ = Down(p.Root, time.Now())
		return
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	_, _ = events.PublishProject(pctx, js, id.SubjectToken, m.Project, checkout, ev)
}

// Watch wakes a waiter on every event published for the member project
// (any checkout's), and once after each reconnect, when the waiter
// re-reads its log (plan step 4; events.Wake). live is false for a
// project in no ensemble or with NATS unreachable: the waiter polls as
// before.
func Watch(ctx context.Context, p *apexv1.Project) (wake <-chan struct{}, stop func(), live bool) {
	m, err := Load(p)
	if err != nil {
		return nil, func() {}, false
	}
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-wait")
	if err != nil {
		return nil, func() {}, false
	}
	wake, unsub, err := events.Wake(nc, "ape.evt.*."+m.Project+".project.>")
	if err != nil {
		nc.Close()
		return nil, func() {}, false
	}
	_ = Up(p.Root) // connected: whatever outage was recorded is over
	return wake, func() { unsub(); nc.Close() }, true
}

// PublishTrigger publishes an assessor trigger (PLAN-V15-ASSESSOR step 1)
// on NATS only, kind project, with the message id
// <project>/<checkout-id>/<event>/<M>/<key>: the same act's trigger is
// stored once however often it is published, and an impact notice names
// its triggers by these ids. It is apexv1.TriggerSink. Nothing for a project
// in no ensemble or during a recorded outage (a trigger is advice; it is
// not replayed).
func PublishTrigger(ctx context.Context, p *apexv1.Project, t apexv1.Trigger) {
	m, err := Load(p)
	if err != nil || !ReadLink(p.Root).DownSince.IsZero() {
		return
	}
	id, err := natsconn.DecodeIdentity(m.Creds)
	if err != nil {
		return
	}
	checkout, err := events.CheckoutID(p.StateDir())
	if err != nil {
		return
	}
	subject, msgID, payload, err := TriggerMessage(id.SubjectToken, m.Project, checkout, t, time.Now())
	if err != nil {
		return
	}
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-trigger", nats.NoReconnect())
	if err != nil {
		_, _ = Down(p.Root, time.Now())
		return
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	_, _ = events.Publish(pctx, js, subject, msgID, payload)
}

// TriggerMessage is a trigger as PublishTrigger publishes it: the subject
// ape.evt.<user>.<project>.project.<M>.<event>, the message id
// <project>/<checkout>/<event>/<M>/<key> and the payload {event, milestone,
// change, commit, paths, gate, key, project, at}.
func TriggerMessage(user, project, checkout string, t apexv1.Trigger, at time.Time) (subject, msgID string, payload []byte, err error) {
	payload, err = json.Marshal(struct {
		apexv1.Trigger
		Project string `json:"project"`
		At      string `json:"at"`
	}{t, project, at.UTC().Format(time.RFC3339)})
	msgID = natsconn.SubjectToken(project) + "/" + checkout + "/" + t.Event + "/" + t.Milestone + "/" + t.Key
	return events.Subject(user, project, events.KindProject, t.Milestone, t.Event), msgID, payload, err
}
