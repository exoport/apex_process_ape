package member

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// noticesWait bounds `ape status`'s read of the notices: status must not
// hang on a slow cluster.
const noticesWait = 3 * time.Second

// Notices are the impact notices the ensemble's coordinator published to
// the member project's milestones (PLAN-V15-ASSESSOR step 5), by
// milestone: every impact-notice whose target is <project>/<M>. ok is
// false when NATS could not be read.
func Notices(ctx context.Context, m *Membership) (map[string][]apexv1.NoticeLine, bool) {
	ctx, cancel := context.WithTimeout(ctx, noticesWait)
	defer cancel()
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, "ape-status-notices")
	if err != nil {
		return nil, false
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, false
	}
	return ReadNotices(ctx, js, m.Ensemble, m.Project)
}

// ReadNotices is Notices on an open JetStream context.
func ReadNotices(ctx context.Context, js jetstream.JetStream, ensemble, project string) (map[string][]apexv1.NoticeLine, bool) {
	// By hand: events.Subject would make the wildcard a literal token.
	filter := "ape.evt.coord." + natsconn.SubjectToken(ensemble) + "." + events.KindCoord + ".*.impact-notice"
	// How many there are, from the stream's own count per subject: an
	// ordered consumer's pending count is not known before its first fetch.
	st, err := js.Stream(ctx, events.EventsStream)
	if err != nil {
		return nil, false
	}
	info, err := st.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return nil, false
	}
	var left uint64
	for _, n := range info.State.Subjects {
		left += n
	}
	out := map[string][]apexv1.NoticeLine{}
	if left == 0 {
		return out, true
	}
	cons, err := js.OrderedConsumer(ctx, events.EventsStream, jetstream.OrderedConsumerConfig{FilterSubjects: []string{filter}, DeliverPolicy: jetstream.DeliverAllPolicy})
	if err != nil {
		return nil, false
	}
	for left > 0 && ctx.Err() == nil {
		batch, err := cons.Fetch(int(min(left, 256)), jetstream.FetchMaxWait(time.Second))
		if err != nil {
			return out, false
		}
		n := uint64(0)
		for msg := range batch.Messages() {
			n++
			var ev struct {
				ID       string   `json:"id"`
				Target   string   `json:"target"`
				Reason   string   `json:"reason"`
				Evidence []string `json:"evidence"`
				Hold     bool     `json:"hold"`
				At       string   `json:"at"`
			}
			if json.Unmarshal(msg.Data(), &ev) != nil {
				continue
			}
			p, ms, ok := strings.Cut(ev.Target, "/")
			if ok && natsconn.SubjectToken(p) == project {
				out[ms] = append(out[ms], apexv1.NoticeLine{ID: ev.ID, Reason: ev.Reason, Evidence: ev.Evidence, Hold: ev.Hold, At: ev.At})
			}
		}
		if n == 0 {
			break
		}
		left -= min(n, left)
	}
	return out, true
}
