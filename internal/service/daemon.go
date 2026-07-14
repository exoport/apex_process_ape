package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// defaultServiceName is the --name default: the <name> subject segment and
// the $SRV discovery name.
const defaultServiceName = "ape"

// now is overridable in tests for deterministic timestamps / job ids.
var now = func() time.Time { return time.Now().UTC() }

// Daemon is the running `ape service` job daemon: it owns the admission
// controller, the job registry, the child spawner, and the NATS connection,
// and answers the micro endpoints. Build with NewDaemon and register onto a
// micro.Service with Register.
type Daemon struct {
	name        string
	projectSlug string
	cfg         *Config
	nc          *nats.Conn
	svc         micro.Service
	adm         *Admissions
	reg         *Registry
	spawner     *Spawner

	// Event identity, precomputed from the daemon credential.
	prefix  string
	userTok string
	userBlk eventing.User

	apeVersion string
	claudeVer  string
	started    time.Time
	draining   atomic.Bool
	jobs       sync.WaitGroup // tracks in-flight child processes (drain)
}

// DaemonConfig carries the pieces NewDaemon needs. Kept separate from the
// command-facing Options (run.go) so the daemon is constructible in tests
// without touching flags or signals.
type DaemonConfig struct {
	Name         string
	Config       *Config
	Conn         *nats.Conn
	Identity     natsconn.Identity
	EventsPrefix string
	ApeVersion   string
	ApeBin       string // "" → os.Executable()
	// NatsURL / NatsCreds are the resolved connection details injected into
	// spawned children (a *nats.Conn does not expose the creds path).
	NatsURL   string
	NatsCreds string
}

// NewDaemon builds a Daemon. It does not touch NATS beyond storing the conn;
// call Register to publish the micro service. ctx bounds the best-effort
// startup probes (the claude version lookup).
func NewDaemon(ctx context.Context, dc DaemonConfig) (*Daemon, error) {
	if dc.Config == nil {
		return nil, errors.New("service: nil config")
	}
	name := strings.TrimSpace(dc.Name)
	if name == "" {
		name = defaultServiceName
	}
	prefix := dc.EventsPrefix
	if prefix == "" {
		prefix = eventing.DefaultPrefix
	}
	sp, err := NewSpawner(dc.ApeBin, dc.NatsURL, dc.NatsCreds, spawnPrefix(prefix))
	if err != nil {
		return nil, err
	}
	userTok := dc.Identity.SubjectToken
	if userTok == "" {
		userTok = "anonymous"
	}
	return &Daemon{
		name:        name,
		projectSlug: eventing.ProjectSlug(dc.Config.ProjectRoot),
		cfg:         dc.Config,
		nc:          dc.Conn,
		adm:         NewAdmissions(),
		reg:         NewRegistry(),
		spawner:     sp,
		prefix:      prefix,
		userTok:     tok(userTok),
		userBlk:     eventing.User{Name: dc.Identity.Name, PublicKey: dc.Identity.Subject},
		apeVersion:  dc.ApeVersion,
		claudeVer:   claudeVersion(ctx),
		started:     now(),
	}, nil
}

// spawnPrefix returns the events prefix to propagate to children — empty
// for the default (children already default to ape.evt), so a child only
// gets an explicit --events-subject-prefix when the daemon overrode it.
func spawnPrefix(prefix string) string {
	if prefix == eventing.DefaultPrefix {
		return ""
	}
	return prefix
}

// tok slugs an arbitrary string into a single NATS subject token, so a
// stray id/name/event value cannot inject extra subject levels or
// wildcards. Empty → "unknown".
func tok(s string) string {
	slug := natsconn.SubjectToken(s)
	if slug == "" {
		return "unknown"
	}
	return slug
}

// Register adds the endpoint group and every endpoint onto svc. The group
// is rooted at ape.svc.<name>.<primary-project-slug>; requests carry their
// own project_root (any allowlisted sibling repo).
func (d *Daemon) Register(svc micro.Service) error {
	d.svc = svc
	grp := svc.AddGroup(fmt.Sprintf("%s.%s.%s", subjectRootSvc, d.name, d.projectSlug))
	endpoints := []struct {
		name, subject string
		h             micro.HandlerFunc
	}{
		{"pipeline-run", "pipeline.run", d.runHandler(KindPipeline)},
		{"task-run", "task.run", d.runHandler(KindTask)},
		{"prompt-run", "prompt.run", d.runHandler(KindPrompt)},
		{"script-run", "script.run", d.runHandler(KindScript)},
		{"job-status", "job.status", d.handleJobStatus},
		{"job-list", "job.list", d.handleJobList},
		{"job-stop", "job.stop", d.handleJobStop},
		{"status", "status", d.handleStatus},
		{"health", "health", d.handleHealth},
	}
	for _, e := range endpoints {
		if err := grp.AddEndpoint(e.name, e.h, micro.WithEndpointSubject(e.subject)); err != nil {
			return fmt.Errorf("service: add endpoint %s: %w", e.subject, err)
		}
	}
	return nil
}

