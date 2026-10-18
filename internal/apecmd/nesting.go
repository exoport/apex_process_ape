package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/proctree"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessions"
	"gopkg.in/yaml.v3"
)

// The nesting rule (D61, D62; apexv1/nesting.go has the policy).
//
// Every model session ape launches — `ape task`, `ape prompt`, `ape chat`,
// `ape change review --run`, and the `ape service` task/prompt jobs, which
// run those commands — carries APE_SESSION=<kind>/<run-id> and its depth
// (repl.EnvDepth, with the parent's run and pid at depth 2). Claude hands
// its environment to every tool subprocess, so a skill that shells out to
// one of those commands reaches here with the marker set, and the command
// asks whether the session it runs in may start one:
//
//   - depth 0 (no ape session around it) starts anything;
//   - depth 1 starts a child only when it is ape's own apex-orchestrator
//     run, only an allow-listed skill, and one child at a time;
//   - depth 2 starts nothing.
//
// Nesting was once open, then closed outright (an outer run's Stop hook,
// idle anchor and cost record wrapped work they could not see). D61 runs
// every skill of the v1 loop as its own session, and D62 allows exactly the
// one level that needs: the orchestrator's children, each detached with its
// own manifest, recording its parent run, and dying with its parent.
//
// What the session is comes from what ape recorded, never from the model:
// the environment marker AND the process ancestry. A model can rewrite its
// tool's environment (`env -u APE_SESSION ape task …`); it cannot rewrite
// the parents of the shell that tool runs in. Every session ape launches is
// a row in the live-session registry (internal/sessions) naming the ape
// process that runs it and the claude it spawned; a command whose
// ancestors include either is inside that session, and its depth is the
// larger of the two answers. Matching is by pid, never by process name: an
// eval's bash stand-in named `claude` gets the same answer a real session
// does.
//
// An `ape service` job (D83) is decided by the daemon from the caller's
// stamp, which `ape service submit` takes from currentSession here; a
// depth-1 orchestrator's job runs under the orchestrator's marker, so its
// `ape task` comes through gateSession as that orchestrator's child
// (service_submit.go).
//
// The residuals, documented rather than chased: a model that scrubs the
// environment AND double-forks (setsid, nohup into a daemon) leaves no
// ancestor ape knows. So does Windows when an ancestor has exited and its
// pid been reused. And a raw NATS publish to a service daemon can claim
// depth 0 in its stamp: the NATS credentials and subject permissions remain
// that boundary. Each needs a deliberate escape, not a slip.
//
// Deterministic commands (status, change new|check|start|verify|archive,
// milestone gate|close, next add, …) never call this. There is no escape
// hatch: a variable that disables the rule is a variable a session can set.

// parentOf reads a process's parent; a test seam (an injected process
// table).
var parentOf proctree.ParentFunc = proctree.Parent

// sessionAt is the ape session a command runs in.
type sessionAt struct {
	apexv1.SessionAt
	// RunnerPID is the ape process running the session: what its child
	// records as APE_PARENT_PID and dies with. 0 when unknown.
	RunnerPID int
	// Source says how it was found: "ancestry", "env", or "" at depth 0.
	Source string
}

// currentSession finds the ape session this process runs in. projectRoot
// (may be "") lets a session known only by its marker be completed from
// its run manifest when the registry no longer has it.
func currentSession(projectRoot string) sessionAt {
	env := sessionFromEnv()
	rows, _ := sessions.Alive(sessions.Path())
	if row := ancestorSession(rows); row != nil {
		at := sessionAt{
			Depth: rowDepth(*row), Run: row.RunID, Skill: row.Skill, Kind: row.Command,
			RunnerPID: row.PID, Source: "ancestry",
		}
		at.Depth = max(at.Depth, env.Depth)
		return at
	}
	if env.Depth == 0 {
		return sessionAt{}
	}
	at := sessionAt{SessionAt: env, Source: "env"}
	for i := range rows {
		if row := &rows[i]; row.RunID != "" && row.RunID == env.Run {
			at.Skill, at.RunnerPID = row.Skill, row.PID
			at.Depth = max(at.Depth, rowDepth(*row))
			return at
		}
	}
	if projectRoot != "" && (env.Kind == "task" || env.Kind == "script") {
		at.Skill = runningTaskSkill(projectRoot, env.Run)
	}
	return at
}

