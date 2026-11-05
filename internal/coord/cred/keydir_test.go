package cred_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/events"
)

// D813/D814 (`ape nats mint`): a key directory is a working trust root —
// a nats-server started from its resolver.conf accepts the minted admin
// and runs JetStream for APP; each kind's JWT carries exactly its cred
// grant; and the server enforces it (a member publishes its own event and
// is refused another's). The controls: a second init, a reserved token
// and a token already minted are refused; a re-mint of the same name is
// not.
func TestKeyDir_MintsWorkingCredentials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, cred.InitKeyDir(dir))
	require.ErrorContains(t, cred.InitKeyDir(dir), "already holds an operator")
	for _, f := range []string{"operator.nk", "sys.nk", "app.nk", cred.ResolverConf} {
		st, err := os.Stat(filepath.Join(dir, f))
		require.NoError(t, err, f)
		require.Equal(t, os.FileMode(0o600), st.Mode().Perm(), f)
	}

	// A server config includes it relative to itself, as natslocal's do.
	conf := filepath.Join(dir, "n.conf")
	require.NoError(t, os.WriteFile(conf, []byte("listen: 127.0.0.1:-1\njetstream { store_dir: \""+filepath.ToSlash(t.TempDir())+"\" }\ninclude "+cred.ResolverConf+"\n"), 0o600))
	opts, err := server.ProcessConfigFile(conf)
	require.NoError(t, err)
	srv, err := server.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(10*time.Second))

	mint := func(s cred.UserSpec) string {
		t.Helper()
		data, err := cred.MintUserIn(dir, s)
		require.NoError(t, err, "%+v", s)
		p := filepath.Join(t.TempDir(), s.Name+".creds")
		require.NoError(t, cred.WritePrivate(p, data))
		return p
	}
	for _, s := range []cred.UserSpec{
		{Kind: "member", Name: "ann", Ensemble: "atomix", Projects: []string{"taskforge"}},
		{Kind: "coordinator", Name: "coord-2a", Ensemble: "atomix"},
		{Kind: "supervisor", Name: "sup-2a"},
		{Kind: "ghapp", Name: "apex-ghapp-2a"},
		{Kind: "ci", Name: "ci", Projects: []string{"taskforge"}},
		{Kind: "conductor", Name: "ann-conductor", Ensemble: "atomix", Projects: []string{"taskforge"}},
	} {
		p := mint(s)
		raw, err := os.ReadFile(p)
		require.NoError(t, err)
		tok, err := jwt.ParseDecoratedJWT(raw)
		require.NoError(t, err)
		uc, err := jwt.DecodeUserClaims(tok)
		require.NoError(t, err)
		g, err := cred.GrantFor(s)
		require.NoError(t, err)
		require.Equal(t, s.Name, uc.Name)
		require.ElementsMatch(t, g.PubAllow, uc.Pub.Allow, "%s: exactly its grant's publish list", s.Kind)
		require.ElementsMatch(t, g.SubAllow, uc.Sub.Allow, "%s: exactly its grant's subscribe list", s.Kind)
		require.Equal(t, g.AllowResponses, uc.Resp != nil, s.Kind)
	}

	admin := mint(cred.UserSpec{Kind: "admin", Name: "admin"})
	anc, err := nats.Connect(srv.ClientURL(), nats.UserCredentials(admin))
	require.NoError(t, err)
	defer anc.Close()
	ajs, err := jetstream.New(anc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, events.Declare(ctx, ajs, 1), "APP has JetStream")

	errs := make(chan error, 4)
	annCreds := mint(cred.UserSpec{Kind: "member", Name: "ann", Ensemble: "atomix", Projects: []string{"taskforge"}})
	ann, err := nats.Connect(srv.ClientURL(), nats.UserCredentials(annCreds), nats.CustomInboxPrefix("_INBOX.ann"),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err }))
	require.NoError(t, err, "control: a re-mint of the same name")
	defer ann.Close()
	js, err := jetstream.New(ann)
	require.NoError(t, err)
	_, err = js.Publish(ctx, "ape.evt.ann.taskforge.project.M1.gate-green", []byte("{}"))
	require.NoError(t, err, "her own event is stored")
	require.NoError(t, ann.Publish("ape.evt.bob.taskforge.project.M1.gate-green", []byte("{}")))
	select {
	case err := <-errs:
		require.ErrorContains(t, err, "ape.evt.bob.taskforge")
	case <-time.After(3 * time.Second):
		t.Fatal("another's event was not refused")
	}

	_, err = cred.MintUserIn(dir, cred.UserSpec{Kind: "member", Name: "Ann", Ensemble: "atomix", Projects: []string{"taskforge"}})
	require.ErrorContains(t, err, "already", "a token already minted")
	_, err = cred.MintUserIn(dir, cred.UserSpec{Kind: "supervisor", Name: "sup"})
	require.ErrorContains(t, err, "reserved")
	_, err = cred.MintUserIn(dir, cred.UserSpec{Kind: "member", Name: "carl"})
	require.ErrorContains(t, err, "needs one project")
	_, err = cred.MintUserIn(dir, cred.UserSpec{Kind: "root", Name: "x"})
	require.ErrorContains(t, err, "not one of")
}
