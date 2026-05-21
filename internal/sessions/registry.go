// Package sessions maintains ~/.ape/registry.json — the cross-project
// list of live `ape chat` / `ape pipeline` (web mode) invocations so
// users can list, prune, and reopen them. PLAN-5 / C5.
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
type Session struct {
	PID       int       `json:"pid"`
	CWD       string    `json:"cwd"`
	Command   string    `json:"command"`
	Port      int       `json:"port"`
	URL       string    `json:"url"`
	StartedAt time.Time `json:"started_at"`
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
