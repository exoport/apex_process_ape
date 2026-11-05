package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// D814: `ape nats mint init|user` through the CLI — the key directory, a
// member's credential 0600, usage for a missing scope, a refusal for a
// taken token; and `ape nats consumer-info` reads a durable's config.
func TestNatsMint_CLI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	_, _, err := v1Exec(t, newNatsCmd, "mint", "init", "--dir", dir)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "ann.creds")
	stdout, _, err := v1Exec(t, newNatsCmd, "mint", "user", "--dir", dir, "--kind", "member", "--name", "ann", "--project", "notes", "--ensemble", "shop", "--out", out)
	require.NoError(t, err, stdout)
	st, err := os.Stat(out)
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // POSIX modes; Windows has none to check
		require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	}
	_, _, err = v1Exec(t, newNatsCmd, "mint", "user", "--dir", dir, "--kind", "member", "--name", "bob", "--out", out+"2")
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "a member needs its project and ensemble")
	_, _, err = v1Exec(t, newNatsCmd, "mint", "user", "--dir", dir, "--kind", "member", "--name", "Ann", "--project", "notes", "--ensemble", "shop", "--out", out+"3")
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), "a token already minted")
}

func TestNatsConsumerInfo_CLI(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))
	_, err = js.CreateOrUpdateConsumer(ctx, events.EventsStream, jetstream.ConsumerConfig{Durable: "conductor-ann-shop", AckPolicy: jetstream.AckExplicitPolicy})
	require.NoError(t, err)
	stdout, _, err := v1Exec(t, newNatsCmd, "consumer-info", events.EventsStream, "conductor-ann-shop", "--nats-url", url)
	require.NoError(t, err)
	var info jetstream.ConsumerInfo
	require.NoError(t, json.Unmarshal([]byte(stdout), &info), stdout)
	require.Equal(t, "conductor-ann-shop", info.Config.Durable)
	require.Equal(t, jetstream.AckExplicitPolicy, info.Config.AckPolicy)
	_, _, err = v1Exec(t, newNatsCmd, "consumer-info", events.EventsStream, "nobody", "--nats-url", url)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), "control: no such consumer")
}
