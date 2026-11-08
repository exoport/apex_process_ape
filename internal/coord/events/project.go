package events

import (
	"context"
	"encoding/json"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// PublishProject publishes one line of the project's events.jsonl as kind
// project: subject ape.evt.<user>.<project>.project.<milestone>.<event>,
// message id <project>/<checkout>/<seq>, the line itself as the payload.
// The line's seq is the checkout's own sequence, so re-publishing the log
// (a replay) stores each event once.
func PublishProject(ctx context.Context, js jetstream.JetStream, user, project, checkout string, e apexv1.Event) (dup bool, err error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	return Publish(ctx, js, Subject(user, project, KindProject, e.Milestone, e.Event), MessageID(project, checkout, e.Seq), payload)
}

// PublishProjectID is PublishProject with the message id given: `ape ci
// watch`'s ci-watch-<event>-<sha> (research's DIM point 3), so two
// watchers that see one merge publish it once within DedupWindow.
func PublishProjectID(ctx context.Context, js jetstream.JetStream, user, project, msgID string, e apexv1.Event) (dup bool, err error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	return Publish(ctx, js, Subject(user, project, KindProject, e.Milestone, e.Event), msgID, payload)
}
