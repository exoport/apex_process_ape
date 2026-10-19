package apexv1

import (
	"context"
	"encoding/json"
	"strings"
)

// AnswerChange records a person's answer to a blocked change (H9, D47):
// status answered, answer {by, at, text} in its state.json — committed
// where the change's state lives, its branch change/<id> when it has one,
// else the branch the draft was planned on, whichever branch HEAD is on
// (editOnBranch) — as "apex: <id> answered". The orchestrator then has
// apex-change re-plan the change with the answer as the hint, and starts
// it again (`ape change start` takes an answered change as a blocked one).
// A person's command: refused in a model session.
func (p *Project) AnswerChange(ctx context.Context, id, text, by string) (*State, error) {
	if err := requirePerson(ctx, ActAnswer, "ape change answer"); err != nil {
		return nil, err
	}
	text, by = strings.TrimSpace(text), strings.TrimSpace(by)
	if by == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names the person answering"}
	}
	if text == "" {
		return nil, &UsageError{Code: "usage.text", Msg: "--text is required: the answer to what blocks the change"}
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if ci.State.Status != StatusBlocked {
		return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + ci.State.Status + " — answer applies to a blocked change"}
	}
	answer := &Answer{By: by, At: Stamp(), Text: text}
	if !p.isWorkTree(ctx) {
		st := ci.State
		answer.ReplansBefore = len(st.Replans)
		st.Status, st.Answer = StatusAnswered, answer
		return st, SaveState(p.StateFile(id), st)
	}
	branch := ChangeBranch(id)
	if !p.branchExists(ctx, branch) {
		if branch, err = p.baseBranch(ctx); err != nil {
			return nil, err
		}
	}
	rel := p.Rel(p.StateFile(id))
	var st *State
	_, err = p.editOnBranch(ctx, branch, "apex: "+id+" "+StatusAnswered+"\n\nChange: "+id, []string{rel},
		func(cur map[string][]byte) (map[string][]byte, error) {
			if cur[rel] == nil {
				return nil, &FailError{Code: "change.no-state", Msg: "change " + id + " has no state.json on " + branch}
			}
			s, err := parseState(rel+" on "+branch, cur[rel])
			if err != nil {
				return nil, &FailError{Code: CodeStateUnreadable, Msg: err.Error()}
			}
			if s.Status != StatusBlocked {
				return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + s.Status + " on " + branch + " — answer applies to a blocked change"}
			}
			answer.ReplansBefore = len(s.Replans)
			s.Status, s.Answer = StatusAnswered, answer
			if s.Accepted == nil {
				s.Accepted = []Accept{}
			}
			if s.Baseline == nil {
				s.Baseline = []Baseline{}
			}
			data, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return nil, err
			}
			st = s
			return map[string][]byte{rel: append(data, '\n')}, nil
		})
	if err != nil {
		return nil, err
	}
	// The answer restarts the count limits.attempts_total judges (H19).
	if _, err := p.updateAttempts(id, func(a *Attempts) { a.AtAnswer = a.Total }); err != nil {
		return nil, err
	}
	return st, nil
}
