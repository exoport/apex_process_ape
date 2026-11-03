package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/slots"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// PLAN-V15-ASSESSOR step 5 (ANSWER-NOTICE-SCHEMA, R5, D762+): notice.json
// is judged whole. Valid: a notice to another open milestone citing a
// snapshot file, one citing only a path a diff in the input changes (an
// open milestone's own files exist only on its branch), the empty answer,
// and a notice to a milestone that has events in a mixed batch. Every
// other case refuses the batch, and the published Rule quotes nothing the
// session wrote.
func TestValidateNotices(t *testing.T) {
	snap := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(snap, "internal", "leader"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(snap, "internal", "leader", "sweep.go"), []byte("package leader\n"), 0o600))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(snap, "link")))
	chk := NoticeCheck{Project: "atomix", Open: []string{"M1", "M2"}, Own: []string{"M2"}, Snapshot: snap, DiffPaths: []string{"docs/configuration.md"}}
	ok := func(body string, c NoticeCheck) []Notice {
		ns, err := ValidateNotices([]byte(body), c)
		require.NoError(t, err, body)
		return ns
	}
	ns := ok(`{"notices":[{"target":"atomix/M1","reason":"B-2 changes the sweep A's gate runs","evidence":["internal/leader/sweep.go"],"hold":true}]}`, chk)
	require.Equal(t, []Notice{{Target: "atomix/M1", Reason: "B-2 changes the sweep A's gate runs", Evidence: []string{"internal/leader/sweep.go"}, Hold: true}}, ns)
	ok(`{"notices":[{"target":"atomix/M1","reason":"r","evidence":["docs/configuration.md"]}]}`, chk) // D762+: a diff-only path
	require.Empty(t, ok(`{"notices": []}`, chk))
	mixed := chk
	mixed.Own = []string{"M1", "M2"}
	ok(`{"notices":[{"target":"atomix/M2","reason":"r","evidence":["internal/leader/sweep.go"]}]}`, mixed)

	long := strings.Repeat("x", 601)
	big := `{"notices":[{"target":"atomix/M1","reason":"` + strings.Repeat("y", 17<<10) + `","evidence":["a"]}]}`
	for name, body := range map[string]string{
		"not json":               `notices`,
		"no notices key":         `{}`,
		"an unknown top key":     `{"notices":[],"extra":1}`,
		"an unknown notice key":  `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["internal/leader/sweep.go"],"why":"x"}]}`,
		"over 16 KiB":            big,
		"two values":             `{"notices":[]} {"notices":[]}`,
		"another project":        `{"notices":[{"target":"other/M1","reason":"r","evidence":["internal/leader/sweep.go"]}]}`,
		"no project":             `{"notices":[{"target":"M1","reason":"r","evidence":["internal/leader/sweep.go"]}]}`,
		"not open":               `{"notices":[{"target":"atomix/M3","reason":"r","evidence":["internal/leader/sweep.go"]}]}`,
		"the batch's own":        `{"notices":[{"target":"atomix/M2","reason":"r","evidence":["internal/leader/sweep.go"]}]}`,
		"two for one target":     `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["internal/leader/sweep.go"]},{"target":"atomix/M1","reason":"s","evidence":["internal/leader/sweep.go"]}]}`,
		"an empty reason":        `{"notices":[{"target":"atomix/M1","reason":" ","evidence":["internal/leader/sweep.go"]}]}`,
		"a reason over 600":      `{"notices":[{"target":"atomix/M1","reason":"` + long + `","evidence":["internal/leader/sweep.go"]}]}`,
		"a control character":    `{"notices":[{"target":"atomix/M1","reason":"a\u0007b","evidence":["internal/leader/sweep.go"]}]}`,
		"a ghp_ token":           `{"notices":[{"target":"atomix/M1","reason":"see ghp_abcdef012345","evidence":["internal/leader/sweep.go"]}]}`,
		"a github_pat_":          `{"notices":[{"target":"atomix/M1","reason":"github_pat_11AAA","evidence":["internal/leader/sweep.go"]}]}`,
		"a ghs_ token":           `{"notices":[{"target":"atomix/M1","reason":"ghs_x","evidence":["internal/leader/sweep.go"]}]}`,
		"a NATS seed":            `{"notices":[{"target":"atomix/M1","reason":"SUAKZ3E5LQ7VJZL6NYHZCF2WQNGVFUVHGIBBSXL","evidence":["internal/leader/sweep.go"]}]}`,
		"a PEM block":            `{"notices":[{"target":"atomix/M1","reason":"-----BEGIN PRIVATE KEY-----","evidence":["internal/leader/sweep.go"]}]}`,
		"no evidence":            `{"notices":[{"target":"atomix/M1","reason":"r","evidence":[]}]}`,
		"nine evidence paths":    `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["a","b","c","d","e","f","g","h","i"]}]}`,
		"an absolute path":       `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["/etc/passwd"]}]}`,
		"a climbing path":        `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["internal/../../x"]}]}`,
		"a path in neither":      `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["internal/leader/nope.go"]}]}`,
		"a link":                 `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["link"]}]}`,
		"a directory":            `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["internal/leader"]}]}`,
		"hold not a boolean":     `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["internal/leader/sweep.go"],"hold":"yes"}]}`,
		"evidence over 200 long": `{"notices":[{"target":"atomix/M1","reason":"r","evidence":["` + strings.Repeat("a", 201) + `"]}]}`,
	} {
		ns, err := ValidateNotices([]byte(body), chk)
		require.ErrorIs(t, err, ErrNoticeRefused, name)
		require.Nil(t, ns, name)
		var ne *NoticeError
		require.ErrorAs(t, err, &ne, name)
		for _, quoted := range []string{"ghp_", "github_pat_", "SUAKZ", "BEGIN", "atomix/M", "sweep.go", "nope.go"} {
			require.NotContains(t, ne.Rule, quoted, "%s: the published rule quotes nothing the session wrote", name)
		}
	}
}