// runHandler dispatches one *.run endpoint for the given kind.
func (d *Daemon) runHandler(kind Kind) micro.HandlerFunc {
	return func(req micro.Request) { d.handleRun(kind, req) }
}

func (d *Daemon) handleRun(kind Kind, req micro.Request) {
	var rr RunRequest
	if err := json.Unmarshal(req.Data(), &rr); err != nil {
		_ = req.Error(CodeValidation, "malformed request JSON: "+err.Error(), nil)
		return
	}
	if strings.TrimSpace(rr.ProjectRoot) == "" {
		_ = req.Error(CodeValidation, "project_root is required", nil)
		return
	}
	// Security boundary first: an unallowlisted root is rejected before the
	// request shape or job kind is even considered.
	if !d.cfg.Allowed(rr.ProjectRoot) {
		_ = req.Error(CodeProjectNotAllowed, fmt.Sprintf("project_root %q is not in this daemon's allowlist", rr.ProjectRoot), nil)
		return
	}
	// Shape + kind availability (catches missing skill/pipeline and the
	// not-shipped prompt.run / script.run kinds → VALIDATION).
	if _, err := BuildArgs(kind, rr); err != nil {
		_ = req.Error(CodeValidation, err.Error(), nil)
		return
	}

	jobID, err := newJobID(now())
	if err != nil {
		_ = req.Error(CodeValidation, "could not mint job id: "+err.Error(), nil)
		return
	}
	projectSlug := eventing.ProjectSlug(rr.ProjectRoot)

	exclusive := !rr.Nonexclusive
	release, err := d.adm.Admit(rr.ExclusivityKey, exclusive)
	if err != nil {
		code := CodeBusyKey
		if errors.Is(err, ErrBusyExclusive) {
			code = CodeBusyExclusive
		}
		d.emitJobEvent(projectSlug, jobID, "job-rejected", map[string]any{
			"kind":            string(kind),
			"reason":          code,
			"exclusivity_key": rr.ExclusivityKey,
		})
		_ = req.Error(code, err.Error(), nil)
		return
	}

	d.reg.Add(JobInfo{
		JobID:          jobID,
		Kind:           kind,
		StartedAt:      now(),
		ExclusivityKey: rr.ExclusivityKey,
		Exclusive:      exclusive,
		SubmittedBy:    rr.SubmittedBy,
	})
	d.emitJobEvent(projectSlug, jobID, "job-accepted", map[string]any{
		"kind":            string(kind),
		"exclusivity_key": rr.ExclusivityKey,
		"exclusive":       exclusive,
		"submitted_by":    rr.SubmittedBy,
	})

	// Track the child for drain. Add before Spawn: Spawn only starts the
	// wait goroutine (which calls onExit → Done) after a successful start,
	// so on a Spawn error we balance with Done ourselves.
	d.jobs.Add(1)
	pid, logPath, err := d.spawner.Spawn(kind, jobID, rr, func(code int) {
		d.onJobExit(projectSlug, jobID, release, code)
	})
	if err != nil {
		d.jobs.Done()
		release()
		d.reg.Finish(jobID, -1)
		d.emitJobEvent(projectSlug, jobID, "job-end", map[string]any{"kind": string(kind), "state": StateFailed, "error": err.Error()})
		_ = req.Error(CodeValidation, "could not start job: "+err.Error(), nil)
		return
	}
	d.reg.SetProcess(jobID, pid, logPath)

	_ = req.RespondJSON(RunReply{V: WireVersion, JobID: jobID, Accepted: true})
}

// onJobExit finalizes a job when its child exits: records the terminal
// state, releases the exclusivity slot, and publishes job-end. Runs exactly
// once per accepted job.
func (d *Daemon) onJobExit(projectSlug, jobID string, release func(), code int) {
	defer d.jobs.Done()
	d.reg.Finish(jobID, code)
	release()
	info, _ := d.reg.Get(jobID)
	d.emitJobEvent(projectSlug, jobID, "job-end", map[string]any{
		"kind":      string(info.Kind),
		"state":     info.State,
		"exit_code": code,
	})
}

func (d *Daemon) handleJobStatus(req micro.Request) {
	var q JobIDRequest
	if err := json.Unmarshal(req.Data(), &q); err != nil {
		_ = req.Error(CodeValidation, "malformed request JSON: "+err.Error(), nil)
		return
	}
	info, ok := d.reg.Get(q.JobID)
	if !ok {
		_ = req.Error(CodeNotFound, fmt.Sprintf("no job %q", q.JobID), nil)
		return
	}
	_ = req.RespondJSON(JobStatusReply{V: WireVersion, JobInfo: info})
}

