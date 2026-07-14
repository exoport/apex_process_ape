package apescript

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/pipeline"
)

// ErrNoRuntime is returned by the orchestration/plumbing functions when they
// are called outside a live `ape script` invocation (i.e. no environment has
// been installed via Activate). Pure helpers that need no host wiring —
// ReadManifest, ScanTranscript, Skills — work regardless.
var ErrNoRuntime = errors.New("apescript: no active ape script runtime (call only from within `ape script`)")

// Config is the per-invocation wiring the `ape script` command installs
// before evaluating a script. It is the host seam, not a script-facing API:
// scripts must not construct or pass it. Every func field may be nil, in
// which case the corresponding orchestration/plumbing function returns
// ErrNoRuntime (or, for Publish/PutBlob, a "not configured" error).
type Config struct {
	// ProjectRoot is the script's default project root.
	ProjectRoot string
	// Args are the tokens after `--` on the command line.
	Args []string
	// Quiet suppresses Log output.
	Quiet bool
	// Sandbox reports whether the interpreter is running restricted.
	Sandbox bool
	// RunID is the script invocation's own run id (the events <id> segment).
	RunID string
	// LogWriter receives Log output (defaults to os.Stderr when nil).
	LogWriter io.Writer

	// RunPipeline / RunTask / RunPrompt are the PTY-backed runner hooks.
	RunPipeline func(context.Context, PipelineOpts) (RunResult, error)
	RunTask     func(context.Context, TaskOpts) (RunResult, error)
	RunPrompt   func(context.Context, PromptOpts) (RunResult, error)

	// Publish publishes a script event (identity-stamped subject; the caller
	// chooses only the event token). Nil when NATS is not configured.
	Publish func(event string, v any) error
	// PutBlob uploads a content-addressed blob. Nil when NATS is not configured.
	PutBlob func(context.Context, io.Reader) (Digest, string, error)
}

// env is the installed per-invocation environment. Unexported so it stays out
// of the yaegi symbol surface; the host mutates it only through Activate.
type env struct {
	cfg Config
}

var (
	mu     sync.RWMutex
	active *env
)

// Activate installs cfg as the current script environment and returns a
// restore func that clears it (call it deferred). Wired by internal/apecmd;
// not for use inside scripts.
func Activate(cfg Config) (restore func()) {
	mu.Lock()
	prev := active
	active = &env{cfg: cfg}
	mu.Unlock()
	return func() {
		mu.Lock()
		active = prev
		mu.Unlock()
	}
}

func current() (*env, bool) {
	mu.RLock()
	defer mu.RUnlock()
	return active, active != nil
}

// RunPipeline runs a named pipeline through the same PTY-backed runner the
// `ape pipeline` command uses and returns the run's result derived from its
// manifest.
func RunPipeline(ctx context.Context, o PipelineOpts) (RunResult, error) {
	e, ok := current()
	if !ok || e.cfg.RunPipeline == nil {
		return RunResult{}, ErrNoRuntime
	}
	return e.cfg.RunPipeline(ctx, o)
}

// RunTask runs a single framework skill through the same PTY-backed runner the
// `ape task` command uses (PLAN-11 semantics) and returns the run's result.
func RunTask(ctx context.Context, o TaskOpts) (RunResult, error) {
	e, ok := current()
	if !ok || e.cfg.RunTask == nil {
		return RunResult{}, ErrNoRuntime
	}
	return e.cfg.RunTask(ctx, o)
}

// RunPrompt drives one unattended Claude session from a prompt or handoff
// document through the same path the `ape prompt` command uses (PLAN-12
// semantics) and returns the run's result.
func RunPrompt(ctx context.Context, o PromptOpts) (RunResult, error) {
	e, ok := current()
	if !ok || e.cfg.RunPrompt == nil {
		return RunResult{}, ErrNoRuntime
	}
	return e.cfg.RunPrompt(ctx, o)
}

