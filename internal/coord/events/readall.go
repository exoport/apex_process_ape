package events

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ReadAll hands fn every stored message on filter, oldest first. The
// count comes from the stream's own per-subject state: an ordered
// consumer's pending count is not known before its first fetch.
func ReadAll(ctx context.Context, js jetstream.JetStream, filter string, fn func([]byte)) bool {
	st, err := js.Stream(ctx, EventsStream)
	if err != nil {
		return false
	}
	info, err := st.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return false
	}
	var left uint64
	for _, n := range info.State.Subjects {
		left += n
	}
	if left == 0 {
		return true
	}
	cons, err := js.OrderedConsumer(ctx, EventsStream, jetstream.OrderedConsumerConfig{FilterSubjects: []string{filter}, DeliverPolicy: jetstream.DeliverAllPolicy})
	if err != nil {
		return false
	}
	for left > 0 && ctx.Err() == nil {
		batch, err := cons.Fetch(int(min(left, 256)), jetstream.FetchMaxWait(time.Second))
		if err != nil {
			return false
		}
		n := uint64(0)
		for msg := range batch.Messages() {
			n++
			fn(msg.Data())
		}
		if n == 0 {
			break
		}
		left -= min(n, left)
	}
	return true
}
