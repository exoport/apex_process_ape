package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/ciwatch"
	"github.com/exoport/apex_process_ape/internal/coord/events"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/ghapp"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// newCICmd is `ape ci` (v1.5 stage 1a, step 10): what a coordinated
// project's merge queue runs in CI.
func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "The merge queue's check and events, run in CI (a project integrating through GitHub)",
		Long: `What a project integrating through GitHub (coordination.github; v1.5 stage 1a, section 5.5)
runs in CI: ` + "`ape ci check`" + ` is the merge queue's required check, ` + "`ape ci event`" + ` tells the
waiting ape a group merged or was ejected, and ` + "`ape ci workflow`" + ` writes the workflow that
runs both.`,
	}
	cmd.AddCommand(newCICheckCmd(), newCIEventCmd(), newCIWorkflowCmd())
	// v1.5 (D876): queue/watch.
	cmd.AddCommand(newCIWatchCmd())
	return cmd
}

func newCICheckCmd() *cobra.Command {
	var f v1Flags
	var base, head string
	cmd := &cobra.Command{
		Use:   "check --base <sha> --head <sha>",
		Short: "Main's merge queue check: run what the merge group carries",
		Long: `Run main's merge queue check on the merge group base..head, with head checked out (full
history; D702: the queue is main's only). ape reads what the group carries from its
commits and runs what each kind needs, the union when the queue batched several pull
requests:

  a close (apex: <M> done, Milestone: <M>; its changes and take-ins ride it)
      the milestone gate with its full run, as ` + "`ape milestone close`" + ` runs it, the gate
      folders the close moved put back first; a person's accepted red passes only on the
      red set accepted (OQ26)
  records alone (apex: <M> proposed | approved | claimed by …)
      the project check

Anything else is red (ci.outside-flow): main takes only ape's close and record pull
requests. A subject proves nothing (D703): every non-merge commit in the group must be on
origin/milestone/<M> of a close the group carries — a branch only ape's App writes — or a
record commit (apex: <M> proposed | approved | re-approved | claimed by …) whose diff
touches only roadmap.md and milestones.json. A plain feature PR into main is refused.

Exit codes: 0 green · 1 red (ci.red), outside the flow (ci.outside-flow), or git failed · 2 usage.`,
		Example: `  ape ci check --base "$BASE_SHA" --head "$HEAD_SHA" --json`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if base == "" || head == "" {
				return usageErr(errors.New("--base and --head name the merge group (merge_group.base_sha, merge_group.head_sha)"))
			}
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			res, err := p.CICheck(cmd.Context(), base, head, apexv1.RunOptions{Timeout: f.timeout, Progress: cmd.ErrOrStderr()})
			if err != nil {
				if res != nil && !f.json {
					printCIRed(cmd.OutOrStdout(), res)
				}
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				fmt.Fprintf(w, "green: %s\n", ciCarries(res))
			})
		},
	}
	addV1Flags(cmd, &f)
	addV1TimeoutFlag(cmd, &f)
	cmd.Flags().StringVar(&base, "base", "", "the merge group's base sha")
	cmd.Flags().StringVar(&head, "head", "", "the merge group's head sha (checked out)")
	return cmd
}

