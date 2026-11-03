package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// The assessor's answer and what the coordinator does with it
// (PLAN-V15-ASSESSOR step 5; research 3, 6, R5; ANSWER-NOTICE-SCHEMA). The
// session writes notice.json and publishes nothing; the coordinator
// validates the file, fences with its own current lease, and publishes
// impact-notice (and hold-requested) on the coord kind. Any refused field
// refuses the whole batch, which then publishes assessor-failed once and
// nothing else: a session cannot probe the filter one notice at a time.
const (
	EventImpactNotice   = "impact-notice"
	EventHoldRequested  = "hold-requested"
	EventAssessorFailed = "assessor-failed"
	// EventAssessorSkipped: a batch not assessed, the ensemble's assessor
	// budget spent or the usage guard against it (step 7, D2).
	EventAssessorSkipped = "assessor-skipped"

	// NoticeFileCap and the field caps (R5).
	NoticeFileCap   = 16 << 10
	NoticeReasonCap = 600
	noticeEvidence  = 8
	noticePathCap   = 200
)

// Notice is one entry of notice.json.
type Notice struct {
	Target   string   `json:"target"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence"`
	Hold     bool     `json:"hold,omitempty"`
}

// NoticeCheck is what a batch's notices are judged against.
type NoticeCheck struct {
	// Project is the batch's project; Open its open milestones; Own the
	// milestones its events are of.
	Project string
	Open    []string
	Own     []string
	// Snapshot is the batch folder; DiffPaths the paths its diffs change.
	Snapshot  string
	DiffPaths []string
}

// noticeSecretRe are the secret shapes a reason may not hold (research 6):
// GitHub tokens, a NATS seed, a PEM block.
var noticeSecretRe = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]|github_pat_|\bS[UAONCX][A-Z2-7]{20,}|-----BEGIN`)

// ErrNoticeRefused: notice.json is missing, does not parse, or breaks a rule.
var ErrNoticeRefused = errors.New("notice.json refused")

// NoticeError is a refusal: Rule names the rule broken and quotes nothing
// the session wrote, so it may be published (assessor-failed); Detail
// quotes it, and stays in the coordinator's own record of the batch.
type NoticeError struct{ Rule, Detail string }

func (e *NoticeError) Error() string { return "notice.json refused: " + e.Detail }
func (e *NoticeError) Unwrap() error { return ErrNoticeRefused }

// ValidateNotices reads and judges a batch's notice.json: nil and an
// error wrapping ErrNoticeRefused when any part is refused; the empty
// list for {"notices": []}.
func ValidateNotices(data []byte, chk NoticeCheck) ([]Notice, error) {
	refuse := func(rule, format string, a ...any) ([]Notice, error) {
		return nil, &NoticeError{Rule: rule, Detail: fmt.Sprintf(format, a...)}
	}
	if len(data) > NoticeFileCap {
		return refuse("over the file cap", "%d bytes, over the %d-byte cap", len(data), NoticeFileCap)
	}
	var file struct {
		Notices *[]Notice `json:"notices"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return refuse("does not parse, or has a key other than notices, target, reason, evidence and hold", "it does not parse as {\"notices\": [...]} with only target, reason, evidence and hold: %v", err)
	}
	if dec.More() {
		return refuse("more than one JSON value", "more than one JSON value")
	}
	if file.Notices == nil {
		return refuse("no notices key", "no notices key (nothing affected is {\"notices\": []})")
	}
	allOwn := ""
	if len(slices.Compact(slices.Sorted(slices.Values(chk.Own)))) == 1 {
		allOwn = chk.Own[0]
	}
	seen := map[string]bool{}
	for i, n := range *file.Notices {
		at := fmt.Sprintf("notice %d", i+1)
		project, ms, ok := strings.Cut(n.Target, "/")
		switch {
		case !ok || project != chk.Project:
			return refuse("a target is not <project>/<M> of the batch's project", "%s: target %q is not <project>/<M> of this batch's project %s", at, n.Target, chk.Project)
		case !slices.Contains(chk.Open, ms):
			return refuse("a target is not an open milestone", "%s: target %q is not an open milestone", at, n.Target)
		case ms == allOwn:
			return refuse("a target is the milestone every event is of", "%s: target %q is the milestone every event of the batch is of: the assessor asks about another one", at, n.Target)
		case seen[n.Target]:
			return refuse("two notices for one target", "%s: a second notice for %s (one per target)", at, n.Target)
		}
		seen[n.Target] = true
		if err := checkReason(n.Reason); err != nil {
			return refuse("a reason "+err.Error(), "%s: reason %v", at, err)
		}
		if len(n.Evidence) < 1 || len(n.Evidence) > noticeEvidence {
			return refuse("evidence is not 1 to 8 paths", "%s: %d evidence paths, not 1 to %d", at, len(n.Evidence), noticeEvidence)
		}
		for _, p := range n.Evidence {
			if err := checkEvidence(p, chk); err != nil {
				return refuse("an evidence path "+err.Error(), "%s: evidence %q %v", at, p, err)
			}
		}
	}
	return *file.Notices, nil
}