// noticeRig is an embedded JetStream with the streams declared.
func noticeRig(t *testing.T) (*nats.Conn, jetstream.JetStream, *lease.Store) {
	t.Helper()
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, js, 1))
	require.NoError(t, slots.Declare(ctx, js, 1))
	require.NoError(t, events.Declare(ctx, js, 1))
	ls, err := lease.Open(ctx, js)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))
	return nc, js, ls
}

// startAssessing runs a coordinator whose OnBatch hands its Publisher out,
// and makes one batch.
func startAssessing(ctx context.Context, t *testing.T, nc *nats.Conn, js jetstream.JetStream, ls *lease.Store, id string) (Publisher, context.CancelFunc, chan error) {
	t.Helper()
	pubs := make(chan Publisher, 4)
	rctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- Run(rctx, nc, Config{Ensemble: "ens", ID: id, TTL: 3 * time.Second, Renew: 500 * time.Millisecond, Projects: []string{"atomix"}, Assessor: &AssessorConfig{
			Quiet: 200 * time.Millisecond, Ceiling: 2 * time.Second,
			OnBatch: func(_ context.Context, _ Batch, p Publisher) { pubs <- p },
		}})
	}()
	require.Eventually(t, func() bool {
		h, _, err := ls.Holder(ctx, "coord.ens")
		return err == nil && h == id
	}, 10*time.Second, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	_, err := js.Publish(ctx, "ape.evt.dana.atomix.project.M2.change-archived", []byte(`{"key":"b-2"}`), jetstream.WithMsgID(id+"/atomix/c/change-archived/M2/b-2"))
	require.NoError(t, err)
	select {
	case p := <-pubs:
		return p, stop, done
	case <-time.After(10 * time.Second):
		t.Fatal("no batch")
	}
	return nil, stop, done
}

func subjectCount(ctx context.Context, t *testing.T, js jetstream.JetStream, subj string) uint64 {
	t.Helper()
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	info, err := st.Info(ctx, jetstream.WithSubjectFilter(subj))
	require.NoError(t, err)
	return info.State.Subjects[subj]
}

