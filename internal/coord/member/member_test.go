package member

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/natstest"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

func project(t *testing.T, config string, creds bool) *apexv1.Project {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "state"), 0o755))
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), []byte(config), 0o644))
	if creds {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), []byte("x"), 0o600))
	}
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	return p
}

const block = "project_name: Notes Svc\ncoordination:\n  ensemble: Shop\n  home: git@example.invalid:shop/home.git\n  url: nats://127.0.0.1:4222\n"

func TestLoad(t *testing.T) {
	m, err := Load(project(t, block, true))
	require.NoError(t, err)
	require.Equal(t, "shop", m.Ensemble)
	require.Equal(t, "notes-svc", m.Project)
	require.Equal(t, "nats://127.0.0.1:4222", m.URL)
	require.True(t, strings.HasSuffix(m.Creds, filepath.FromSlash("hot/ape-nats.creds")))

	_, err = Load(project(t, "project_name: x\n", true))
	require.ErrorIs(t, err, ErrNotMember)
	_, err = Load(project(t, "coordination:\n  ensemble: shop\n", true))
	require.ErrorIs(t, err, ErrIncomplete)
	require.ErrorContains(t, err, "home, url")
	_, err = Load(project(t, block, false))
	require.ErrorIs(t, err, ErrNoCreds)
	// R8: an ensemble named as the project is refused, slug for slug.
	_, err = Load(project(t, strings.Replace(block, "ensemble: Shop", "ensemble: notes.svc", 1), true))
	require.ErrorIs(t, err, ErrNameClash)
}

func TestCheckEnsemble(t *testing.T) {
	require.NoError(t, CheckEnsemble("shop", []string{"notes", "cart"}))
	require.ErrorIs(t, CheckEnsemble("Cart", []string{"notes", "cart"}), ErrNameClash)
}

// §4.5 / OQ5: a start needs NATS; the outage is recorded from its first
// failure and cleared by the next connect; the status line says it.
func TestRequireAndStatusLine(t *testing.T) {
	p := project(t, block, true)
	m, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "coordination: ensemble shop (home git@example.invalid:shop/home.git)", StatusLine(&m, ReadLink(p.Root)))
	require.Empty(t, StatusLine(nil, Link{}), "a project in no ensemble has no line")

	t0 := time.Date(2026, 10, 31, 14, 2, 0, 0, time.Local) //nolint:gosmopolitan // the line shows local time
	down := func() error { return errors.New("nats: no servers available for connection") }
	err = Require(p.Root, "change start", down, t0)
	var fe *apexv1.FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, apexv1.CodeCoordNATSUnreachable, fe.Code)
	require.ErrorContains(t, err, "since 14:02, so change start waits")
	require.Error(t, Require(p.Root, "milestone claim", down, t0.Add(time.Hour)))
	require.Equal(t, t0.UTC(), ReadLink(p.Root).DownSince, "the outage began at the first failure")
	require.Equal(t, "coordination: paused — NATS unreachable since 14:02 (in-flight work continues; new claims and merges wait)",
		StatusLine(&m, ReadLink(p.Root)))
	require.FileExists(t, runlog.CoordLinkPath(p.Root))

	require.NoError(t, Require(p.Root, "change start", func() error { return nil }, t0.Add(2*time.Hour)))
	require.True(t, ReadLink(p.Root).DownSince.IsZero())
	require.NoFileExists(t, runlog.CoordLinkPath(p.Root))
}

// OQ5 end to end on a server that checks credentials: a project in no
// ensemble passes untouched; a member connects with hot/ape-nats.creds;
// the same member with the server gone is refused and the outage noted;
// a broken block is the person's (coord.membership).
func TestGate(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	require.NoError(t, Gate(ctx, project(t, "project_name: x\n", false), "change start", now))

	url, account := natstest.StartOperator(t)
	creds, _ := natstest.MintCreds(t, account, "notes", nil, nil)
	p := project(t, strings.Replace(block, "nats://127.0.0.1:4222", url, 1), false)
	data, err := os.ReadFile(creds)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(p.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "ape-nats.creds"), data, 0o600))
	require.NoError(t, Gate(ctx, p, "change start", now))
	require.True(t, ReadLink(p.Root).DownSince.IsZero())

	// The control: a wrong credential is refused by the server, so the
	// pass above was the credential's.
	other, _ := natstest.MintCreds(t, natstest.NewAccountKey(t), "stranger", nil, nil)
	bad, err := os.ReadFile(other)
	require.NoError(t, err)
	q := project(t, strings.Replace(block, "nats://127.0.0.1:4222", url, 1), false)
	require.NoError(t, os.MkdirAll(filepath.Join(q.Root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(q.Root, "hot", "ape-nats.creds"), bad, 0o600))
	var fe *apexv1.FailError
	require.ErrorAs(t, Gate(ctx, q, "change start", now), &fe)
	require.Equal(t, apexv1.CodeCoordNATSUnreachable, fe.Code)

	down := project(t, strings.Replace(block, "nats://127.0.0.1:4222", "nats://127.0.0.1:1", 1), true) // nothing listens there
	require.ErrorAs(t, Gate(ctx, down, "change archive", now), &fe)
	require.Equal(t, apexv1.CodeCoordNATSUnreachable, fe.Code)
	require.False(t, ReadLink(down.Root).DownSince.IsZero())

	require.ErrorAs(t, Gate(ctx, project(t, block, false), "change start", now), &fe)
	require.Equal(t, apexv1.CodeCoordMembership, fe.Code)
}