func checkReason(r string) error {
	switch {
	case strings.TrimSpace(r) == "":
		return errors.New("is empty")
	case utf8.RuneCountInString(r) > NoticeReasonCap:
		return fmt.Errorf("is over %d characters", NoticeReasonCap)
	case strings.ContainsFunc(r, func(c rune) bool { return c != '\n' && unicode.IsControl(c) }):
		return errors.New("holds a control character")
	case noticeSecretRe.MatchString(r):
		return errors.New("matches a secret pattern")
	}
	return nil
}

// checkEvidence: a relative path with no `..`, at most noticePathCap
// long, naming a regular file of the snapshot or a path a diff in the
// input changes (D762+: an open milestone's own files exist only on its
// branch).
func checkEvidence(p string, chk NoticeCheck) error {
	switch {
	case p == "" || len(p) > noticePathCap:
		return fmt.Errorf("is empty or over %d characters", noticePathCap)
	case strings.HasPrefix(p, "/") || !filepath.IsLocal(filepath.FromSlash(p)) || slices.Contains(strings.Split(p, "/"), ".."):
		return errors.New("is not a relative path inside the snapshot")
	case slices.Contains(chk.DiffPaths, p):
		return nil
	}
	info, err := os.Lstat(filepath.Join(chk.Snapshot, filepath.FromSlash(p)))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("is neither a regular file of the snapshot nor a path a diff in the input changes")
	}
	return nil
}

// NoticeEvent is impact-notice's and hold-requested's payload.
type NoticeEvent struct {
	ID       string   `json:"id"`
	Batch    string   `json:"batch"`
	Target   string   `json:"target"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence"`
	Hold     bool     `json:"hold"`
	// Events are the batch's triggers' message ids (NOTE-NOTICE-EVENTS-ID).
	Events []string `json:"events"`
	At     string   `json:"at"`
}

// NoticeID is a notice's id: <batch>-<project>-<M>, one subject token.
func NoticeID(batch, project, ms string) string {
	return batch + "-" + natsconn.SubjectToken(project) + "-" + ms
}

// ErrFenced: the coordinator no longer holds its lease; nothing was
// published.
var ErrFenced = errors.New("coordinator: fenced out")

// Publisher publishes a batch's outcome, each publish fenced with the
// coordinator's current lease. OnBatch gets one.
type Publisher interface {
	// Notices publishes each notice once (impact-notice, and
	// hold-requested when it asks), returning the ids published and those
	// already published (a replayed batch).
	Notices(ctx context.Context, batch, project string, eventIDs []string, ns []Notice) (published, dup []string, err error)
	// Failed publishes assessor-failed for the batch's project, once.
	Failed(ctx context.Context, batch, project, reason string, eventIDs []string) error
	// Skipped publishes assessor-skipped for the batch's project, once
	// (step 7): reason is budget or usage; first marks a stage's first
	// skip, which the conductor surfaces to the person once.
	Skipped(ctx context.Context, s Skip) error
	// Metrics publishes a session's tokens on ape.metrics.coord.<ens>.
	// <batch>-<project> (core NATS, as ape.metrics is; informational).
	Metrics(batch, project string, m AssessMetrics) error
}

// Skip is assessor-skipped's payload.
//
//nolint:tagliatelle // ape's NATS payloads are snake_case (events.md)
type Skip struct {
	Batch   string   `json:"batch"`
	Project string   `json:"project"`
	Reason  string   `json:"reason"`
	Detail  string   `json:"detail,omitempty"`
	Spent   int64    `json:"spent_tokens"`
	Budget  int64    `json:"budget_tokens"`
	First   bool     `json:"first"`
	Events  []string `json:"events"`
	At      string   `json:"at"`
}

// AssessMetrics is one assessor session's usage.
//
//nolint:tagliatelle // ape's NATS payloads are snake_case (events.md)
type AssessMetrics struct {
	Batch       string  `json:"batch"`
	Project     string  `json:"project"`
	Ensemble    string  `json:"ensemble"`
	TokensTotal int64   `json:"tokens_total"`
	Input       int64   `json:"input_tokens"`
	Output      int64   `json:"output_tokens"`
	CacheRead   int64   `json:"cache_read_input_tokens"`
	CacheWrite  int64   `json:"cache_creation_input_tokens"`
	CostUSD     float64 `json:"cost_usd"`
	Termination string  `json:"termination,omitempty"`
	SpentTotal  int64   `json:"ensemble_spent_tokens"`
	At          string  `json:"at"`
}