// Step 5's publish (research 3, D3): the coordinator publishes a valid
// batch's notices once, fenced with its own current lease — impact-notice,
// and hold-requested when a notice asks; a replay of the batch publishes
// nothing more (the per-subject dedupe, past any stream window);
// assessor-failed goes out once. A coordinator replaced mid-session
// publishes nothing from its old batch; its replacement publishes the
// batch once.
func TestCoordinator_PublishesNoticesOnceFenced(t *testing.T) {
	nc, js, ls := noticeRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pub, stop1, done1 := startAssessing(ctx, t, nc, js, ls, "c1")

	n := []Notice{{Target: "atomix/M1", Reason: "B-2 changes what A's gate runs", Evidence: []string{"x.go"}, Hold: true}}
	published, dup, err := pub.Notices(ctx, "20261103T030000Z-1", "atomix", []string{"atomix/c/change-archived/M2/b-2"}, n)
	require.NoError(t, err)
	require.Equal(t, []string{"20261103T030000Z-1-atomix-M1"}, published)
	require.Empty(t, dup)
	notice := "ape.evt.coord.ens.coord.20261103T030000Z-1-atomix-M1.impact-notice"
	hold := "ape.evt.coord.ens.coord.20261103T030000Z-1-atomix-M1.hold-requested"
	require.Equal(t, uint64(1), subjectCount(ctx, t, js, notice))
	require.Equal(t, uint64(1), subjectCount(ctx, t, js, hold))
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	m, err := st.GetLastMsgForSubject(ctx, notice)
	require.NoError(t, err)
	require.Contains(t, string(m.Data), `"events":["atomix/c/change-archived/M2/b-2"]`)
	require.Contains(t, string(m.Data), `"target":"atomix/M1"`)

	// Past the stream's dedupe window (shrunk here from 10 min to 100 ms),
	// the per-subject check is what keeps a replay out.
	info, err := st.Info(ctx)
	require.NoError(t, err)
	scfg := info.Config
	scfg.Duplicates = 100 * time.Millisecond
	_, err = js.UpdateStream(ctx, scfg)
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)
	published, dup, err = pub.Notices(ctx, "20261103T030000Z-1", "atomix", nil, n)
	require.NoError(t, err)
	require.Empty(t, published, "a replayed batch publishes nothing more")
	require.Equal(t, []string{"20261103T030000Z-1-atomix-M1"}, dup)
	require.Equal(t, uint64(1), subjectCount(ctx, t, js, notice))

	require.NoError(t, pub.Failed(ctx, "20261103T030000Z-2", "atomix", "notice.json refused: a reason matches a secret pattern", nil))
	require.NoError(t, pub.Failed(ctx, "20261103T030000Z-2", "atomix", "again", nil))
	require.Equal(t, uint64(1), subjectCount(ctx, t, js, "ape.evt.coord.ens.coord.20261103T030000Z-2-atomix.assessor-failed"), "once per batch")

	// Replaced mid-session: c1 stops (its lease goes), c2 takes the ensemble.
	stop1()
	<-done1
	pub2, stop2, done2 := startAssessing(ctx, t, nc, js, ls, "c2")
	defer func() { stop2(); <-done2 }()
	_, _, err = pub.Notices(ctx, "20261103T030000Z-3", "atomix", nil, []Notice{{Target: "atomix/M1", Reason: "r", Evidence: []string{"x.go"}}})
	require.ErrorIs(t, err, ErrFenced, "the old coordinator's batch publishes nothing")
	require.ErrorIs(t, pub.Failed(ctx, "20261103T030000Z-3", "atomix", "x", nil), ErrFenced)
	old := "ape.evt.coord.ens.coord.20261103T030000Z-3-atomix-M1.impact-notice"
	require.Zero(t, subjectCount(ctx, t, js, old))
	published, _, err = pub2.Notices(ctx, "20261103T030000Z-3", "atomix", nil, []Notice{{Target: "atomix/M1", Reason: "r", Evidence: []string{"x.go"}}})
	require.NoError(t, err)
	require.Len(t, published, 1, "the replacement publishes the batch once")
	require.Equal(t, uint64(1), subjectCount(ctx, t, js, old))
}

// S5-1: a renew between fence's copy of the lease and its check moves the
// key's revision; the coordinator still holds its lease, so its publish
// goes out (the copy is refreshed and checked once more). The control: a
// lease another coordinator took stays fenced (TestCoordinator_
// PublishesNoticesOnceFenced's replacement case).
func TestPublisher_RenewBetweenCopyAndFencePublishes(t *testing.T) {
	nc, js, ls := noticeRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pub, stop, done := startAssessing(ctx, t, nc, js, ls, "c1")
	defer func() { stop(); <-done }()
	pp, ok := pub.(publisher)
	require.True(t, ok)
	c := pp.c
	renewed := 0
	afterHeldCopy = func() {
		if renewed > 0 {
			return
		}
		renewed++
		c.mu.Lock()
		l, err := c.leases.Renew(ctx, c.held)
		require.NoError(t, err)
		c.held = l
		c.mu.Unlock()
	}
	t.Cleanup(func() { afterHeldCopy = nil })
	published, _, err := pub.Notices(ctx, "20261103T050000Z-1", "atomix", nil, []Notice{{Target: "atomix/M1", Reason: "r", Evidence: []string{"x.go"}}})
	require.NoError(t, err, "its own renew does not fence the coordinator out")
	require.Len(t, published, 1)
	require.Equal(t, 1, renewed)
}