func ciCarries(res *apexv1.CIResult) string {
	var parts []string
	for _, x := range []struct {
		what string
		list []string
	}{{"change", res.Changes}, {"take-in", res.TakeIns}, {"close", res.Closes}, {"record", res.Records}} {
		if len(x.list) > 0 {
			parts = append(parts, x.what+" "+strings.Join(x.list, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

func printCIRed(w io.Writer, res *apexv1.CIResult) {
	for _, r := range res.Red {
		fmt.Fprintln(w, "red: "+r)
	}
}

// ciEvents are the events CI publishes for the waiting ape.
var ciEvents = map[string]bool{"merged": true, "ejected": true, "close-merged": true}

func newCIEventCmd() *cobra.Command {
	var f coordFlags
	var ref, sha, milestone string
	cmd := &cobra.Command{
		Use:   "event <merged|ejected|close-merged> --ref <ref> [--sha <sha>] [--milestone <M>]",
		Short: "Publish a merge queue event for the waiting ape",
		Long: `Publish merged, ejected or close-merged as a project event of the coordinated project
(ape.evt.<user>.<project>.project.<M>.<event>), where every member's waiting ape — a close
waiting on its pull request in main's merge queue — wakes and reads GitHub (OQ10). The
event carries the ref and sha; it decides nothing. Run in CI with the ensemble credential
from a secret (--nats-url, --nats-creds), in the project's checkout for its project name.

Exit codes: 0 published · 1 publish failed · 2 usage, or no NATS.`,
		Example: `  ape ci event merged --ref milestone/M2 --sha "$GITHUB_SHA" --nats-url "$URL" --nats-creds "$CREDS"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !ciEvents[name] {
				return usageErr(fmt.Errorf("event %q: one of merged, ejected, close-merged", name))
			}
			ctx := cmd.Context()
			p, err := f.project()
			if err != nil {
				return usageErr(err)
			}
			cfg, err := apexv1.LoadConfig(p.Root)
			if err != nil {
				return usageErr(err)
			}
			project := cfg.ProjectName
			if project == "" {
				return usageErr(errors.New("_apex/config.yaml has no project_name: the event's subject names the project"))
			}
			nc, _, err := f.connect(ctx, "ape-ci-event")
			if err != nil {
				return err
			}
			defer nc.Close()
			js, err := jetstream.New(nc)
			if err != nil {
				return err
			}
			user := "ci"
			if creds := natsconn.Resolve(f.url, f.creds).CredsFile; creds != "" {
				if id, err := natsconn.DecodeIdentity(creds); err == nil {
					user = id.SubjectToken
				}
			}
			ev := apexv1.CIEvent(name, ref, sha, milestone, time.Now())
			if _, err := events.PublishProject(ctx, js, user, natsconn.SubjectToken(project), "ci-"+strconv.FormatInt(time.Now().UnixNano(), 36), ev); err != nil {
				return failErr(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "published %s for %s (%s)\n", name, project, ev.Reason)
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&ref, "ref", "", "the branch or merge group ref the event is about")
	cmd.Flags().StringVar(&sha, "sha", "", "its sha")
	cmd.Flags().StringVar(&milestone, "milestone", "", "the milestone (default: from a milestone/<M> ref)")
	return cmd
}

func newCIWatchCmd() *cobra.Command {
	var f coordFlags
	var once, dry bool
	var every time.Duration
	cmd := &cobra.Command{
		Use:   "watch [--once] [--dry-run] [--every 10s]",
		Short: "Watch main's merge queue and publish close-merged and ejected (queue/watch, the coordination host)",
		Long: `queue/watch's process (D876), on the coordination host: every --every it reads, over
GitHub's REST API and conditionally (a 304 costs no rate limit), main's tip and the merge
groups' branches (refs/heads/gh-readonly-queue/main/pr-<n>-…), and publishes what the queue's
workflow would have, so a waiting close wakes in seconds rather than at its 1-min poll —
with nothing run on GitHub and no secret on the repository:

  close-merged  main's tip moved (ref main, the new tip's sha)
  ejected       a pull request's merge group left the queue and its sha is not in main
                (GitHub's compare <sha>...main neither identical nor ahead), with the
                group's ref and sha as last seen; a group re-created under a new sha is
                no ejection, and a sha GitHub does not know publishes nothing

Each event is ` + "`ape ci event`" + `'s payload (one builder), under the credential's user, with the
message id ci-watch-<event>-<sha>, so two watchers publish a merge once. The events decide
nothing: the close reads GitHub, and its poll stays the fallback. Stateless: the first look
after a start is a baseline and publishes nothing.

Its GitHub token is the App service's watch token (contents: read, metadata: read), asked
for over NATS as the credential's user, which the service's config lists under the
repository's watchers. Its credential is the watch kind (` + "`ape nats mint user --kind watch`" + `):
the project's events and that one request. Refused unless the project's mode is
queue/watch (ci.watch-mode). Rate limits wait for GitHub's reset; other failures back off
from --every to 5 min; each is one line on stderr.

--once looks once and prints what it read and decided as JSON (exit 0, or 1 when the
look failed); with --dry-run it publishes nothing (a read-only look).

Exit codes: 0 stopped, or --once looked · 1 refused, or the look failed · 2 usage.`,
		Example: "  ape ci watch --nats-creds /var/lib/apex-ghapp/watch.creds\n  ape ci watch --once --dry-run --nats-creds /var/lib/apex-ghapp/watch.creds",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			p, err := f.project()
			if err != nil {
				return usageErr(err)
			}
			project, repo, api, err := p.WatchTarget()
			if fe, ok := errors.AsType[*apexv1.FailError](err); ok {
				return failErr(fmt.Errorf("%w (%s)", err, fe.Code))
			}
			if err != nil {
				return usageErr(err)
			}
			if api == "" {
				api = member.DefaultGitHubAPI
			}
			nc, m, err := f.connect(ctx, "ape-ci-watch")
			if err != nil {
				return err
			}
			defer nc.Close()
			js, err := jetstream.New(nc)
			if err != nil {
				return err
			}
			credsFile := natsconn.Resolve(f.url, f.creds).CredsFile
			if credsFile == "" && m != nil {
				credsFile = m.Creds
			}
			id, err := natsconn.DecodeIdentity(credsFile)
			if err != nil {
				return failErr(err)
			}
			user := id.SubjectToken
			var tok string
			var until time.Time
			token := func(ctx context.Context, refresh bool) (string, error) {
				if !refresh && tok != "" && time.Now().Before(until) {
					return tok, nil
				}
				actx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				rep, err := ghapp.Ask(actx, nc, ghapp.TokenSubject, user, ghapp.Request{Repo: repo, Purpose: ghapp.WatchPurpose})
				if err != nil {
					return "", err
				}
				tok, until = rep.Token, rep.ExpiresAt.Add(-10*time.Minute)
				return tok, nil
			}
			var publish func(context.Context, ciwatch.Published) error
			if !dry {
				publish = func(ctx context.Context, pub ciwatch.Published) error {
					_, err := events.PublishProjectID(ctx, js, user, natsconn.SubjectToken(project), pub.MsgID, pub.Event)
					return err
				}
			}
			w := ciwatch.New(ciwatch.Config{API: api, Repo: repo, Token: token, Publish: publish, Every: every, Log: cmd.ErrOrStderr()})
			if once {
				r, err := w.Look(ctx)
				data, _ := json.MarshalIndent(r, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				if err != nil {
					return failErr(err)
				}
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%s ape ci watch: watching %s's main queue every %s for %s, as %s (dry run: %v)\n", time.Now().UTC().Format(time.RFC3339), repo, every, project, user, dry)
			return w.Run(ctx)
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&once, "once", false, "look once, print what it read and decided as JSON, and exit")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "decide, but publish nothing")
	cmd.Flags().DurationVar(&every, "every", 10*time.Second, "how often to look")
	return cmd
}

func newCIWorkflowCmd() *cobra.Command {
	var f v1Flags
	var write bool
	cmd := &cobra.Command{
		Use:   "workflow [--write]",
		Short: "Print, or write, the merge queue's workflow (.github/workflows/apex-queue.yml)",
		Long: `Print the merge queue's GitHub Actions workflow, or with --write put it at
` + apexv1.QueueWorkflowRel + ` (v1.5 stage 1a, section 5.5). On merge_group it runs
` + "`ape ci check`" + ` (main's queue's required check, "apex-queue-check") and publishes
ejected when it is red; on a push to main it publishes close-merged; on a pull request it
passes at once, since the queue checks the tree that merges. Milestone branches have no
queue: ape lands on them itself (D702). It is queue/runner's (D876): every job runs on
[self-hosted, <runner_label>], the project's own self-hosted runner with ape installed, and
it is refused (ci.workflow-mode) unless the coordination: block says integration: queue,
events: runner and a runner_label of the runner's own — never self-hosted or another of
GitHub's default labels (D871: a bare self-hosted sent the jobs and the credential to
another org runner). The label narrows where the jobs go; only the runner group's
repository access isolates them. It reads two secrets, APE_NATS_URL and APE_NATS_CREDS
(the ensemble credential's text), which a person sets on the repository. ape writes the
file; committing it is the person's, through main's queue.

Exit codes: 0 printed or written · 1 refused (ci.workflow-mode, coord.integration) or the
write failed · 2 usage.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if !write {
				wf, err := p.QueueWorkflow()
				if err != nil {
					return v1Fail(cmd, &f, err, nil)
				}
				_, _ = io.WriteString(cmd.OutOrStdout(), wf)
				return nil
			}
			changed, err := p.WriteQueueWorkflow()
			if err != nil {
				return v1Fail(cmd, &f, err, nil)
			}
			return v1Emit(cmd.OutOrStdout(), &f, map[string]any{"path": apexv1.QueueWorkflowRel, "changed": changed}, func(w io.Writer) {
				if changed {
					fmt.Fprintf(w, "wrote %s — commit it through main's merge queue, and set the APE_NATS_URL and APE_NATS_CREDS secrets\n", apexv1.QueueWorkflowRel)
					return
				}
				fmt.Fprintf(w, "%s is up to date\n", apexv1.QueueWorkflowRel)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&write, "write", false, "write the file instead of printing it")
	return cmd
}
