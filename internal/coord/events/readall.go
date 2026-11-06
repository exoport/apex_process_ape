package events

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go/jetstream"
)

// ReadAll hands fn every stored message on filter, oldest first. The
// count comes from the stream's own per-subject state, and the messages
// are read one by one, each the next on filter (MSG.GET's next_by_subj):
// no consumer, so it runs on a person's credential, which may touch no
// consumer but its own conductor's (V12-3).
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
	for seq := info.State.FirstSeq; left > 0; left-- {
		m, err := st.GetMsg(ctx, seq, jetstream.WithGetMsgSubject(filter))
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			break // aged out since the count
		}
		if err != nil {
			return false
		}
		fn(m.Data)
		seq = m.Sequence + 1
	}
	return true
}
