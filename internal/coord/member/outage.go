package member

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// Link is the project's recorded NATS link: down since a time, or up
// (zero DownSince).
//
//nolint:tagliatelle // ape's state files are snake_case
type Link struct {
	DownSince time.Time `json:"down_since,omitzero"`
}

// ReadLink reads the recorded link; an absent or unreadable record is up.
func ReadLink(root string) Link {
	var l Link
	data, err := os.ReadFile(runlog.CoordLinkPath(root))
	if err != nil {
		return Link{}
	}
	_ = json.Unmarshal(data, &l)
	return l
}

// Down records the link down at at, keeping an earlier record: the
// outage began at the first failure.
func Down(root string, at time.Time) (Link, error) {
	if l := ReadLink(root); !l.DownSince.IsZero() {
		return l, nil
	}
	l := Link{DownSince: at.UTC()}
	data, err := json.Marshal(l)
	if err != nil {
		return l, err
	}
	path := runlog.CoordLinkPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return l, err
	}
	return l, atomicfile.Write(path, data)
}

// Up clears the record.
func Up(root string) error {
	err := os.Remove(runlog.CoordLinkPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Require runs connect before op (a start or an integration step). A
// failure records the outage and refuses op (coord.nats-unreachable); a
// success clears any record. Work already in flight never calls it
// (§4.5).
func Require(root, op string, connect func() error, now time.Time) error {
	if err := connect(); err != nil {
		l, _ := Down(root, now)
		return &apexv1.FailError{Code: apexv1.CodeCoordNATSUnreachable, Msg: fmt.Sprintf(
			"NATS unreachable since %s, so %s waits (%v) — work in flight continues; run it again once NATS is back",
			clock(l.DownSince), op, err)}
	}
	return Up(root)
}

// StatusLine is `ape status`'s coordination line (§4.5): "" for a project
// in no ensemble.
func StatusLine(m *Membership, l Link) string {
	if m == nil {
		return ""
	}
	if !l.DownSince.IsZero() {
		return "coordination: paused — NATS unreachable since " + clock(l.DownSince) +
			" (in-flight work continues; new claims and merges wait)"
	}
	return "coordination: ensemble " + m.Ensemble + " (home " + m.Home + ")"
}

// clock is a time as the person's wall clock shows it.
func clock(t time.Time) string {
	return t.Local().Format("15:04") //nolint:gosmopolitan // the person reads their own wall clock
}
