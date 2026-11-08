//go:build unix

package apecmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// FINDING-2A-DRY-1 (eval's 2a dry run, D816): attach named its conductor
// by git user.email's slug, while its credential is ann's, so an
// enforcing cluster refused its lease claim and attach exited after 30 s.
// The T1, under exactly cred.MemberGrant on an enforcing server: with no
// --user, attach runs as the credential's user — it holds
// conductor.ann.<ens> and binds conductor-ann-<ens>, with nothing refused —
// and a --user that is not the credential's refuses at once (exit 2). The
// control: a --user that is the credential's runs.
func TestConductorAttach_RunsAsTheCredentialsUser(t *testing.T) {
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	e := natstest.StartEnforced(t)
	anc, err := nats.Connect(e.URL, nats.UserCredentials(e.Admin))
	require.NoError(t, err)
	t.Cleanup(anc.Close)
	ajs, err := jetstream.New(anc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, lease.Declare(ctx, ajs, 1))
	require.NoError(t, events.Declare(ctx, ajs, 1))
	ls, err := lease.Open(ctx, ajs)
	require.NoError(t, err)
	require.NoError(t, ls.SetEpoch(ctx, 1))

	root := v1Repo(t)
	v1Git(t, root, "config", "user.email", "diegos@exo.com.ar") // a git identity that is not the credential's
	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  integration: trunk\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: " + e.URL + "\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), []byte("x"), 0o600))
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	m, err := member.Load(p)
	require.NoError(t, err)
	g := cred.MemberGrant("ann", m.Project, m.Ensemble)
	data, err := os.ReadFile(e.Mint(t, "ann", natstest.Perms{PubAllow: g.PubAllow, SubAllow: g.SubAllow}))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))

	start := time.Now()
	_, stderr, err := v1Exec(t, newConductorCmd, "attach", "--project", root, "--pane", "%1", "--user", "diegos-exo-com-ar")
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "%s", stderr)
	require.ErrorContains(t, err, "the credential")
	require.ErrorContains(t, err, "is ann's")
	require.Less(t, time.Since(start), time.Second, "refused at once")

	for _, args := range [][]string{{}, {"--user", "ann"}} {
		cmd := newConductorCmd()
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		var errb bytes.Buffer
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&errb)
		cmd.SetArgs(append([]string{"attach", "--project", root, "--pane", "%1"}, args...))
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- cmd.ExecuteContext(rctx) }()
		require.Eventually(t, func() bool {
			h, _, err := ls.Holder(ctx, "conductor.ann.shop")
			if err != nil || h == "" {
				return false
			}
			_, err = ajs.Consumer(ctx, events.EventsStream, "conductor-ann-shop")
			return err == nil
		}, 15*time.Second, 100*time.Millisecond, "%v: holds conductor.ann.shop and binds conductor-ann-shop; stderr %s", args, errb.String())
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err, "%v: Ctrl-C stops it, exit 0", args)
		case <-time.After(15 * time.Second):
			t.Fatalf("%v: attach did not stop", args)
		}
		require.Contains(t, errb.String(), "attach: ann's conductor", "%v", args)
		require.NotContains(t, errb.String(), "diegos-exo-com-ar", "%v: not the git slug", args)
	}

	// The control: a credential that names nobody (an unenforced
	// cluster's) keeps git user.email's slug, as before.
	url := natstest.RunJetStreamServer(t)
	unc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(unc.Close)
	ujs, err := jetstream.New(unc)
	require.NoError(t, err)
	require.NoError(t, lease.Declare(ctx, ujs, 1))
	require.NoError(t, events.Declare(ctx, ujs, 1))
	uls, err := lease.Open(ctx, ujs)
	require.NoError(t, err)
	require.NoError(t, uls.SetEpoch(ctx, 1))
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(strings.Replace(block, e.URL, url, 1))...), 0o644))
	anon, _ := natstest.MintStandaloneCreds(t, "")
	data, err = os.ReadFile(anon)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), data, 0o600))
	cmd := newConductorCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var errb bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"attach", "--project", root, "--pane", "%1"})
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(rctx) }()
	require.Eventually(t, func() bool {
		h, _, err := uls.Holder(ctx, "conductor.diegos-exo-com-ar.shop")
		return err == nil && h != ""
	}, 15*time.Second, 100*time.Millisecond, "control: git's slug; stderr %s", errb.String())
	cancel()
	<-done
}