func (d *Daemon) handleJobList(req micro.Request) {
	_ = req.RespondJSON(JobListReply{V: WireVersion, Jobs: d.reg.List()})
}

func (d *Daemon) handleJobStop(req micro.Request) {
	var q JobIDRequest
	if err := json.Unmarshal(req.Data(), &q); err != nil {
		_ = req.Error(CodeValidation, "malformed request JSON: "+err.Error(), nil)
		return
	}
	if _, ok := d.reg.Get(q.JobID); !ok {
		_ = req.Error(CodeNotFound, fmt.Sprintf("no job %q", q.JobID), nil)
		return
	}
	pid, ok := d.reg.RequestStop(q.JobID)
	if !ok {
		// Job exists but is already terminal: nothing to stop.
		_ = req.RespondJSON(JobStopReply{V: WireVersion, Stopped: false})
		return
	}
	terminateGroup(pid) // SIGTERM the child's whole process group; onExit records "stopped"
	_ = req.RespondJSON(JobStopReply{V: WireVersion, Stopped: true})
}

func (d *Daemon) handleStatus(req micro.Request) {
	_ = req.RespondJSON(StatusReply{
		V:           WireVersion,
		RunningJobs: d.reg.RunningCount(),
		HeldKeys:    d.adm.Snapshot(),
		UptimeSecs:  now().Sub(d.started).Seconds(),
		Versions:    Versions{Ape: d.apeVersion, Claude: d.claudeVer},
		ProjectRoot: d.cfg.ProjectRoot,
		Allowlist:   d.cfg.Allow,
		Name:        d.name,
		Draining:    d.draining.Load(),
	})
}

func (d *Daemon) handleHealth(req micro.Request) {
	checks := map[string]bool{
		"nats":         d.nc != nil && d.nc.IsConnected(),
		"claude_bin":   lookPathOK("claude"),
		"project_root": dirExists(d.cfg.ProjectRoot),
	}
	ok := true
	for _, v := range checks {
		ok = ok && v
	}
	_ = req.RespondJSON(HealthReply{V: WireVersion, OK: ok, Checks: checks})
}

// emitJobEvent publishes a daemon-side job lifecycle event on
// <prefix>.<user>.<project>.svc.<job_id>.<event> (kind "svc", PLAN-14). It
// is fire-and-forget; a nil conn or marshal error is silently dropped (the
// child's own events remain the primary progress stream).
func (d *Daemon) emitJobEvent(projectSlug, jobID, event string, extra map[string]any) {
	if d.nc == nil {
		return
	}
	subject := strings.Join([]string{
		d.prefix, d.userTok, projectSlug, string(eventing.KindSvc), tok(jobID), tok(event),
	}, ".")
	m := map[string]any{
		"v":       eventing.SchemaVersion,
		"ts":      now().Format(time.RFC3339Nano),
		"user":    d.userBlk,
		"project": projectSlug,
		"event":   event,
		"run_id":  jobID,
	}
	maps.Copy(m, extra)
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = d.nc.Publish(subject, data)
}

// StopAccepting drains the micro endpoint subscriptions so no new request is
// delivered; in-flight children keep running. The Flush after Stop forces
// the UNSUB protocol messages to the server (and waits out the PONG) so that
// once this returns the server has removed interest — new requests get
// ErrNoResponders rather than sneaking into a half-torn-down handler.
// Idempotent.
func (d *Daemon) StopAccepting() error {
	d.draining.Store(true)
	if d.svc == nil {
		return nil
	}
	err := d.svc.Stop()
	if d.nc != nil {
		_ = d.nc.Flush()
	}
	return err
}

// WaitJobs blocks until every in-flight child has exited.
func (d *Daemon) WaitJobs() { d.jobs.Wait() }

// KillAll SIGTERMs the process group of every still-running job (drain
// escalation).
func (d *Daemon) KillAll() {
	list := d.reg.List()
	for i := range list {
		if list[i].State == StateRunning && list[i].PID > 0 {
			terminateGroup(list[i].PID)
		}
	}
}

// RunningCount reports how many jobs are still running (test/diagnostic).
func (d *Daemon) RunningCount() int { return d.reg.RunningCount() }

// lookPathOK reports whether bin is on PATH.
func lookPathOK(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// dirExists reports whether p is an existing directory.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// claudeVersion resolves `claude --version` once at startup (best-effort,
// short timeout derived from ctx). Empty when claude is absent or slow —
// health reports the binary's presence separately.
func claudeVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
