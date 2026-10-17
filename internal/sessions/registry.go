// Package sessions maintains ~/.ape/registry.json — the cross-project
// list of live ape sessions: every model session ape launches (`ape task`,
// `ape prompt`, `ape chat`, and the `ape change review --run` and `ape
// script` sessions that run through them), and the web-mode sessions an
// older ape wrote. PLAN-5 / C5.
//
// The rows for model sessions are also how ape tells what session a
// command runs under (the nesting rule, D62): a row names the ape process
// that runs the session and the claude process it spawned, and a command
// whose ancestors include either runs inside that session, whatever its
// environment says.
package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Session is one row in the registry.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type Session struct {
	PID       int       `json:"pid"`
	CWD       string    `json:"cwd"`
	Command   string    `json:"command"`
	Port      int       `json:"port"`
	URL       string    `json:"url"`
	StartedAt time.Time `json:"started_at"`

	// The fields below are a model session's (D62), absent on a web-mode
	// row. PID is then the ape process running the session, and ClaudePID
	// the claude it spawned (0 until the spawn).
	ClaudePID int `json:"claude_pid,omitempty"`
	// RunID is the run's id: the manifest's run id (task), the prompt or
	// chat id.
	RunID string `json:"run_id,omitempty"`
	// Skill is the skill ape typed into the session (task), "" otherwise.
	Skill string `json:"skill,omitempty"`
	// Depth is the session's nesting depth: 1 for one a terminal, a script
	// or the service daemon started, 2 for one an orchestrator started.
	Depth int `json:"depth,omitempty"`
	// ParentRun is the run of the session that started this one (depth 2).
	ParentRun string `json:"parent_run,omitempty"`
}

// registryShape is the on-disk container; we keep the wrapper so a
// future plan can add new top-level fields (`version`, ...) without
// breaking the file format.
type registryShape struct {
	Sessions []Session `json:"sessions"`
}

// DefaultPath returns ~/.ape/registry.json. Falls back to a
// temp-directory path if $HOME is unset (best-effort — the registry
// is non-load-bearing, so a missing home dir should not crash ape).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "ape-registry.json")
	}
	return filepath.Join(home, ".ape", "registry.json")
}

// Path is the registry every reader and writer uses: DefaultPath, replaced
// by tests that must not touch the user's registry.
var Path = DefaultPath

// Register appends s to the registry at path. Acquires an exclusive
// flock for the write so concurrent ape invocations cannot interleave.
// Creates the parent directory if missing.
func Register(path string, s Session) error {
	return withLockedFile(path, func(f *os.File) error {
		reg, err := decode(f)
		if err != nil {
			return err
		}
		reg.Sessions = append(reg.Sessions, s)
		return writeBack(f, reg)
	})
}

// Deregister removes the entry whose PID matches s.PID. No-op if not
// found (best-effort cleanup; we want shutdown to always succeed).
func Deregister(path string, pid int) error {
	return withLockedFile(path, func(f *os.File) error {
		reg, err := decode(f)
		if err != nil {
			return err
		}
		out := reg.Sessions[:0]
		for _, row := range reg.Sessions {
			if row.PID != pid {
				out = append(out, row)
			}
		}
		reg.Sessions = out
		return writeBack(f, reg)
	})
}

// DeregisterRun removes the row of run runID that process pid registered:
// one process (an `ape script`) may run several sessions at once.
func DeregisterRun(path string, pid int, runID string) error {
	return withLockedFile(path, func(f *os.File) error {
		reg, err := decode(f)
		if err != nil {
			return err
		}
		out := reg.Sessions[:0]
		for _, row := range reg.Sessions {
			if row.PID != pid || row.RunID != runID {
				out = append(out, row)
			}
		}
		reg.Sessions = out
		return writeBack(f, reg)
	})
}

// UpdateRun applies change to the row of run runID that process pid
// registered. No-op if not found.
func UpdateRun(path string, pid int, runID string, change func(*Session)) error {
	return withLockedFile(path, func(f *os.File) error {
		reg, err := decode(f)
		if err != nil {
			return err
		}
		for i := range reg.Sessions {
			if reg.Sessions[i].PID == pid && reg.Sessions[i].RunID == runID {
				change(&reg.Sessions[i])
			}
		}
		return writeBack(f, reg)
	})
}

// Alive lists the rows whose PID is running, without taking the write lock
// or rewriting the file: what a reader that must not block on, or modify,
// another process's registry needs.
func Alive(path string) ([]Session, error) {
	rows, err := List(path)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, row := range rows {
		if pidAlive(row.PID) {
			out = append(out, row)
		}
	}
	return out, nil
}

// Prune drops rows whose PID is no longer running. Returns the rows
// that survived. Called by every startup and by `ape sessions prune`.
func Prune(path string) ([]Session, error) {
	var alive []Session
	err := withLockedFile(path, func(f *os.File) error {
		reg, err := decode(f)
		if err != nil {
			return err
		}
		for _, row := range reg.Sessions {
			if pidAlive(row.PID) {
				alive = append(alive, row)
			}
		}
		reg.Sessions = alive
		return writeBack(f, reg)
	})
	return alive, err
}

// List reads the registry without taking the write lock or pruning.
// Useful for `ape sessions` (which prunes first via Prune).
func List(path string) ([]Session, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	reg, err := decode(f)
	if err != nil {
		return nil, err
	}
	return reg.Sessions, nil
}

func decode(f *os.File) (registryShape, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return registryShape{}, err
	}
	var reg registryShape
	bs, err := io.ReadAll(f)
	if err != nil {
		return registryShape{}, err
	}
	if len(bs) == 0 {
		return reg, nil
	}
	if err := json.Unmarshal(bs, &reg); err != nil {
		// Corrupt file → start fresh. Surfacing the error would
		// block legitimate runs; the registry is best-effort.
		return registryShape{}, nil //nolint:nilerr // see comment above
	}
	return reg, nil
}

func writeBack(f *os.File, reg registryShape) error {
	bs, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Write(bs); err != nil {
		return err
	}
	if _, err := f.WriteString("\n"); err != nil {
		return err
	}
	return nil
}

// withLockedFile opens path (creating it if missing) with an exclusive
// flock for the duration of fn. Cross-platform via build-tagged
// lock_{unix,windows}.go helpers.
func withLockedFile(path string, fn func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sessions: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("sessions: open: %w", err)
	}
	defer f.Close()
	if err := lockExclusive(f); err != nil {
		return fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock(f)
	return fn(f)
}
