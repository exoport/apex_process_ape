package member

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// Notice-ack outcomes (Q3).
const (
	NoticeActed     = "acted"
	NoticeDismissed = "dismissed"
)

// NoticeAck is a notice-acked event's payload, and what `ape coord notice
// ack` reports.
type NoticeAck struct {
	ID        string `json:"id"`
	Target    string `json:"target"`
	Milestone string `json:"milestone"`
	Outcome   string `json:"outcome"`
	Kind      string `json:"kind"`
	By        string `json:"by"`
	User      string `json:"user"`
	At        string `json:"at"`
}

// AckNotice is a person's ack of impact notice id (PLAN-V15-ASSESSOR step
// 6; Q3, research 5): acted or dismissed, recorded once with the
// credential's user (no --by), and only from the checkout whose book holds
// the target milestone's claim. It publishes notice-acked, kind project,
// on the target milestone. The caller has refused a model session
// (apexv1.PersonCoordNoticeAck).
func AckNotice(ctx context.Context, p *apexv1.Project, id, outcome string) (NoticeAck, error) {
	s, err := openMilestones(ctx, p, nats.NoReconnect())
	if err != nil {
		return NoticeAck{}, err
	}
	defer s.close()
	js, err := jetstream.New(s.nc)
	if err != nil {
		return NoticeAck{}, err
	}
	st, err := js.Stream(ctx, events.EventsStream)
	if err != nil {
		return NoticeAck{}, err
	}
	raw, err := st.GetLastMsgForSubject(ctx, events.Subject("coord", s.m.Ensemble, events.KindCoord, id, "impact-notice"))
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return NoticeAck{}, &apexv1.FailError{Code: apexv1.CodeNoticeUnknown, Action: apexv1.ActionPerson, Msg: "ensemble " + s.m.Ensemble + " has no impact notice " + id + " — `ape status --json` lists the open ones (milestones[].notices)"}
	}
	if err != nil {
		return NoticeAck{}, err
	}
	var n struct {
		Target string `json:"target"`
	}
	_ = json.Unmarshal(raw.Data, &n)
	project, ms, _ := strings.Cut(n.Target, "/")
	if natsconn.SubjectToken(project) != s.m.Project {
		return NoticeAck{}, notHolder(fmt.Sprintf("notice %s is to %s, not to a milestone of %s: it is acknowledged from the checkout holding %s's claim", id, n.Target, s.m.Project, n.Target))
	}
	if ok, err := s.claimed(ms); err != nil || !ok {
		if err == nil {
			err = notHolder(fmt.Sprintf("notice ack %s refused: this checkout does not hold %s's claim — a notice is acknowledged by the person whose checkout holds its milestone (Q3)", id, ms))
		}
		return NoticeAck{}, err
	}
	acks, _ := ReadAcks(ctx, js, s.m.Project)
	if prior, ok := acks[id]; ok {
		return NoticeAck{}, &apexv1.FailError{Code: apexv1.CodeNoticeAcked, Action: apexv1.ActionPerson, Msg: fmt.Sprintf("notice %s was acknowledged already: %s, by %s at %s", id, prior.Outcome, prior.By, prior.At)}
	}
	user, _, _ := strings.Cut(s.holder, "/")
	ack := NoticeAck{ID: id, Target: n.Target, Milestone: ms, Outcome: outcome, Kind: "person", By: user, User: user, At: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.Marshal(ack)
	if err != nil {
		return NoticeAck{}, err
	}
	subj := events.Subject(user, s.m.Project, events.KindProject, ms, apexv1.EventNoticeAcked)
	if _, err := events.Publish(ctx, js, subj, s.holder+"/"+apexv1.EventNoticeAcked+"/"+id, data); err != nil {
		return NoticeAck{}, fmt.Errorf("notice ack %s: publishing %s: %w", id, apexv1.EventNoticeAcked, err)
	}
	return ack, nil
}

// ReadAcks are the project's notice acks, by notice id (the first of
// each).
func ReadAcks(ctx context.Context, js jetstream.JetStream, project string) (map[string]NoticeAck, bool) {
	out := map[string]NoticeAck{}
	ok := events.ReadAll(ctx, js, "ape.evt.*."+natsconn.SubjectToken(project)+"."+events.KindProject+".*."+apexv1.EventNoticeAcked, func(data []byte) {
		var a NoticeAck
		if json.Unmarshal(data, &a) == nil && a.ID != "" {
			if _, seen := out[a.ID]; !seen {
				out[a.ID] = a
			}
		}
	})
	return out, ok
}
