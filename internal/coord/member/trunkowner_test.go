package member

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/layout"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

// checkoutOf is checkout for project name's block cfg (block's, with its
// NATS URL), user's credential in its hot/.
func checkoutOf(t *testing.T, url, user, name, cfg string) *apexv1.Project {
	t.Helper()
	cfg = strings.Replace(strings.Replace(cfg, "nats://127.0.0.1:4222", url, 1), "project_name: Notes Svc", "project_name: "+name, 1)
	p := project(t, cfg, false)
	creds, _ := natstest.MintStandaloneCreds(t, user)
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
	return p
}

func ownerOf(t *testing.T, p *apexv1.Project) string {
	t.Helper()
	s, err := openMilestones(context.Background(), p)
	require.NoError(t, err)
	defer s.close()
	cur, _, err := s.store.Holder(context.Background(), layout.TrunkOwnerKey(s.m.Project))
	require.NoError(t, err)
	return cur
}

// D876 (c): a trunk project is one person's. The first claim records its
// claimant's user as the owner; another person's claim is
// coord.trunk-one-member, naming the owner and queue/*; the owner's second
// checkout claims as before. A queue project has no owner key.
func TestTrunkOwner(t *testing.T) {
	ctx := context.Background()
	url := leaseCluster(t)
	ann, ann2, bob := checkout(t, url, "ann"), checkout(t, url, "ann"), checkout(t, url, "bob")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	require.Equal(t, "ann", ownerOf(t, ann))

	_, err = ClaimMilestone(ctx, bob, "M2")
	require.Equal(t, apexv1.CodeTrunkOneMember, leaseCode(t, err))
	require.ErrorContains(t, err, "owned by ann")
	require.ErrorContains(t, err, "integration: queue")
	require.Equal(t, "ann", ownerOf(t, ann), "the refusal moves nothing")
	_, err = HoldMilestone(ctx, bob, "M2", nil)
	require.Error(t, err, "nor does bob's loop take one")

	_, err = ClaimMilestone(ctx, ann2, "M2")
	require.NoError(t, err, "the owner's second checkout claims")

	// The control: a queue project (here through the App, so no token
	// file) writes no owner key, and another person claims.
	app := queuedBlock + "  github:\n    repo: shop/notes\n    app: true\n"
	q := checkoutOf(t, url, "ann", "queue-svc", app)
	s, err := openMilestones(ctx, q)
	require.NoError(t, err)
	defer s.close()
	require.Equal(t, apexv1.ModeQueuePoll, s.m.Mode)
	require.NoError(t, s.ownTrunk(ctx))
	cur, _, err := s.store.Holder(ctx, layout.TrunkOwnerKey(s.m.Project))
	require.NoError(t, err)
	require.Empty(t, cur, "no owner key in a queue project")
}

// The race (research's DIM point 2): two people's first claims at once —
// exactly one creates the owner key and claims, the other is refused,
// every time.
func TestTrunkOwner_RaceHasOneWinner(t *testing.T) {
	ctx := context.Background()
	url := leaseCluster(t)
	for i := range 50 {
		name := fmt.Sprintf("race-%d", i)
		ann, bob := checkoutOf(t, url, "ann", name, block), checkoutOf(t, url, "bob", name, block)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, p := range []*apexv1.Project{ann, bob} {
			wg.Go(func() { _, errs[j] = ClaimMilestone(ctx, p, fmt.Sprintf("M%d", j+1)) })
		}
		wg.Wait()
		var won, refused int
		for _, err := range errs {
			if err == nil {
				won++
				continue
			}
			fe, ok := errors.AsType[*apexv1.FailError](err)
			require.True(t, ok, "round %d: %v", i, err)
			require.Equal(t, apexv1.CodeTrunkOneMember, fe.Code, "round %d", i)
			refused++
		}
		require.Equal(t, 1, won, "round %d: %v", i, errs)
		require.Equal(t, 1, refused, "round %d", i)
		owner := ownerOf(t, ann)
		winner := "ann"
		if errs[0] != nil {
			winner = "bob"
		}
		require.Equal(t, winner, owner, "round %d: the owner is the one that claimed", i)
	}
}

// The handover: the owner only, and only holding no live milestone lease
// in the project; after it the new owner claims and the old one is refused.
func TestHandTrunkOwner(t *testing.T) {
	ctx := context.Background()
	url := leaseCluster(t)
	ann, bob := checkout(t, url, "ann"), checkout(t, url, "bob")
	_, err := ClaimMilestone(ctx, ann, "M1")
	require.NoError(t, err)

	_, err = HandTrunkOwner(ctx, bob, "bob", "diegos")
	require.Equal(t, apexv1.CodeTrunkOneMember, leaseCode(t, err))
	require.ErrorContains(t, err, "only the owner hands")
	_, err = HandTrunkOwner(ctx, ann, "bob", "diegos")
	require.Equal(t, apexv1.CodeTrunkOneMember, leaseCode(t, err))
	require.ErrorContains(t, err, "still holds milestone.notes-svc.M1")
	require.Equal(t, "ann", ownerOf(t, ann))

	_, err = ReleaseMilestone(ctx, ann, "M1")
	require.NoError(t, err)
	from, err := HandTrunkOwner(ctx, ann, "bob", "diegos")
	require.NoError(t, err)
	require.Equal(t, "ann", from)
	require.Equal(t, "bob", ownerOf(t, bob))
	_, err = ClaimMilestone(ctx, bob, "M1")
	require.NoError(t, err)
	_, err = ClaimMilestone(ctx, ann, "M2")
	require.Equal(t, apexv1.CodeTrunkOneMember, leaseCode(t, err), "the old owner is refused now")
	require.ErrorContains(t, err, "owned by bob")
}

// (c-1), research: 2c's topology — two people on one queue project — is
// the lease's, never the owner rule's: the second person's claim of a held
// milestone is session.not-lease-holder (lease.ErrHeld), not
// coord.trunk-one-member, and no owner key appears.
func TestTrunkOwner_QueueProjectTwoPeopleOneMilestone(t *testing.T) {
	ctx := context.Background()
	url := leaseCluster(t)
	app := queuedBlock + "  github:\n    repo: shop/notes\n    app: true\n"
	ann, bob := checkoutOf(t, url, "ann", "queue-svc", app), checkoutOf(t, url, "bob", "queue-svc", app)
	s, err := openMilestones(ctx, ann)
	require.NoError(t, err)
	defer s.close()
	l, err := s.claim(ctx, "M1")
	require.NoError(t, err)

	_, err = ClaimMilestone(ctx, bob, "M1")
	require.Equal(t, apexv1.CodeNotLeaseHolder, leaseCode(t, err), "the lease refuses bob, not the owner rule")
	require.ErrorContains(t, err, "M1 is held by "+l.Holder)
	cur, _, err := s.store.Holder(ctx, layout.TrunkOwnerKey(s.m.Project))
	require.NoError(t, err)
	require.Empty(t, cur, "no owner key in a queue project")
	require.NoError(t, s.ownTrunk(ctx), "the owner rule is no queue project's")
}
