package apexv1

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The conductor's decision log (v1.4.2, D660.3, D661): per person and
// project, written only by ape — every delegated act with its reason and
// evidence (RunDelegated), and the holds for the person and the open
// questions the conductor notes (`ape conductor note`). A conductor that
// resumes after a handoff reads what is still open (`ape conductor log
// --open`). The record is conductor-<person>.jsonl in _apex/state (local,
// ignored by git); conductor-<person>.md beside it is rendered from it on
// every write, for a person to read.

// The decision log's entry kinds.
const (
	DecisionAct      = "act"
	DecisionHold     = "hold"
	DecisionQuestion = "question"
	DecisionResolved = "resolved"
)

// maxDecisionLine bounds one line of the record (an act's reason and its
// evidence list).
const maxDecisionLine = 4 << 20

// DecisionEntry is one line of the decision log.
type DecisionEntry struct {
	N        int           `json:"n"`
	At       string        `json:"at"`
	Kind     string        `json:"kind"`
	Text     string        `json:"text"`
	Reason   string        `json:"reason,omitempty"`
	Orders   string        `json:"orders,omitempty"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
	// Resolves is the hold or question a resolved entry closes.
	Resolves int `json:"resolves,omitempty"`
}

// DecisionLog is a person's log as read back.
type DecisionLog struct {
	Person  string          `json:"person"`
	Path    string          `json:"path"`
	Entries []DecisionEntry `json:"entries"`
}

// DecisionLogPath is a person's decision log, the rendered .md.
func (p *Project) DecisionLogPath(person string) string {
	return filepath.Join(p.StateDir(), "conductor-"+PersonSlug(person)+".md")
}

func (p *Project) decisionRecordPath(person string) string {
	return filepath.Join(p.StateDir(), "conductor-"+PersonSlug(person)+".jsonl")
}

// ReadDecisionLog reads a person's log; an absent one is empty.
func (p *Project) ReadDecisionLog(person string) (*DecisionLog, error) {
	l := &DecisionLog{Person: PersonSlug(person), Path: p.Rel(p.DecisionLogPath(person)), Entries: []DecisionEntry{}}
	data, err := os.ReadFile(p.decisionRecordPath(person))
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), maxDecisionLine)
	for sc.Scan() {
		var e DecisionEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			l.Entries = append(l.Entries, e)
		}
	}
	return l, sc.Err()
}

// Open are the holds and questions no resolved entry closed, oldest first.
func (l *DecisionLog) Open() []DecisionEntry {
	closed := map[int]bool{}
	for _, e := range l.Entries {
		if e.Kind == DecisionResolved {
			closed[e.Resolves] = true
		}
	}
	out := []DecisionEntry{}
	for _, e := range l.Entries {
		if (e.Kind == DecisionHold || e.Kind == DecisionQuestion) && !closed[e.N] {
			out = append(out, e)
		}
	}
	return out
}

// NoteDecision adds a hold, a question, or a resolution of one of them to
// a person's log, and returns the entry.
func (p *Project) NoteDecision(person, kind, text string, resolves int) (*DecisionEntry, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, &UsageError{Code: "usage.text", Msg: "a note needs its text"}
	}
	switch kind {
	case DecisionHold, DecisionQuestion:
	case DecisionResolved:
		l, err := p.ReadDecisionLog(person)
		if err != nil {
			return nil, err
		}
		open := false
		for _, e := range l.Open() {
			open = open || e.N == resolves
		}
		if !open {
			return nil, &UsageError{Code: "usage.resolves", Msg: fmt.Sprintf("no open hold or question %d in %s's log (`ape conductor log --open`)", resolves, PersonSlug(person))}
		}
	default:
		return nil, &UsageError{Code: "usage.kind", Msg: "a note is a hold, a question, or resolves one"}
	}
	e := DecisionEntry{Kind: kind, Text: text, Resolves: resolves}
	return p.logDecision(person, e)
}

// logDecision appends e (numbered, stamped) and renders the .md. A log that
// cannot be written is reported, never fatal to an act already done.
func (p *Project) logDecision(person string, e DecisionEntry) (*DecisionEntry, error) {
	l, err := p.ReadDecisionLog(person)
	if err != nil {
		return nil, err
	}
	e.N, e.At = len(l.Entries)+1, Stamp()
	line, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	p.ensureState()
	f, err := os.OpenFile(p.decisionRecordPath(person), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	l.Entries = append(l.Entries, e)
	return &e, writeFileAtomic(p.DecisionLogPath(person), renderDecisionLog(l))
}

// renderDecisionLog is the person-readable log: what is open first, then
// every entry, newest first.
func renderDecisionLog(l *DecisionLog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Conductor decision log — %s\n\nWritten by ape (`ape conductor act`, `ape conductor note`); read with `ape conductor log`.\n\n## Open\n\n", l.Person)
	open := l.Open()
	if len(open) == 0 {
		b.WriteString("Nothing open.\n")
	}
	for _, e := range open {
		fmt.Fprintf(&b, "- [%d] %s %s: %s\n", e.N, e.At, e.Kind, e.Text)
	}
	b.WriteString("\n## Entries (newest first)\n\n")
	for _, e := range slices.Backward(l.Entries) {
		switch e.Kind {
		case DecisionAct:
			fmt.Fprintf(&b, "- [%d] %s act %s — %s (orders %s", e.N, e.At, e.Text, e.Reason, e.Orders)
			for _, ev := range e.Evidence {
				fmt.Fprintf(&b, "; %s %.12s", ev.Path, ev.SHA256)
			}
			b.WriteString(")\n")
		case DecisionResolved:
			fmt.Fprintf(&b, "- [%d] %s resolved [%d]: %s\n", e.N, e.At, e.Resolves, e.Text)
		default:
			fmt.Fprintf(&b, "- [%d] %s %s: %s\n", e.N, e.At, e.Kind, e.Text)
		}
	}
	return b.String()
}
