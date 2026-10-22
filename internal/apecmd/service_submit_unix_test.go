//go:build linux || darwin

package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/exoport/apex_process_ape/internal/service"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
	"github.com/stretchr/testify/require"
)

// `ape service submit` against a real daemon (eval rc.3 probe N1-service):
// from a depth-2 session the job is refused with session.depth, exit 2, and
// nothing runs; from a terminal it is accepted and the reply printed.
func TestServiceSubmit_StampsTheCallersDepth(t *testing.T) {
	outsideEverySession(t)
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755), "the daemon serves git repositories")
	marker := filepath.Join(t.TempDir(), "spawned")
	bin := filepath.Join(t.TempDir(), "ape")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho ran >> "+marker+"\n"), 0o755))

	url := natstest.RunServer(t)
	conn, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Drain() })
	cfg := &service.Config{ProjectRoot: root, Allow: []string{root}}
	require.NoError(t, cfg.Validate())
	d, err := service.NewDaemon(context.Background(), service.DaemonConfig{Name: "ape", Config: cfg, Conn: conn, ApeBin: bin, ApeVersion: "0.0.0", NatsURL: url, Gate: serviceGate})
	require.NoError(t, err)
	svc, err := micro.AddService(conn, micro.Config{Name: "ape", Version: "0.0.0"})
	require.NoError(t, err)
	require.NoError(t, d.Register(svc))
	t.Cleanup(func() { d.KillAll(); _ = svc.Stop() })
	// Ready the way service.Run is: the server holds the endpoints'
	// interest before submit's connection asks (V11RC1-F2).
	require.NoError(t, conn.Flush())

	submit := func(body string) (string, error) {
		cmd := newServiceCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"submit", "task", body, "--nats-url", url, "--timeout", "3s"})
		err := cmd.Execute()
		return out.String(), err
	}
	body := `{"project_root":"` + root + `","skill":"apex-apply","caller":{"depth":0}}`

	inSession(t, "task/child-run", 2)
	_, err = submit(body)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "session.depth", "a forged depth 0 in the body is replaced by the stamp")
	time.Sleep(200 * time.Millisecond)
	_, statErr := os.Stat(marker)
	require.ErrorIs(t, statErr, os.ErrNotExist, "nothing ran")

	outsideEverySession(t)
	out, err := submit(body)
	require.NoError(t, err)
	var reply service.RunReply
	require.NoError(t, json.Unmarshal([]byte(out), &reply), out)
	require.True(t, reply.Accepted)
	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 5*time.Second, 20*time.Millisecond)

	_, err = submit(`{"skill":"x","bogus":1}`)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, err.Error(), "bogus")
}
