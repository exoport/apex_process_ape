package member

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// appService is a stand-in App service: it answers token requests,
// refusing while refuse is set, until stopped.
type appService struct {
	nc     *nats.Conn
	sub    *nats.Subscription
	refuse atomic.Bool
}

func (a *appService) start(t *testing.T) {
	t.Helper()
	var err error
	a.sub, err = a.nc.Subscribe(ghapp.TokenSubject+".*", func(m *nats.Msg) {
		var r ghapp.Request
		_ = json.Unmarshal(m.Data, &r)
		rep := ghapp.Reply{OK: true, Token: "ghs_" + r.Purpose}
		if a.refuse.Load() {
			rep = ghapp.Reply{Error: "repository not served"}
		}
		data, _ := json.Marshal(rep)
		_ = m.Respond(data)
	})
	require.NoError(t, err)
	require.NoError(t, a.nc.Flush())
}

func (a *appService) stop(t *testing.T) {
	t.Helper()
	require.NoError(t, a.sub.Unsubscribe())
	require.NoError(t, a.nc.Flush())
}

// appRig is an App-service project (coordination.github.app) on a
// three-node cluster, and an App service answering on it.
func appRig(t *testing.T) (*apexv1.Project, *natstest.Cluster, *appService) {
	t.Helper()
	c := natstest.RunCluster(t, 3)
	nc, err := nats.Connect(c.URLs(), nats.MaxReconnects(-1), nats.ReconnectWait(100*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	a := &appService{nc: nc}
	a.start(t)
	app := strings.Replace(queued(block), "nats://127.0.0.1:4222", c.URLs(), 1) + "  github:\n    repo: shop/notes\n    app: true\n"
	p := project(t, app, false)
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
	return p, c, a
}

// V8L-1 (research, D808+): the token a push, a fetch or a PR call asks
// the App service for after its fence waits for NATS inside a run, as the
// fence does. The T1: every node down when the push token is asked for,
// under WithRunWait — it says it waits, and the token comes once NATS is
// back; the same with NATS up and no App service answering, until it
// does. The controls: outside a run each refuses at once
// (coord.nats-unreachable); a reply that refuses is returned at once in a
// run too.
func TestGitVia_TheTokenWaitsForNATSInARun(t *testing.T) {
	p, c, app := appRig(t)
	ctx := t.Context()
	defer func(d time.Duration) { gateWaitEvery = d }(gateWaitEvery)
	gateWaitEvery = 200 * time.Millisecond
	_, _, err := GitVia(ctx, p, "push")
	require.NoError(t, err, "NATS and the service up: the token comes")

	waits := func(name string, down, up func()) {
		t.Helper()
		down()
		start := time.Now()
		_, _, err := GitVia(ctx, p, "push")
		require.True(t, Transient(err), "%s, control: outside a run it refuses at once: %v", name, err)
		require.Less(t, time.Since(start), 10*time.Second)

		var out syncBuf
		done := make(chan error, 1)
		go func() {
			_, env, err := GitVia(WithRunWait(ctx, time.Minute, &out), p, "push")
			if err == nil && len(env) == 0 {
				err = os.ErrNotExist
			}
			done <- err
		}()
		require.Eventually(t, func() bool {
			return strings.Contains(out.String(), "the push token from the App service: waiting for NATS")
		},
			15*time.Second, 50*time.Millisecond, "%s: says it waits", name)
		select {
		case err := <-done:
			t.Fatalf("%s: returned during the outage: %v", name, err)
		default:
		}
		up()
		select {
		case err := <-done:
			require.NoError(t, err, "%s: the token comes once it is back", name)
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: still waiting after it came back", name)
		}
	}
	waits("NATS down", func() {
		for i := range 3 {
			c.Stop(i)
		}
	}, func() {
		for i := range 3 {
			c.Restart(i)
		}
	})
	waits("no App service", func() { app.stop(t) }, func() { app.start(t) })

	app.refuse.Store(true)
	start := time.Now()
	_, _, err = GitVia(WithRunWait(ctx, time.Minute, nil), p, "push")
	require.ErrorContains(t, err, "repository not served")
	require.Less(t, time.Since(start), 5*time.Second, "control: a refusal is returned at once, in a run too")
}

// V8LM2-1 (research's note on 1bb321e): the token wait is for NATS or the
// App service being away, not for a credential the server refuses. In a
// run, a credential of another account (rejected at connect) and one whose
// grant does not cover the token subject (the request refused, so never
// answered) each refuse at once rather than wait the bound. The control: a
// grant that covers it, with no App service answering, is transient (and
// waits in a run: TestGitVia_TheTokenWaitsForNATSInARun).
func TestGitHubTokenFor_ARefusedCredentialDoesNotWait(t *testing.T) {
	e, _ := enforcedCluster(t)
	app := queued(block) + "  github:\n    repo: shop/notes\n    app: true\n"
	run := WithRunWait(context.Background(), time.Minute, io.Discard)
	refusesAtOnce := func(name string, p *apexv1.Project, want string) {
		t.Helper()
		m, err := Load(p)
		require.NoError(t, err)
		start := time.Now()
		_, err = m.GitHubTokenFor(run, "push")
		require.ErrorContains(t, err, want, name)
		require.Less(t, time.Since(start), 10*time.Second, "%s: refused at once, not waited out", name)
	}

	// A credential of another account: the server rejects the connection.
	stranger := project(t, strings.Replace(app, "nats://127.0.0.1:4222", e.URL, 1), false)
	creds, _ := natstest.MintStandaloneCreds(t, "ann")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(stranger.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stranger.Root, "hot", "ape-nats.creds"), data, 0o600))
	refusesAtOnce("rejected credential", stranger, "uthorization")

	// A grant without the token subject: the request is refused.
	narrow := project(t, strings.Replace(app, "nats://127.0.0.1:4222", e.URL, 1), false)
	g := cred.MemberGrant("ann", "notes-svc", "shop")
	var pub []string
	for _, s := range g.PubAllow {
		if !strings.HasPrefix(s, ghapp.TokenSubject) {
			pub = append(pub, s)
		}
	}
	g.PubAllow = pub
	data, err = os.ReadFile(e.Mint(t, "ann", perms(g)))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(narrow.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(narrow.Root, "hot", "ape-nats.creds"), data, 0o600))
	e.Expect(`"` + ghapp.TokenSubject + `.ann"`)
	refusesAtOnce("denied token subject", narrow, "may not ask the App service")

	// The control: covered, but nobody answers — transient.
	ann := enforcedCheckout(t, e, app)
	m, err := Load(ann)
	require.NoError(t, err)
	_, err = m.GitHubTokenFor(context.Background(), "push")
	require.Error(t, err)
	require.True(t, askUnanswered(err), "no App service: unanswered, which a run waits for: %v", err)
}
