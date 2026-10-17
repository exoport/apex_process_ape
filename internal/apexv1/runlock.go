package apexv1

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
)

// One run per scope (D58 2.1). A long command — `change start|verify|
// archive` (scope: the change id) or `milestone gate|close` (scope: M<n>)
// — holds _apex/state/runs/<scope>.json while it runs: {id, pid, pgid,
// verb, scope, sha, started, log}, created atomically (a link, which
// fails when the file exists, of a fully written temp file — O_EXCL
// without a moment where a reader finds it empty). A second call of the
// same verb on the scope attaches to the run instead of starting another
// (apecmd prints its result once it ends); a different verb is refused,
// run.busy (exit 3, retry). A lock whose process is gone is stale: it is
// removed and reported as run.interrupted — never an attempt. The run's
// outcome is kept beside it as <scope>.result.json (RunResult) until a
// call has printed it. All of it is ignored (_apex/state/'s whitelist).

// CodeRunBusy: another long command holds the scope's run lock.
const CodeRunBusy = "run.busy"

// RunLock is one long run in flight on a scope.
type RunLock struct {
	ID string `json:"id"`
	// PID is the process running the checks — the detached worker, or the
	// command itself when it runs in-process — and PGID its process group.
	PID     int    `json:"pid"`
	PGID    int    `json:"pgid,omitempty"`
	Verb    string `json:"verb"`
	Scope   string `json:"scope"`
	SHA     string `json:"sha"`
	Started string `json:"started"`
	Log     string `json:"log"`
}

// Alive reports whether the lock's process still runs.
func (l *RunLock) Alive() bool { return processAlive(l.PID) }

// RunsDir is _apex/state/runs/, where run locks, results and logs live.
func (p *Project) RunsDir() string {
	return filepath.Join(p.StateDir(), runlog.V1RunsDirName)
}

// RunLockPath is a scope's run lock.
func (p *Project) RunLockPath(scope string) string {
	return filepath.Join(p.RunsDir(), scope+".json")
}

// RunLogPath is the log a scope's detached run writes.
func (p *Project) RunLogPath(scope string) string {
	return filepath.Join(p.RunsDir(), scope+".log")
}

func (p *Project) runResultPath(scope string) string {
	return filepath.Join(p.RunsDir(), scope+".result.json")
}

// LoadRunLock reads a scope's lock; nil when there is none or it does
// not parse.
func (p *Project) LoadRunLock(scope string) *RunLock {
	data, err := os.ReadFile(p.RunLockPath(scope))
	if err != nil {
		return nil
	}
	var l RunLock
	if json.Unmarshal(data, &l) != nil || l.ID == "" {
		return nil
	}
	return &l
}

// TakeRunLock takes scope's run lock for verb, as this process. When a
// living process holds it, held is that lock (nil error): the caller
// attaches to it or refuses (RunBusy). A stale lock is removed and
// returned as run.interrupted.
func (p *Project) TakeRunLock(ctx context.Context, verb, scope string) (lock, held *RunLock, err error) {
	p.ensureState()
	if err := os.MkdirAll(p.RunsDir(), 0o755); err != nil {
		return nil, nil, err
	}
	l := &RunLock{
		ID:  time.Now().UTC().Format("20060102-150405") + "-" + strconv.Itoa(os.Getpid()) + "-" + randomHex(3),
		PID: os.Getpid(), PGID: ProcessGroup(), Verb: verb, Scope: scope,
		SHA: HeadSHA(ctx, p.Root), Started: Stamp(), Log: p.Rel(p.RunLogPath(scope)),
	}
	for range 3 {
		err := p.linkLock(l)
		if err == nil {
			return l, nil, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, nil, err
		}
		cur := p.LoadRunLock(scope)
		if cur == nil {
			// Removed between the link and the read, or unreadable: a lock
			// only ape writes, whole, cannot be half-written — remove it.
			if _, statErr := os.Stat(p.RunLockPath(scope)); statErr == nil {
				_ = os.Remove(p.RunLockPath(scope))
			}
			continue
		}
		if cur.Alive() {
			return nil, cur, nil
		}
		p.removeLock(cur)
		// A lock left behind by a run that recorded its result ended
		// fine; only a run with no result was interrupted.
		if r := p.LoadRunResult(scope); r != nil && r.ID == cur.ID {
			continue
		}
		return nil, nil, RunGone(cur)
	}
	return nil, nil, &FailError{Code: CodeRunInterrupted, Msg: "could not take the run lock " + p.Rel(p.RunLockPath(scope)) + " — it keeps changing; run the command again"}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// linkLock creates the lock file whole: a temp file linked into place,
// which fails with os.ErrExist when a lock is there.
func (p *Project) linkLock(l *RunLock) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.RunsDir(), ".lock-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), p.RunLockPath(l.Scope))
}