// sessionFromEnv reads the marker ape set on the session: APE_SESSION, and
// APE_DEPTH (1 when an older ape set only the first).
func sessionFromEnv() apexv1.SessionAt {
	marker := os.Getenv(repl.EnvApeSession)
	depth, err := strconv.Atoi(os.Getenv(repl.EnvDepth))
	if err != nil || depth < 0 {
		depth = 0
	}
	if marker == "" && depth == 0 {
		return apexv1.SessionAt{}
	}
	if depth == 0 {
		depth = 1
	}
	kind, run, _ := strings.Cut(marker, "/")
	return apexv1.SessionAt{Depth: depth, Kind: kind, Run: run}
}

// ancestorSession is the registry row of the nearest ancestor that is an
// ape-launched session: its claude, or the ape process running it.
func ancestorSession(rows []sessions.Session) *sessions.Session {
	if len(rows) == 0 {
		return nil
	}
	for _, pid := range proctree.Ancestors(os.Getpid(), parentOf) {
		for i := range rows {
			if rows[i].RunID == "" {
				continue // a web-mode row: no model session ape tracks
			}
			if rows[i].PID == pid || (rows[i].ClaudePID != 0 && rows[i].ClaudePID == pid) {
				return &rows[i]
			}
		}
	}
	return nil
}

func rowDepth(r sessions.Session) int {
	if r.Depth <= 0 {
		return 1
	}
	return r.Depth
}

// runningTaskSkill is the skill of a task run still marked running under
// projectRoot: ape's own record of a session whose registry row is gone.
func runningTaskSkill(projectRoot, run string) string {
	if run == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(runlog.TasksRoot(projectRoot), "*", run, "manifest.yaml"))
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var mf struct {
			Status string `yaml:"status"`
		}
		if yaml.Unmarshal(data, &mf) == nil && mf.Status == "running" {
			return filepath.Base(filepath.Dir(filepath.Dir(m)))
		}
	}
	return ""
}

// sessionLaunch is what a command may start, decided before it starts it:
// the new session's depth and parent, and the locks it holds.
type sessionLaunch struct {
	depth     int
	parentRun string
	parentPID int
	// skill is the skill the session runs ("" for a prompt or chat).
	skill string
	// token identifies the holder of locks: the detached run's id, or
	// pid-<pid>.
	token string
	locks []string
}

// env is the session's lineage (repl.LineageEnv), plus an empty
// APE_DETACHED_ID: a session never passes on the id of the detached run
// that hosts it, which a lock would otherwise read as its own.
func (l *sessionLaunch) env() []string {
	if l == nil {
		return nil
	}
	return append(repl.LineageEnv(l.depth, l.parentRun, l.parentPID), envDetachedID+"=")
}

// release drops the locks this process holds.
func (l *sessionLaunch) release() {
	if l == nil {
		return
	}
	for _, path := range l.locks {
		releaseSessionLock(path, l.token)
	}
}

// gateSession applies the nesting rule for verb (e.g. "ape task") about to
// start a session running skill ("" for a prompt or chat) in projectRoot.
// The error is an apexv1.UsageError (session.depth, session.not-allowed).
func gateSession(verb, skill, projectRoot string) (*sessionLaunch, error) {
	at := currentSession(projectRoot)
	var allow []string
	if at.Orchestrator() {
		allow = childAllowList(projectRoot)
	}
	depth, err := apexv1.SessionStart(at.SessionAt, verb, skill, allow)
	if err != nil {
		return nil, err
	}
	l := &sessionLaunch{depth: depth, skill: skill}
	if depth > 1 {
		l.parentRun, l.parentPID = at.Run, at.RunnerPID
	}
	return l, nil
}

// childAllowList is the orchestrator's allow-list for projectRoot (the core
// skills when the project's config cannot be read).
func childAllowList(projectRoot string) []string {
	p, err := apexv1.Open(projectRoot)
	if err != nil {
		p = &apexv1.Project{Root: projectRoot}
	}
	cfg, _ := apexv1.LoadConfig(projectRoot)
	return p.ChildSkillAllowList(cfg)
}

// refuseInSession is `ape script`'s rule (RC3-PLAN Q6): a script starts model
// sessions, so it is a depth-0 conductor and refuses inside any ape session.
func refuseInSession(verb string) error {
	at := currentSession("")
	if at.Depth == 0 {
		return nil
	}
	return &apexv1.UsageError{Code: apexv1.CodeSessionDepth, Action: apexv1.ActionStop, Msg: fmt.Sprintf(
		"%s refused: this runs inside an ape session (%s/%s, depth %d), and a script is a conductor: it runs from a terminal, outside every ape session",
		verb, at.Kind, at.Run, at.Depth)}
}

