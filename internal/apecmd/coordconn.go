package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/nats-io/nats.go"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/member"
	"github.com/exoport/apex_process_ape/internal/natsconn"
)

// coordFlags are the connection flags of the v1.5 coordination verbs
// (ape nats, coordinator, supervisor, health, conductor).
type coordFlags struct {
	cwd, url, creds string
}

func (f *coordFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.cwd, "cwd", "", "the project whose coordination: block and hot/ape-nats.creds to use (default: the current directory)")
	cmd.Flags().StringVar(&f.url, "nats-url", "", "NATS URL, instead of the project's (env "+natsconn.EnvURL+")")
	cmd.Flags().StringVar(&f.creds, "nats-creds", "", "NATS credentials file, instead of the project's hot/ape-nats.creds (env "+natsconn.EnvCreds+")")
}

// connect opens the connection: the flags or their environment when a URL
// is given there, else the project's coordination: block with its
// credential. The membership is nil when the flags decided.
func (f *coordFlags) connect(ctx context.Context, name string) (*nats.Conn, *member.Membership, error) {
	if cfg := natsconn.Resolve(f.url, f.creds); cfg.Enabled() {
		nc, err := natsconn.Connect(ctx, cfg, name)
		return nc, nil, err
	}
	p, err := f.project()
	if err != nil {
		return nil, nil, err
	}
	m, err := member.Load(p)
	if errors.Is(err, member.ErrNotMember) {
		return nil, nil, usageErr(errors.New("no NATS to connect to: give --nats-url (or " + natsconn.EnvURL + "), or run in a project with a coordination: block (" + apexv1.CodeCoordMembership + ")"))
	}
	if err != nil {
		return nil, nil, failErr(fmt.Errorf("%w (%s)", err, apexv1.CodeCoordMembership))
	}
	nc, err := natsconn.Connect(ctx, natsconn.Config{URL: m.URL, CredsFile: m.Creds}, name)
	if err != nil {
		return nil, nil, failErr(fmt.Errorf("NATS unreachable at %s: %w (%s)", m.URL, err, apexv1.CodeCoordNATSUnreachable))
	}
	return nc, &m, nil
}

func (f *coordFlags) project() (*apexv1.Project, error) {
	cwd := f.cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	return apexv1.Open(apexv1.FindRoot(cwd))
}

// defaultCoordID names a coordination process: <kind>-<host>-<pid>.
func defaultCoordID(kind string) string {
	host, _ := os.Hostname()
	return kind + "-" + natsconn.SubjectToken(host) + "-" + strconv.Itoa(os.Getpid())
}

// A member project's events are also published on NATS (v1.5), and a
// project integrating through GitHub opens its PRs with ape's own client
// (stage 1a).
func init() {
	apexv1.EventSink = member.Publish
	apexv1.NewIntegrator = member.Integrator
	apexv1.FenceMilestone = member.FenceMilestone
	apexv1.FenceClose = member.FenceClose
	apexv1.GitVia = member.GitVia
}
