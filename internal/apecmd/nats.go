package apecmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/lease"
	"github.com/exoport/apex_process_ape/internal/coord/rebuild"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

func newNatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nats",
		Short: "APEX v1.5 (in development): declare the coordination layout, open a new epoch, replay this checkout's records",
		Long: `APEX v1.5's rebuild (stage 0; docs/reference/coordination.md): after a NATS cluster
loses its data, the supervisor runs ` + "`ape nats declare --new-epoch`" + `, every member
checkout runs ` + "`ape nats replay`" + `, and each lease is re-claimed by its last holder's
token. Both connect with --nats-url/--nats-creds, or with the project's coordination:
block and hot/ape-nats.creds.`,
	}
	cmd.AddCommand(newNatsDeclareCmd(), newNatsReplayCmd())
	return cmd
}

func newNatsDeclareCmd() *cobra.Command {
	var f coordFlags
	var replicas int
	var newEpoch bool
	var epochFile string
	var window time.Duration
	cmd := &cobra.Command{
		Use:   "declare [--replicas 3] [--new-epoch --epoch-file <path> [--window 2m]]",
		Short: "Declare every bucket, stream and object store of the layout (idempotent); with --new-epoch, open the next epoch",
		Long: `Declare every KV bucket, stream and object store of the coordination layout, creating
what is missing and leaving what exists (§4.4 step 1). With --new-epoch, also write the
epoch after the higher of --epoch-file (the supervisor's local copy) and the cluster's,
to both, and open the re-claim window for --window: leases are re-claimed by the
highest revision of the previous epoch until it closes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if newEpoch && epochFile == "" {
				return usageErr(errors.New("--new-epoch needs --epoch-file: the supervisor's local copy of the epoch, which outlives the cluster (usage.epoch-file)"))
			}
			ctx := cmd.Context()
			nc, _, err := f.connect(ctx, "ape-nats-declare")
			if err != nil {
				return err
			}
			defer nc.Close()
			js, err := jetstream.New(nc)
			if err != nil {
				return err
			}
			if err := rebuild.Declare(ctx, js, replicas); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "declared the coordination layout (replicas %d)\n", replicas)
			if !newEpoch {
				return nil
			}
			ls, err := lease.Open(ctx, js)
			if err != nil {
				return err
			}
			e, err := rebuild.NewEpoch(ctx, js, ls, rebuild.EpochFile(epochFile), window)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "epoch %d; re-claim window open for %s\n", e, window)
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().IntVar(&replicas, "replicas", 3, "replicas of every bucket and stream")
	cmd.Flags().BoolVar(&newEpoch, "new-epoch", false, "open the next epoch after declaring (the supervisor's step after a rebuild)")
	cmd.Flags().StringVar(&epochFile, "epoch-file", "", "the supervisor's local copy of the epoch (with --new-epoch)")
	cmd.Flags().DurationVar(&window, "window", 2*time.Minute, "how long the re-claim window stays open (with --new-epoch)")
	return cmd
}

func newNatsReplayCmd() *cobra.Command {
	var f coordFlags
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Write this checkout's own events, run manifests and attempt counts back to NATS (idempotent)",
		Long: `Write back what this checkout alone wrote (§4.4 step 2): its events.jsonl lines, by
message id <project>/<checkout-id>/<seq>, so a second replay, or one racing a live
publish, stores each once; its run manifests and attempt counts, by their ids. Run in
a project with a coordination: block; the <user> token is the credential's.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			nc, m, err := f.connect(ctx, "ape-nats-replay")
			if err != nil {
				return err
			}
			defer nc.Close()
			if m == nil {
				return usageErr(errors.New("replay writes a member project's records: run it in a project with a coordination: block, without --nats-url (" + apexv1.CodeCoordMembership + ")"))
			}
			id, err := natsconn.DecodeIdentity(m.Creds)
			if err != nil {
				return err
			}
			p, err := f.project()
			if err != nil {
				return err
			}
			recs, err := rebuild.Collect(p, id.SubjectToken, m.Project)
			if err != nil {
				return err
			}
			js, err := jetstream.New(nc)
			if err != nil {
				return err
			}
			if err := rebuild.Replay(ctx, js, recs); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "replayed %d event(s), %d run manifest(s), %d attempt record(s) of %s (checkout %s)\n",
				len(recs.Events), len(recs.Runs), len(recs.Attempts), m.Project, recs.Checkout)
			return nil
		},
	}
	f.add(cmd)
	return cmd
}