// sessionRefusal renders a nesting refusal for the session-starting
// commands, which are not v1 commands and print no --json diagnostic: the
// message, then its code and action, with the code's exit (2, or 3 for a
// lock another run holds).
func sessionRefusal(err error) error {
	var code, action string
	exit := ExitUsage
	var ue *apexv1.UsageError
	var ce *apexv1.ConflictError
	switch {
	case errors.As(err, &ue):
		code, action = ue.Code, ue.Action
	case errors.As(err, &ce):
		code, action, exit = ce.Code, ce.Action, exitV1Conflict
	default:
		return usageErr(err)
	}
	return usageErrExit(exit, fmt.Errorf("%w (code %s, action %s)", err, code, action))
}

// acquireSessionLocks takes the locks the launch needs in projectRoot: the
// project's one apex-orchestrator run, and the parent orchestrator run's one
// child. token names the holder (a detached run's id, else pid-<pid>); a
// lock the same token already holds is taken over. On error nothing is
// held.
func (l *sessionLaunch) acquire(projectRoot, token string) error {
	l.token = token
	root := runlog.SessionLocksRoot(projectRoot)
	rec := sessionLock{PID: os.Getpid(), Token: token, Skill: l.skill, ParentRun: l.parentRun, StartedAt: time.Now().UTC()}
	if l.skill == apexv1.OrchestratorSkill {
		path := filepath.Join(root, "orchestrator.json")
		if err := acquireSessionLock(path, rec, false); err != nil {
			return err
		}
		l.locks = append(l.locks, path)
	}
	if l.depth > 1 {
		path := filepath.Join(root, "child-"+safeLockName(l.parentRun)+".json")
		if err := acquireSessionLock(path, rec, true); err != nil {
			l.release()
			l.locks = nil
			return err
		}
		l.locks = append(l.locks, path)
	}
	return nil
}

// lockToken is the holder token for this process: the detached run it is
// (the supervisor sets APE_DETACHED_ID), else its pid.
func lockToken() string {
	if id := os.Getenv(envDetachedID); id != "" {
		return id
	}
	return "pid-" + strconv.Itoa(os.Getpid())
}

func safeLockName(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == os.PathSeparator {
			return '_'
		}
		return r
	}, s)
}

