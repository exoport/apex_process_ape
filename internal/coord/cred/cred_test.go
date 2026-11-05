package cred

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/aped"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// R1: the minter refuses a member whose token is the coordinator's, a
// supervisor's or a VM's.
func TestMint_RefusesReservedTokens(t *testing.T) {
	acct, err := aped.NewAccount()
	require.NoError(t, err)
	for _, name := range []string{"coord", "SUP", "vm-7", "Coord"} {
		_, err := Mint(acct, name, "taskforge", "atomix", time.Hour)
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "reserved")
	}
	creds, err := Mint(acct, "Ann Lee", "taskforge", "atomix", time.Hour)
	require.NoError(t, err)
	require.Contains(t, string(creds), "BEGIN NATS USER JWT")

	// V12-5: a name whose token another minted name has already.
	_, err = Mint(acct, "ann.lee", "taskforge", "atomix", time.Hour, "Ann Lee", "bob")
	require.ErrorContains(t, err, `"ann-lee" is "Ann Lee"'s already`)
	_, err = Mint(acct, "Ann Lee", "taskforge", "atomix", time.Hour, "Ann Lee", "bob")
	require.NoError(t, err, "control: the same name again (a re-mint) is fine")
	_, err = Mint(acct, "carl", "taskforge", "atomix", time.Hour, "Ann Lee", "bob")
	require.NoError(t, err, "control: a token nobody has")
}

// §4.6 (D546), server-enforced: a member writes its own conductor lease and
// its project's milestone and close leases, never another user's
// conductor key or another project's lease (V12), and publishes only its
// own events.
func TestMemberGrant_ServerEnforced(t *testing.T) {
	url, acct := natstest.StartOperator(t)
	g := MemberGrant("ann", "taskforge", "atomix")
	path, _ := natstest.MintCreds(t, acct, "ann", g.PubAllow, g.SubAllow)

	var mu sync.Mutex
	var got []string
	nc, err := nats.Connect(url, nats.UserCredentials(path), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		mu.Lock()
		got = append(got, err.Error())
		mu.Unlock()
	}))
	require.NoError(t, err)
	defer nc.Close()

	allowed := []string{
		"$KV.ape-leases.conductor.ann.atomix",
		"$KV.ape-leases.milestone.taskforge.M2",
		"$KV.ape-leases.close.taskforge",
		"ape.evt.ann.taskforge.project.M2.person",
		"ape.metrics.ann.taskforge.run",
	}
	denied := []string{
		"$KV.ape-leases.conductor.bob.atomix",
		"$KV.ape-leases.sup.leader",
		"$KV.ape-leases.coord.atomix",
		"$KV.ape-leases.steward.atomix",
		"$KV.ape-leases.milestone.other.M1",
		"$KV.ape-leases.close.other",
		"$KV.ape-slots.slot.h.port.0",
		"$KV.ape-meta.epoch",
		"ape.evt.bob.taskforge.project.M2.person",
		"ape.evt.coord.atomix.coord.x.lease-expired",
		"ape.evt.ann.other.project.M1.person",
	}
	for _, s := range append(allowed, denied...) {
		require.NoError(t, nc.Publish(s, []byte("x")))
	}
	require.NoError(t, nc.Flush())
	time.Sleep(300 * time.Millisecond)
	nc.Close()
	mu.Lock()
	joined := strings.Join(got, "\n")
	mu.Unlock()
	for _, s := range denied {
		require.Contains(t, joined, s, "publishing to %s must be refused by the server", s)
	}
	for _, s := range allowed {
		require.NotContains(t, joined, `"`+s+`"`, "publishing to %s is allowed", s)
	}
}