// ReadManifest loads and parses an ape run manifest.yaml from its containing
// run directory or the manifest path itself.
func ReadManifest(path string) (Manifest, error) {
	// Accept either the run dir or the manifest.yaml path directly.
	runDir := path
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		runDir = dirOf(path)
	}
	m, err := pipeline.LoadManifest(runDir)
	if err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ScanTranscript scans one Claude Code session transcript (a .jsonl file) and
// returns its cost/token totals and per-model breakdown (PLAN-10 shape).
func ScanTranscript(path string) (ScanResult, error) {
	return cost.ScanSession(path)
}

// Skills returns the framework skills resolved for cwd — project-scoped skills
// under <cwd>/.claude/skills first, then the user-scoped ~/.claude/skills that
// are not shadowed by a project skill of the same name. Sorted by name.
func Skills(cwd string) ([]SkillInfo, error) {
	seen := map[string]bool{}
	var out []SkillInfo

	if cwd != "" {
		projDir := framework.ProjectSkillsPath(cwd)
		names, err := framework.ListInstalledSkills(projDir)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			path, scope, found := framework.ResolveSkill(n, cwd)
			if !found {
				continue
			}
			seen[n] = true
			out = append(out, SkillInfo{Name: n, Scope: string(scope), Path: path, Framework: framework.IsFrameworkSkill(n)})
		}
	}

	if userDir := framework.UserSkillsPath(); userDir != "" {
		names, err := framework.ListInstalledSkills(userDir)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			if seen[n] {
				continue // project skill shadows the user one
			}
			path, scope, found := framework.ResolveSkill(n, "")
			if !found {
				continue
			}
			out = append(out, SkillInfo{Name: n, Scope: string(scope), Path: path, Framework: framework.IsFrameworkSkill(n)})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Log writes a structured line to the script's log stream (stderr by default),
// unless the invocation set --quiet. A trailing newline is added when absent.
//
//nolint:goprintffuncname // public v1 API name fixed by PLAN-15 (Log, not Logf)
func Log(format string, args ...any) {
	e, ok := current()
	w := io.Writer(os.Stderr)
	quiet := false
	if ok {
		quiet = e.cfg.Quiet
		if e.cfg.LogWriter != nil {
			w = e.cfg.LogWriter
		}
	}
	if quiet {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if msg == "" || msg[len(msg)-1] != '\n' {
		msg += "\n"
	}
	fmt.Fprint(w, msg)
}

// Args returns the script arguments — everything after `--` on the
// `ape script` command line.
func Args() []string {
	e, ok := current()
	if !ok {
		return nil
	}
	return e.cfg.Args
}

// PublishEvent publishes a script event over the configured NATS connection.
// The subject is always the identity-stamped
//
//	ape.evt.<user>.<project>.script.<run-id>.<event>
//
// The caller chooses only the final <event> token; the identity-stamped prefix
// is fixed so script events stay attributable (PLAN-13/PLAN-17). Returns
// ErrNoRuntime outside a run, or a "not configured" error when NATS is off.
func PublishEvent(event string, v any) error {
	e, ok := current()
	if !ok {
		return ErrNoRuntime
	}
	if e.cfg.Publish == nil {
		return errors.New("apescript: event publishing not configured (pass --nats-url/--nats-creds)")
	}
	return e.cfg.Publish(event, v)
}

// PutBlob uploads r as a content-addressed blob through the configured store
// and returns its digest and locator URI. Returns ErrNoRuntime outside a run,
// or a "not configured" error when NATS is off.
func PutBlob(ctx context.Context, r io.Reader) (digest Digest, uri string, err error) {
	e, ok := current()
	if !ok {
		return Digest{}, "", ErrNoRuntime
	}
	if e.cfg.PutBlob == nil {
		return Digest{}, "", errors.New("apescript: blob store not configured (pass --nats-url/--nats-creds)")
	}
	return e.cfg.PutBlob(ctx, r)
}

// dirOf returns the directory component of a path without pulling filepath
// into the extracted symbol surface for such a trivial need.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
