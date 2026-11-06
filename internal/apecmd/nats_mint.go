package apecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/coord/cred"
	"github.com/exoport/apex_process_ape/internal/coord/member"
)

// newNatsMintCmd is `ape nats mint` (hidden; D813/D814): an enforcing
// cluster's trust root and its credentials, one per kind, each carrying
// exactly its measured cred grant — so no hand-typed nsc lists.
func newNatsMintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "mint",
		Short:  "Make an enforcing cluster's operator and accounts, and mint a credential per kind (stage 2a)",
		Hidden: true,
	}
	cmd.AddCommand(newNatsMintInitCmd(), newNatsMintUserCmd())
	return cmd
}

func newNatsMintInitCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "init --dir <keys>",
		Short: "Create the operator, SYS and APP (JetStream) and the resolver.conf a nats-server includes",
		Long: `Create an operator, the system account SYS and the account APP (JetStream unlimited) in
--dir, their seeds 0600, and resolver.conf (operator, system_account, a MEMORY resolver with
both accounts preloaded) for each nats-server config to include. Refuses a --dir that holds an
operator already. The seeds are the cluster's trust root: keep the directory 0700.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				return usageErr(errors.New("--dir is required"))
			}
			if err := cred.InitKeyDir(dir); err != nil {
				return failErr(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "made %s (include %s in each node's config)\n", dir, cred.ResolverConf)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "the key directory to create")
	return cmd
}

func newNatsMintUserCmd() *cobra.Command {
	var dir, out string
	var s cred.UserSpec
	cmd := &cobra.Command{
		Use:   "user --dir <keys> --kind <kind> --name <name> [--project <p>]… [--ensemble <e>] --out <file.creds>",
		Short: "Mint one credential of a kind, with exactly its grant",
		Long: `Mint one user of APP with exactly its kind's grant (internal/coord/cred):
member (--project once, --ensemble), conductor (--ensemble, --project…), coordinator
(--ensemble), supervisor, ghapp, ci (--project…), or admin (unrestricted: the operator's own,
for ape nats declare and reads; never a checkout's or a service's). --name is the JWT name and
the subject token. A reserved token (coord, sup, vm-*) and a token another minted name has
(minted.json) are refused. Writes --out 0600.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" || out == "" || s.Name == "" || s.Kind == "" {
				return usageErr(errors.New("--dir, --kind, --name and --out are required"))
			}
			data, err := cred.MintUserIn(dir, s)
			if err != nil {
				if strings.Contains(err.Error(), "needs") || strings.Contains(err.Error(), "not one of") {
					return usageErr(err)
				}
				return failErr(err)
			}
			if err := cred.WritePrivate(out, data); err != nil {
				return failErr(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "minted %s %s → %s\n", s.Kind, s.Name, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "the key directory (ape nats mint init)")
	cmd.Flags().StringVar(&s.Kind, "kind", "", "one of "+strings.Join(cred.Kinds, ", "))
	cmd.Flags().StringVar(&s.Name, "name", "", "the credential's name: its JWT name and subject token")
	cmd.Flags().StringArrayVar(&s.Projects, "project", nil, "a project of the kind's scope (repeatable)")
	cmd.Flags().StringVar(&s.Ensemble, "ensemble", "", "the ensemble (member, conductor, coordinator)")
	cmd.Flags().DurationVar(&s.Expires, "expires", 7*24*time.Hour, "how long it lives (0: no expiry)")
	cmd.Flags().StringVar(&out, "out", "", "the .creds file to write (0600)")
	return cmd
}

// newNatsConsumerInfoCmd is `ape nats consumer-info` (hidden, read-only;
// D814): a consumer's configuration and state as JSON, so a watcher can
// see a durable re-created or changed (V12-3's 2a watch) with no nats CLI.
// On a person's credential it reads that person's own conductor durable
// only (V12-3); another's needs the operator's.
func newNatsConsumerInfoCmd() *cobra.Command {
	var f coordFlags
	cmd := &cobra.Command{
		Use:    "consumer-info <stream> <consumer>",
		Short:  "Print a consumer's config and state as JSON (read-only)",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			nc, _, err := f.connect(ctx, "ape-nats-consumer-info")
			if err != nil {
				return err
			}
			defer nc.Close()
			js, err := jetstream.New(nc)
			if err != nil {
				return err
			}
			c, err := js.Consumer(ctx, args[0], args[1])
			if err != nil {
				return failErr(fmt.Errorf("consumer %s on %s: %w", args[1], args[0], err))
			}
			info, err := c.Info(ctx)
			if err != nil {
				return failErr(err)
			}
			data, err := json.MarshalIndent(info, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	f.add(cmd)
	return cmd
}

// newNatsPauseStateCmd is `ape nats pause-state` (hidden, read-only;
// D816+): a milestone's pause state exactly as this checkout's gate reads
// it (member.ReadPauseState: the same PausedWith, counted from the
// credential's user), as JSON, so eval can read row 3a with no gated verb.
func newNatsPauseStateCmd() *cobra.Command {
	var f coordFlags
	cmd := &cobra.Command{
		Use:    "pause-state <M> [--cwd <checkout>]",
		Short:  "Print a milestone's pause state as this checkout's gate reads it (read-only)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := f.project()
			if err != nil {
				return usageErr(err)
			}
			st, err := member.ReadPauseState(cmd.Context(), p, args[0])
			if err != nil {
				return failErr(err)
			}
			data, err := json.MarshalIndent(st, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
	f.add(cmd)
	return cmd
}
