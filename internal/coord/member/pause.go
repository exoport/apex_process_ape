package member

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/conductor"
	"github.com/exoport/apex_process_ape/internal/coord/events"
)

// PauseResult is what `ape milestone pause|resume` reports.
type PauseResult struct {
	Milestone string `json:"milestone"`
	Paused    bool   `json:"paused"`
	By        string `json:"by"`
	Reason    string `json:"reason,omitempty"`
	At        string `json:"at"`
}

// PauseMilestone is a person's pause (pause) or resume of a milestone
// (Q-1C-1, D758; OQ6: a hold on one milestone is a person act of the
// person whose checkout holds it). It publishes the milestone-paused or
// milestone-resumed event OQ24's pause uses, kind "person", by the
// credential's user, so the gate that refuses a paused milestone's change
// new, start, archive, take-in and close (coord.milestone-paused) honours
// it unchanged. Only from the checkout whose book holds M's claim.
func PauseMilestone(ctx context.Context, p *apexv1.Project, ms, reason string, pause bool) (PauseResult, error) {
	s, err := openMilestones(ctx, p, nats.NoReconnect())
	if err != nil {
		return PauseResult{}, err
	}
	defer s.close()
	verb := "resume"
	if pause {
		verb = "pause"
	}
	if ok, err := s.claimed(ms); err != nil || !ok {
		if err == nil {
			err = notHolder(fmt.Sprintf("%s %s refused: this checkout does not hold %s's claim — a milestone is paused and resumed from the checkout holding it (OQ6)", verb, ms, ms))
		}
		return PauseResult{}, err
	}
	user, _, _ := strings.Cut(s.holder, "/")
	at := time.Now().UTC()
	event := apexv1.EventMilestoneResumed
	if pause {
		event = apexv1.EventMilestonePaused
	}
	data, err := json.Marshal(conductor.Pause{Kind: conductor.PausePerson, By: user, User: user, Reason: reason, At: at.Format(time.RFC3339)})
	if err != nil {
		return PauseResult{}, err
	}
	js, err := jetstream.New(s.nc)
	if err != nil {
		return PauseResult{}, err
	}
	subj := events.Subject(user, s.m.Project, events.KindProject, ms, event)
	if _, err := events.Publish(ctx, js, subj, s.holder+"/"+event+"/"+ms+"/"+at.Format(time.RFC3339Nano), data); err != nil {
		return PauseResult{}, fmt.Errorf("%s %s: publishing %s: %w", verb, ms, event, err)
	}
	return PauseResult{Milestone: ms, Paused: pause, By: user, Reason: reason, At: at.Format(time.RFC3339)}, nil
}
