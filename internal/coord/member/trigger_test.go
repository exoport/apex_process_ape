package member

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// PLAN-V15-ASSESSOR step 1: a trigger is published on NATS only, kind
// project (ape.evt.<user>.<project>.project.<M>.<event>), with the message
// id <project>/<checkout-id>/<event>/<M>/<key> — the id an impact notice's
// events[] carries (NOTE-NOTICE-EVENTS-ID) — so the same act's trigger
// published twice is stored once. The control: a project in no ensemble
// publishes nothing.
func TestPublishTrigger(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, events.Declare(ctx, js, 1))

	p := project(t, strings.Replace(block, "nats://127.0.0.1:4222", url, 1), false)
	creds, _ := natstest.MintStandaloneCreds(t, "dana")
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
	m, err := Load(p)
	require.NoError(t, err)
	checkout, err := events.CheckoutID(p.StateDir())
	require.NoError(t, err)

	tr := apexv1.Trigger{Event: apexv1.EventChangeArchived, Milestone: "M1", Change: "add-wave", Commit: "abc123", Paths: []string{"wave.sh"}, Key: "add-wave"}
	PublishTrigger(ctx, p, tr)
	PublishTrigger(ctx, p, tr)
	st, err := js.Stream(ctx, events.EventsStream)
	require.NoError(t, err)
	info, err := st.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), info.State.Msgs, "one act, stored once")

	subj := events.Subject("dana", m.Project, events.KindProject, "M1", apexv1.EventChangeArchived)
	msg, err := st.GetLastMsgForSubject(ctx, subj)
	require.NoError(t, err, "on %s", subj)
	require.Equal(t, m.Project+"/"+checkout+"/change-archived/M1/add-wave", msg.Header.Get(jetstream.MsgIDHeader))
	var got map[string]any
	require.NoError(t, json.Unmarshal(msg.Data, &got))
	require.Equal(t, "add-wave", got["change"])
	require.Equal(t, m.Project, got["project"])
	require.NotEmpty(t, got["at"])

	before := info.State.Msgs
	PublishTrigger(ctx, project(t, "project_name: x\n", false), tr)
	info, err = st.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, before, info.State.Msgs, "control: a project in no ensemble publishes nothing")
}