// sessionLock is one lock file under runlog.SessionLocksRoot.
//
//nolint:tagliatelle // snake_case, like every other file ape writes for tools to read
type sessionLock struct {
	PID       int       `json:"pid"`
	Token     string    `json:"token"`
	Skill     string    `json:"skill,omitempty"`
	ParentRun string    `json:"parent_run,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

func (s sessionLock) describe() string {
	d := s.Skill
	if strings.HasPrefix(s.Token, "pid-") {
		d += ", pid " + strconv.Itoa(s.PID)
	} else {
		d += ", detached run " + s.Token
	}
	return strings.TrimPrefix(d, ", ") + ", since " + s.StartedAt.Format(time.RFC3339)
}

func readSessionLock(path string) (sessionLock, error) {
	var s sessionLock
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

// acquireSessionLock creates path for rec, exclusively: written to a
// temporary file and hard-linked into place, so a reader never sees it half
// written. A lock held by rec's own token is taken over (rec's pid recorded);
// one whose holder is dead is cleared; one a live holder has is refused
// (apexv1.SessionBusy).
func acquireSessionLock(path string, rec sessionLock, child bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	for range 5 {
		tmp := path + "." + rec.Token + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil { //nolint:gosec // a session lock, the mode of the run locks beside it (atomicfile.Mode)
			return err
		}
		err := os.Link(tmp, path)
		_ = os.Remove(tmp)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		cur, rerr := readSessionLock(path)
		switch {
		case rerr != nil:
			// Unreadable: a lock is never half written, so it is damaged —
			// clear it.
			_ = os.Remove(path)
		case cur.Token == rec.Token:
			return atomicfile.Write(path, data)
		case !proctree.Alive(cur.PID):
			_ = os.Remove(path)
		default:
			return apexv1.SessionBusy(child, cur.describe())
		}
	}
	return fmt.Errorf("could not take the session lock %s", path)
}

// retargetSessionLocks records pid as the holder of the locks token holds:
// the --detach process hands them to the supervisor it started, which lives
// as long as the run.
func retargetSessionLocks(paths []string, token string, pid int) {
	for _, path := range paths {
		cur, err := readSessionLock(path)
		if err != nil || cur.Token != token {
			continue
		}
		cur.PID = pid
		if data, err := json.MarshalIndent(cur, "", "  "); err == nil {
			_ = atomicfile.Write(path, append(data, '\n'))
		}
	}
}

// releaseSessionLock removes path when token holds it.
func releaseSessionLock(path, token string) {
	if cur, err := readSessionLock(path); err == nil && cur.Token == token {
		_ = os.Remove(path)
	}
}

// liveSession is a launched session's row in the live-session registry:
// registered once the run has an id, its claude pid recorded at the spawn,
// and removed when the session ends. Best-effort throughout: a registry
// that cannot be written costs the ancestry half of the nesting rule, never
// the run.
type liveSession struct {
	pid int
	run string
}

// registerSession adds the row for a session this process runs.
func registerSession(kind, projectRoot, runID string, l *sessionLaunch) *liveSession {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		abs = projectRoot
	}
	row := sessions.Session{PID: os.Getpid(), CWD: abs, Command: kind, StartedAt: time.Now().UTC(), RunID: runID, Depth: 1}
	if l != nil {
		row.Skill, row.Depth, row.ParentRun = l.skill, l.depth, l.parentRun
	}
	if sessions.Register(sessions.Path(), row) != nil {
		return nil
	}
	return &liveSession{pid: row.PID, run: runID}
}

// spawned records the claude pid of the session.
func (s *liveSession) spawned(claudePID int) {
	if s == nil || claudePID <= 0 {
		return
	}
	_ = sessions.UpdateRun(sessions.Path(), s.pid, s.run, func(row *sessions.Session) { row.ClaudePID = claudePID })
}

// end removes the row.
func (s *liveSession) end() {
	if s == nil {
		return
	}
	_ = sessions.DeregisterRun(sessions.Path(), s.pid, s.run)
}

// errParentGone is the cancellation cause of a session whose parent
// orchestrator run ended first.
var errParentGone = errors.New("the orchestrator run that started this session ended")

// watchParent returns ctx cancelled with errParentGone once the process
// parentPID is gone (checked every parentPollInterval); parentPID 0 watches
// nothing.
func watchParent(ctx context.Context, parentPID int) (watched context.Context, stop func()) {
	if parentPID <= 0 {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		tick := time.NewTicker(parentPollInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !proctree.Alive(parentPID) {
					cancel(errParentGone)
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(nil) }
}

// recordLineage writes what the run's manifest cannot know at its start:
// termination parent_gone for a session stopped because its parent ended,
// and, for an orchestrator run, the child runs it started with their cost
// (children, tree_cost_usd — totals stay its own). tree_cost_usd is written
// for every orchestrator run, $0 and childless included, so its absence
// only ever means "not computed". Best-effort, like recordLaunch.
func recordLineage(ctx context.Context, projectRoot, skill, manifestDir string, l *sessionLaunch) {
	gone := errors.Is(context.Cause(ctx), errParentGone)
	if !gone && skill != apexv1.OrchestratorSkill {
		return
	}
	patchLatestManifest(projectRoot, skill, manifestDir, func(m *pipeline.Manifest) {
		if gone {
			m.Termination = &pipeline.TerminationRecord{Kind: pipeline.TerminationParentGone, Message: fmt.Sprintf(
				"the orchestrator run %s that started this session ended (its ape process, pid %d, is gone), and ape stopped the session", l.parentRun, l.parentPID)}
		}
		if skill != apexv1.OrchestratorSkill {
			return
		}
		children, childCost := cost.ChildRuns(projectRoot, m.RunID)
		m.Children = nil
		for _, c := range children {
			m.Children = append(m.Children, pipeline.ChildRunRecord{Run: c.Run, Skill: c.Skill, CostUSD: c.CostUSD, Status: c.Status})
		}
		tree := m.Totals.CostUSD + childCost
		m.TreeCostUSD = &tree
	})
}

func (l *sessionLaunch) depthOrZero() int {
	if l == nil {
		return 0
	}
	return l.depth
}

func (l *sessionLaunch) parentRunOrEmpty() string {
	if l == nil {
		return ""
	}
	return l.parentRun
}