type publisher struct{ c *Coordinator }

// afterHeldCopy is a test seam: called between fence's copy of the
// lease and its check (S5-1).
var afterHeldCopy func()

// fence checks the coordinator's current lease. A renew between the copy
// and the check moves the key's revision (S5-1): then the lease it holds
// now is checked once more, so the coordinator never fences out its own
// publish; a lease another coordinator took stays refused.
func (p publisher) fence(ctx context.Context) error {
	c := p.c
	held := func() lease.Lease {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.held
	}
	l := held()
	if afterHeldCopy != nil {
		afterHeldCopy()
	}
	err := c.leases.Fence(ctx, l.Key, l.Token)
	if errors.Is(err, lease.ErrStale) {
		if now := held(); now.Token != l.Token {
			err = c.leases.Fence(ctx, now.Key, now.Token)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFenced, err)
	}
	return nil
}

// once publishes payload on the coord subject of id and event unless that
// subject already holds a message: a dedupe that never expires, beside
// the stream's 10-minute window (R5/D3).
func (p publisher) once(ctx context.Context, id, event string, payload any) (dup bool, err error) {
	return p.onceOn(ctx, events.Subject(Owner, p.c.cfg.Ensemble, events.KindCoord, id, event), id, event, payload)
}

// onceOn is once on subject subj.
func (p publisher) onceOn(ctx context.Context, subj, id, event string, payload any) (dup bool, err error) {
	c := p.c
	st, err := c.js.Stream(ctx, events.EventsStream)
	if err != nil {
		return false, err
	}
	if _, err := st.GetLastMsgForSubject(ctx, subj); err == nil {
		return true, nil
	} else if !errors.Is(err, jetstream.ErrMsgNotFound) {
		return false, err
	}
	if err := p.fence(ctx); err != nil {
		return false, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	return events.Publish(ctx, c.js, subj, id+"/"+event, data)
}

func (p publisher) Notices(ctx context.Context, batch, project string, eventIDs []string, ns []Notice) (published, dup []string, err error) {
	if err := p.fence(ctx); err != nil {
		return nil, nil, err
	}
	at := time.Now().UTC().Format(time.RFC3339)
	for _, n := range ns {
		_, ms, _ := strings.Cut(n.Target, "/")
		id := NoticeID(batch, project, ms)
		ev := NoticeEvent{ID: id, Batch: batch, Target: n.Target, Reason: n.Reason, Evidence: n.Evidence, Hold: n.Hold, Events: eventIDs, At: at}
		d, err := p.once(ctx, id, EventImpactNotice, ev)
		if err != nil {
			return published, dup, err
		}
		if n.Hold {
			if _, err := p.once(ctx, id, EventHoldRequested, ev); err != nil {
				return published, dup, err
			}
		}
		if d {
			dup = append(dup, id)
		} else {
			published = append(published, id)
		}
	}
	return published, dup, nil
}

type failedEvent struct {
	Batch   string   `json:"batch"`
	Project string   `json:"project"`
	Reason  string   `json:"reason"`
	Events  []string `json:"events"`
	At      string   `json:"at"`
}

func (p publisher) Failed(ctx context.Context, batch, project, reason string, eventIDs []string) error {
	if len(reason) > 2*NoticeReasonCap {
		reason = reason[:2*NoticeReasonCap] + "…"
	}
	_, err := p.once(ctx, batch+"-"+natsconn.SubjectToken(project), EventAssessorFailed,
		failedEvent{Batch: batch, Project: project, Reason: reason, Events: eventIDs, At: time.Now().UTC().Format(time.RFC3339)})
	return err
}

func (p publisher) Skipped(ctx context.Context, s Skip) error {
	if s.At == "" {
		s.At = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := p.once(ctx, s.Batch+"-"+natsconn.SubjectToken(s.Project), EventAssessorSkipped, s)
	return err
}

func (p publisher) Metrics(batch, project string, m AssessMetrics) error {
	m.Batch, m.Project, m.Ensemble = batch, project, p.c.cfg.Ensemble
	if m.At == "" {
		m.At = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return p.c.nc.Publish("ape.metrics."+Owner+"."+natsconn.SubjectToken(p.c.cfg.Ensemble)+"."+natsconn.SubjectToken(batch+"-"+project), data)
}
