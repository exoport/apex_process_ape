package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Questions as acts (v1.4.2, D663 2a): an orchestrator asks the person a
// design question through ape (`ape question ask`), recorded; the person
// answers it (`ape question answer`, a person act, E2-grantable by
// "answer Q<n>: <answer>"), or their conductor does under standing orders
// that name question-answer. An open question is a person action, so
// `ape status` lists it, the person event and the notifier carry it, and
// `ape status --wait --until person` wakes on it.

// ActQuestionAnswer is the person act of answering a question.
const ActQuestionAnswer = "question answer"

// The question events.
const (
	EventQuestion         = "question"
	EventQuestionAnswered = "question-answered"
)

// CodeQuestionNotFound / CodeQuestionAnswered: no such question; one
// already answered.
const (
	CodeQuestionNotFound = "question.not-found"
	CodeQuestionAnswered = "question.answered"
)

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeQuestionNotFound, 1, ActionStop, "`ape question answer|show`: no question has that id in this project (`ape question list`)"},
		CodeDoc{CodeQuestionAnswered, 1, ActionStop, "`ape question answer`: the question was answered already; its answer stands (`ape question list --json`)"},
	)
}

// QuestionAnswer is a question's answer.
type QuestionAnswer struct {
	By   string `json:"by"`
	At   string `json:"at"`
	Text string `json:"text"`
}

// Question is one recorded question.
type Question struct {
	ID        string          `json:"id"`
	At        string          `json:"at"`
	Milestone string          `json:"milestone,omitempty"`
	Change    string          `json:"change,omitempty"`
	Text      string          `json:"text"`
	Options   []string        `json:"options,omitempty"`
	Answer    *QuestionAnswer `json:"answer,omitempty"`
}

// QuestionsPath holds the project's questions (local state).
func (p *Project) QuestionsPath() string { return filepath.Join(p.StateDir(), "questions.json") }

// Questions reads the project's questions, oldest first.
func (p *Project) Questions() ([]Question, error) {
	data, err := os.ReadFile(p.QuestionsPath())
	if errors.Is(err, os.ErrNotExist) {
		return []Question{}, nil
	}
	if err != nil {
		return nil, err
	}
	var qs []Question
	if err := json.Unmarshal(data, &qs); err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", p.Rel(p.QuestionsPath()), err)
	}
	return qs, nil
}

// OpenQuestions are the questions not answered yet.
func (p *Project) OpenQuestions() []Question {
	qs, _ := p.Questions()
	out := []Question{}
	for _, q := range qs {
		if q.Answer == nil {
			out = append(out, q)
		}
	}
	return out
}

func (p *Project) editQuestions(edit func([]Question) ([]Question, error)) error {
	p.ensureState()
	unlock, err := lockFileExclusive(p.QuestionsPath() + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	qs, err := p.Questions()
	if err != nil {
		return err
	}
	qs, err = edit(qs)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(qs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p.QuestionsPath(), string(data)+"\n")
}

// AskQuestion records a question and logs the question event (the
// notifier runs). Asking is no person act: any session may ask.
func (p *Project) AskQuestion(ctx context.Context, q Question, warn io.Writer) (*Question, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return nil, &UsageError{Code: "usage.text", Msg: "the question needs its text"}
	}
	err := p.editQuestions(func(qs []Question) ([]Question, error) {
		q.ID, q.At = "Q"+strconv.Itoa(len(qs)+1), Stamp()
		return append(qs, q), nil
	})
	if err != nil {
		return nil, err
	}
	p.Emit(ctx, Event{Event: EventQuestion, Milestone: q.Milestone, Change: q.Change, Action: "answer " + q.ID + ": <answer>", Reason: q.Text}, warn)
	return &q, nil
}

// AnswerQuestion records a question's answer: a person's act (E2), or a
// conductor's under orders (RunDelegated builds the act).
func (p *Project) AnswerQuestion(ctx context.Context, id, text, by string) (*Question, error) {
	if err := requirePerson(ctx, ActQuestionAnswer, "ape question answer"); err != nil {
		return nil, err
	}
	text, by = strings.TrimSpace(text), strings.TrimSpace(by)
	if by == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names the person answering"}
	}
	if text == "" {
		return nil, &UsageError{Code: "usage.text", Msg: "--text is required: the answer"}
	}
	var out Question
	err := p.editQuestions(func(qs []Question) ([]Question, error) {
		for i := range qs {
			if qs[i].ID != id {
				continue
			}
			if qs[i].Answer != nil {
				return nil, &FailError{Code: CodeQuestionAnswered, Msg: fmt.Sprintf("%s was answered by %s at %s", id, qs[i].Answer.By, qs[i].Answer.At)}
			}
			qs[i].Answer = &QuestionAnswer{By: by, At: Stamp(), Text: text}
			out = qs[i]
			return qs, nil
		}
		return nil, &FailError{Code: CodeQuestionNotFound, Msg: "no question " + id + " in this project (`ape question list`)"}
	})
	if err != nil {
		return nil, err
	}
	p.Emit(ctx, Event{Event: EventQuestionAnswered, Milestone: out.Milestone, Change: out.Change, Action: id, Reason: "answered by " + by}, nil)
	return &out, nil
}