// UpdateRunLock rewrites the lock — the worker recording its pid — when
// the file on disk is still l's run; false when it is not (gone, or
// another run's).
func (p *Project) UpdateRunLock(l *RunLock) bool {
	cur := p.LoadRunLock(l.Scope)
	if cur == nil || cur.ID != l.ID {
		return false
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return false
	}
	return writeFileAtomic(p.RunLockPath(l.Scope), string(data)+"\n") == nil
}

// ReleaseRunLock removes the lock if it is still l's run.
func (p *Project) ReleaseRunLock(l *RunLock) {
	if cur := p.LoadRunLock(l.Scope); cur != nil && cur.ID == l.ID {
		p.removeLock(cur)
	}
}

func (p *Project) removeLock(l *RunLock) { _ = os.Remove(p.RunLockPath(l.Scope)) }

// RemoveStaleRunLock removes l when it is still the scope's lock and its
// process is gone; it returns run.interrupted for it.
func (p *Project) RemoveStaleRunLock(l *RunLock) error {
	if cur := p.LoadRunLock(l.Scope); cur != nil && cur.ID == l.ID && !cur.Alive() {
		p.removeLock(cur)
	}
	return RunGone(l)
}

// RunBusy is run.busy for a call of another verb on l's scope.
func RunBusy(l *RunLock) error {
	return &ConflictError{Code: CodeRunBusy, Action: ActionRetry, Msg: fmt.Sprintf(
		"%s: `ape %s %s` is running on it since %s (pid %d, log %s) — one run per change or milestone; wait for it (`ape %s %s` attaches to it), then run this command again",
		l.Scope, l.Verb, l.Scope, l.Started, l.PID, l.Log, l.Verb, l.Scope)}
}

// RunGone is run.interrupted for a run whose process ended without a
// result.
func RunGone(l *RunLock) error {
	return &FailError{Code: CodeRunInterrupted, Msg: fmt.Sprintf(
		"the run of `ape %s %s` started %s (pid %d) ended without a result — killed, or the machine restarted; its log is %s. Nothing was recorded and no attempt counted — run the same command again",
		l.Verb, l.Scope, l.Started, l.PID, l.Log)}
}

// RunOutput is one rendering of a run's outcome: what the command prints
// on stdout and stderr.
type RunOutput struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// RunResult is a long run's outcome, rendered both ways (--json and not),
// so whichever call prints it prints what the command would have: kept
// until a call has printed it (Reported).
type RunResult struct {
	ID      string `json:"id"`
	Verb    string `json:"verb"`
	Scope   string `json:"scope"`
	SHA     string `json:"sha"`
	Started string `json:"started"`
	Ended   string `json:"ended"`
	// HeadAfter is HEAD when the run ended: an unprinted result is handed
	// to the next call of its verb only while HEAD is still there.
	HeadAfter string `json:"head_after"`
	Exit      int    `json:"exit"`
	// Silent: the outcome is already in the output (a reported error).
	Silent   bool      `json:"silent"`
	Err      string    `json:"error,omitempty"`
	Human    RunOutput `json:"human"`
	JSON     RunOutput `json:"json"`
	Reported bool      `json:"reported"`
}

// SaveRunResult writes a run's result.
func (p *Project) SaveRunResult(r *RunResult) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p.ensureState()
	if err := os.MkdirAll(p.RunsDir(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(p.runResultPath(r.Scope), string(data)+"\n")
}

// LoadRunResult reads a scope's last result; nil when there is none.
func (p *Project) LoadRunResult(scope string) *RunResult {
	data, err := os.ReadFile(p.runResultPath(scope))
	if err != nil {
		return nil
	}
	var r RunResult
	if json.Unmarshal(data, &r) != nil || r.ID == "" {
		return nil
	}
	return &r
}

// AnyRunLive reports whether a long run holds a lock in the project at
// root: work in progress, which `ape task`'s idle backstop counts as
// progress (D58 2.3).
func AnyRunLive(root string) bool {
	p := &Project{Root: root}
	paths, _ := filepath.Glob(filepath.Join(p.RunsDir(), "*.json"))
	for _, path := range paths {
		if strings.HasSuffix(path, ".result.json") {
			continue
		}
		if l := p.LoadRunLock(strings.TrimSuffix(filepath.Base(path), ".json")); l != nil && l.Alive() {
			return true
		}
	}
	return false
}
