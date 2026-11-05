package ghapp

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

func perms(g aped.Grant) natstest.Perms {
	return natstest.Perms{PubAllow: g.PubAllow, PubDeny: g.PubDeny, SubAllow: g.SubAllow, SubDeny: g.SubDeny, AllowResponses: g.AllowResponses}
}

// V12g (R4, G4/G5): the App service serves under exactly cred.GHAppGrant
// on an enforcing server — a member's token and an operator's set-up act
// answered, on the members' own inboxes, with nothing refused — and its
// grant is no wider: no stream administration, no event or lease
// publish, no request of its own, no subscription past its two subjects
// and its inbox.
func TestService_UnderItsGrant(t *testing.T) {
	key, pemBytes := appKey(t)
	f := newFake(t, &key.PublicKey)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(keyPath, pemBytes, 0o600))
	s, err := NewService(&Config{
		API:    srv.URL,
		Warden: AppConfig{AppID: 42, Key: keyPath, Installations: map[string]int64{"shop/notes": 8}},
		Pusher: AppConfig{AppID: 42, Key: keyPath, Installations: map[string]int64{"shop/notes": 7}},
		Repos:  map[string]RepoPolicy{"shop/notes": {Members: []string{"dana"}, Operators: []string{"dana"}}},
		Log:    filepath.Join(dir, "acts.jsonl"),
	})
	require.NoError(t, err)

	e := natstest.StartEnforced(t)
	svcCreds := e.Mint(t, "apex-ghapp", perms(cred.GHAppGrant("apex-ghapp")))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: e.URL, CredsFile: svcCreds}, "ape-ghapp")
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	go func() { _ = s.Serve(ctx, nc) }()

	dana := e.Mint(t, "dana", perms(cred.MemberGrant("dana", "notes", "shop")))
	cli, err := natsconn.Connect(ctx, natsconn.Config{URL: e.URL, CredsFile: dana}, "ape-ghapp-client")
	require.NoError(t, err)
	t.Cleanup(cli.Close)
	ask := func(subject string, req Request) (Reply, error) {
		actx, acancel := context.WithTimeout(ctx, 5*time.Second)
		defer acancel()
		return Ask(actx, cli, subject, "dana", req)
	}
	require.Eventually(t, func() bool {
		_, err := ask(TokenSubject, Request{Repo: "shop/notes", Purpose: "push"})
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "a member's token, answered under the service's grant")
	_, err = ask(SetupSubject, Request{Repo: "shop/notes", Act: "rulesets"})
	require.NoError(t, err, "an operator's set-up act")

	inboxOpt := []nats.Option{nats.CustomInboxPrefix(natsconn.InboxPrefix("apex-ghapp"))}
	e.RequireRefused(t, svcCreds, inboxOpt,
		[]string{
			"$JS.API.STREAM.PURGE.APE_EVENTS", "$JS.API.STREAM.DELETE.KV_ape-leases", "$JS.API.STREAM.UPDATE.APE_EVENTS", "$JS.API.INFO",
			"ape.evt.dana.notes.project.M1.milestone-paused", "$KV.ape-leases.milestone.notes.M1", "ape.sup.ensure", "_INBOX.dana.x", "ape.sup.ghapp.pusher.dana",
		},
		[]string{"_INBOX.>", "_INBOX.dana.>", "ape.>", "ape.evt.>", "ape.sup.ensure"})
}

// V12g: the CI publisher (`ape ci event`) publishes its projects' events
// under its own token, and nothing else.
func TestCIGrant_NoWider(t *testing.T) {
	e := natstest.StartEnforced(t)
	ci := e.Mint(t, "ci", perms(cred.CIGrant("ci", "notes")))
	e.RequireRefused(t, ci, []nats.Option{nats.CustomInboxPrefix(natsconn.InboxPrefix("ci"))},
		[]string{
			"ape.evt.dana.notes.project.M1.merged", "ape.evt.ci.other.project.M1.merged", "ape.evt.coord.shop.coord.x.impact-notice",
			"$KV.ape-leases.milestone.notes.M1", "$JS.API.STREAM.PURGE.APE_EVENTS", "$JS.API.INFO", "ape.sup.ghapp.pusher.ci", "ape.sup.ensure",
		},
		[]string{"_INBOX.>", "ape.>", "ape.sup.ghapp.>"})
	empty := cred.CIGrant("ci")
	require.NotEmpty(t, empty.PubAllow, "no projects is not 'no restriction'")
}
