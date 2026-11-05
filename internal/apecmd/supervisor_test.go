package apecmd

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/coord/supervisor"
	"github.com/exoport/apex_process_ape/internal/natstest"
)

type stubHandle struct{}

func (stubHandle) Stop() {}

// D719 (a): `ape supervisor ensure` is how the 1a run asks for the
// ensemble's coordinator — nothing else sent ape.sup.ensure. With no
// supervisor it exits 1; with one it prints the action, and the
// supervisor started the ensemble's coordinator.
func TestSupervisorEnsure(t *testing.T) {
	url := natstest.RunJetStreamServer(t)
	_, _, err := v1Exec(t, newSupervisorCmd, "ensure", "--nats-url", url, "--ensemble", "shop")
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), "no supervisor answered")

	_, _, err = v1Exec(t, newNatsCmd, "declare", "--nats-url", url, "--replicas", "1", "--new-epoch", "--epoch-file", filepath.Join(t.TempDir(), "sup.epoch"), "--window", "1s")
	require.NoError(t, err)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	var mu sync.Mutex
	var started []string
	var start supervisor.Starter = func(_ context.Context, ens string) (supervisor.Handle, error) {
		mu.Lock()
		started = append(started, ens)
		mu.Unlock()
		return stubHandle{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := supervisor.Run(ctx, nc, supervisor.Config{ID: "sup-t", Start: start, Verify: true}); err != nil {
			t.Logf("supervisor: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })

	require.Eventually(t, func() bool {
		stdout, _, err := v1Exec(t, newSupervisorCmd, "ensure", "--nats-url", url, "--ensemble", "shop")
		return err == nil && stdout == "shop: started\n"
	}, 30*time.Second, 200*time.Millisecond)
	mu.Lock()
	require.Contains(t, started, "shop")
	mu.Unlock()

	// status is read-only: a supervisor leads, but the stub coordinator
	// never answers its ping — exit 1, and nothing more is started.
	mu.Lock()
	n := len(started)
	mu.Unlock()
	stdout, _, err := v1Exec(t, newSupervisorCmd, "status", "--nats-url", url, "--ensemble", "shop")
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, "supervisor: sup-t leads")
	require.Contains(t, stdout, "shop: no coordinator answers")
	mu.Lock()
	require.Len(t, started, n, "status starts nothing")
	mu.Unlock()
}

// The coordinator a supervisor starts watches the member projects'
// milestone leases: each --project goes on its command line.
func TestSupervisor_CoordinatorArgs(t *testing.T) {
	f := coordFlags{cwd: "/srv/atomix", url: "nats://127.0.0.1:14222"}
	require.Equal(t, []string{"coordinator", "--ensemble", "atomix-1a", "--project", "oxid-atomix-engine", "--cwd", "/srv/atomix", "--nats-url", "nats://127.0.0.1:14222"},
		f.coordinatorArgs("atomix-1a", []string{"oxid-atomix-engine"}))
	require.Equal(t, []string{"coordinator", "--ensemble", "atomix-1a", "--assess", "--assess-checkout", "atomix=/srv/c", "--cwd", "/srv/atomix"},
		(&coordFlags{cwd: "/srv/atomix"}).coordinatorArgs("atomix-1a", nil, assessArgs(true, []string{"atomix=/srv/c"}, "")...))
	require.Nil(t, assessArgs(false, []string{"x=y"}, "1M"), "control: no --assess, no assessor flags")

	// V12b: --coordinator-creds gives the child its own credential; the
	// control: without it, the supervisor's own.
	sup := coordFlags{cwd: "/srv/atomix", creds: "/srv/hot/sup.creds"}
	own, same := sup.forCoordinators("/srv/hot/coord.creds"), sup.forCoordinators("")
	require.Equal(t, []string{"coordinator", "--ensemble", "atomix-1a", "--cwd", "/srv/atomix", "--nats-creds", "/srv/hot/coord.creds"},
		own.coordinatorArgs("atomix-1a", nil))
	require.Equal(t, []string{"coordinator", "--ensemble", "atomix-1a", "--cwd", "/srv/atomix", "--nats-creds", "/srv/hot/sup.creds"},
		same.coordinatorArgs("atomix-1a", nil))
}
