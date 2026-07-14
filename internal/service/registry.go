package service

import (
	"sort"
	"sync"
	"time"
)

// Job states, reported in JobInfo.State.
const (
	StateRunning = "running"
	StateDone    = "done"    // child exited 0
	StateFailed  = "failed"  // child exited non-zero (not operator-stopped)
	StateStopped = "stopped" // operator-requested stop via job.stop
)

// Registry is the daemon's in-memory job table: the authoritative source
// for job.status / job.list and the daemon `status` running count. It holds
// only metadata (no process handles); the daemon owns signalling. Safe for
// concurrent use; the zero value is unusable — build with NewRegistry.
type Registry struct {
	mu   sync.Mutex
	jobs map[string]*jobEntry
}

type jobEntry struct {
	info          JobInfo
	stopRequested bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{jobs: make(map[string]*jobEntry)}
}

// Add records a newly accepted, running job. info.State is forced to
// running so callers can't add a job in a terminal state by mistake, and
// LastEventAt is seeded to StartedAt (a just-accepted job's last lifecycle
// event is its acceptance).
func (r *Registry) Add(info JobInfo) {
	info.State = StateRunning
	info.LastEventAt = info.StartedAt
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[info.JobID] = &jobEntry{info: info}
}

// SetProcess records the child's pid and log path once the process has
// started. It never changes State, so it is safe even if the child has
// already exited and been finalized (the pid/log are informational).
func (r *Registry) SetProcess(id string, pid int, logPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.jobs[id]; ok {
		e.info.PID = pid
		e.info.LogPath = logPath
	}
}

// Get returns a copy of the job's info and whether it exists.
func (r *Registry) Get(id string) (JobInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.jobs[id]
	if !ok {
		return JobInfo{}, false
	}
	return e.info, true
}

// List returns a copy of every job, sorted by job id (chronological, since
// ids embed a timestamp prefix).
func (r *Registry) List() []JobInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]JobInfo, 0, len(r.jobs))
	for _, e := range r.jobs {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JobID < out[j].JobID })
	return out
}

// RunningCount reports how many jobs are still running.
func (r *Registry) RunningCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.jobs {
		if e.info.State == StateRunning {
			n++
		}
	}
	return n
}

// RequestStop marks a running job for operator stop and returns its pid so
// the daemon can signal the process group. ok is false when the job is
// unknown or already terminal (nothing to stop).
func (r *Registry) RequestStop(id string) (pid int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, exists := r.jobs[id]
	if !exists || e.info.State != StateRunning {
		return 0, false
	}
	e.stopRequested = true
	return e.info.PID, true
}

// Finish records a job's terminal state from its child exit code, stamping
// at as the time of the job-end lifecycle event. State and LastEventAt are
// set under the same lock, so a job.status caller can never observe a
// terminal state paired with a stale (acceptance-time) last_event_at. A job
// marked via RequestStop becomes "stopped"; otherwise exit 0 → "done" and
// any non-zero → "failed". No-op for an unknown id.
func (r *Registry) Finish(id string, exitCode int, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.jobs[id]
	if !ok {
		return
	}
	code := exitCode
	e.info.ExitCode = &code
	e.info.LastEventAt = at
	switch {
	case e.stopRequested:
		e.info.State = StateStopped
	case exitCode == 0:
		e.info.State = StateDone
	default:
		e.info.State = StateFailed
	}
}
