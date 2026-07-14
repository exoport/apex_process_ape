package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrValidation: the request is missing a required field, names a path
// outside the allowlist, uses a disabled feature, or is otherwise
// malformed. The daemon maps it to CodeValidation (the error text carries
// the specific reason).
var ErrValidation = errors.New("service: invalid request")

// scriptStdinArg is the `ape script` positional that switches it to read
// the Go source from stdin (mirrors apecmd.scriptStdinArg).
const scriptStdinArg = "-"

// BuildArgs maps a validated request to the `ape` child-process argv for
// the given kind (the subcommand + flags, without the leading binary). The
// mapping is strict and typed — request fields become discrete argv
// elements, never concatenated into a shell string — so a hostile field
// value cannot inject extra flags or shell metacharacters.
//
// cfg is consulted only by the script builder (allowlist membership of
// script_path plus the allow_script_source / force_script_sandbox gates);
// it may be nil for the other kinds.
func BuildArgs(kind Kind, req RunRequest, cfg *Config) ([]string, error) {
	if strings.TrimSpace(req.ProjectRoot) == "" {
		return nil, fmt.Errorf("%w: project_root is required", ErrValidation)
	}
	switch kind {
	case KindPipeline:
		return buildPipelineArgs(req)
	case KindTask:
		return buildTaskArgs(req)
	case KindPrompt:
		return buildPromptArgs(req)
	case KindScript:
		return buildScriptArgs(req, cfg)
	default:
		return nil, fmt.Errorf("%w: unknown job kind %q", ErrValidation, kind)
	}
}

// buildPromptArgs maps a prompt.run request to `ape prompt` argv: exactly
// one of the positional prompt (req.Prompt) or --handoff (req.Handoff),
// plus optional --agent/--model/--workflow, always headless via --quiet.
func buildPromptArgs(req RunRequest) ([]string, error) {
	hasPrompt := strings.TrimSpace(req.Prompt) != ""
	hasHandoff := strings.TrimSpace(req.Handoff) != ""
	if hasPrompt == hasHandoff {
		return nil, fmt.Errorf("%w: prompt.run requires exactly one of prompt or handoff", ErrValidation)
	}
	var args []string
	if hasPrompt {
		// Positional prompt precedes the flags (ape prompt [text]).
		args = []string{"prompt", req.Prompt, "--quiet", "--cwd", req.ProjectRoot}
	} else {
		args = []string{"prompt", "--handoff", req.Handoff, "--quiet", "--cwd", req.ProjectRoot}
	}
	if req.Agent != "" {
		args = append(args, "--agent", req.Agent)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Workflow {
		args = append(args, "--workflow")
	}
	return args, nil
}

// buildScriptArgs maps a script.run request to `ape script` argv. Exactly
// one of script_path or script_source must be set:
//
//   - script_path must resolve (absolute, or relative to project_root) to an
//     existing file inside an allowlisted root — the D2 filesystem boundary.
//   - script_source is arbitrary code on the daemon host (D5): it is gated by
//     cfg.AllowScriptSource and delivered via `ape script -` stdin (Spawn
//     wires the source onto the child's stdin), never onto the argv.
//
// force_script_sandbox forces PLAN-15's interpreter-level --sandbox onto
// every script job.
func buildScriptArgs(req RunRequest, cfg *Config) ([]string, error) {
	hasPath := strings.TrimSpace(req.ScriptPath) != ""
	hasSource := strings.TrimSpace(req.ScriptSource) != ""
	if hasPath == hasSource {
		return nil, fmt.Errorf("%w: script.run requires exactly one of script_path or script_source", ErrValidation)
	}

	positional := scriptStdinArg
	if hasPath {
		p, err := resolveAllowedScriptPath(req, cfg)
		if err != nil {
			return nil, err
		}
		positional = p
	} else if cfg == nil || !cfg.AllowScriptSource {
		// D5 gate: inline source is disabled unless the operator opts in.
		return nil, fmt.Errorf("%w: script_source is disabled on this daemon (set allow_script_source: true in service.yaml)", ErrValidation)
	}

	args := []string{"script", positional, "--quiet", "--cwd", req.ProjectRoot}
	if cfg != nil && cfg.ForceScriptSandbox {
		args = append(args, "--sandbox")
	}
	if len(req.ScriptArgs) > 0 {
		args = append(args, "--")
		args = append(args, req.ScriptArgs...)
	}
	return args, nil
}

// resolveAllowedScriptPath resolves req.ScriptPath (absolute, or relative to
// project_root), verifies it is an existing file inside an allowlisted root,
// and returns the cleaned path. Any failure is an ErrValidation.
func resolveAllowedScriptPath(req RunRequest, cfg *Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("%w: script.run requires a service config", ErrValidation)
	}
	p := req.ScriptPath
	if !filepath.IsAbs(p) {
		p = filepath.Join(req.ProjectRoot, p)
	}
	p = filepath.Clean(p)
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("%w: script_path %q does not resolve to a file", ErrValidation, req.ScriptPath)
	}
	if !underAnyRoot(cfg.Allow, p) {
		return "", fmt.Errorf("%w: script_path %q resolves outside every allowlisted root", ErrValidation, req.ScriptPath)
	}
	return p, nil
}

// underAnyRoot reports whether p (already cleaned) is one of, or nested
// inside, any of the cleaned allowlist roots.
func underAnyRoot(roots []string, p string) bool {
	for _, root := range roots {
		root = filepath.Clean(root)
		if p == root {
			return true
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

func buildPipelineArgs(req RunRequest) ([]string, error) {
	if strings.TrimSpace(req.Pipeline) == "" {
		return nil, fmt.Errorf("%w: pipeline is required for pipeline.run", ErrValidation)
	}
	// --no-tui --quiet: headless, plain progress; the child's PLAN-13
	// events (not stdout) carry progress to remote consumers.
	args := []string{"pipeline", req.Pipeline, "--no-tui", "--quiet", "--cwd", req.ProjectRoot}
	if req.From != "" {
		args = append(args, "--from", req.From)
	}
	if req.Prompt != "" {
		args = append(args, "--prompt", req.Prompt)
	}
	args = appendCommonRunFlags(args, req)
	return args, nil
}

func buildTaskArgs(req RunRequest) ([]string, error) {
	if strings.TrimSpace(req.Skill) == "" {
		return nil, fmt.Errorf("%w: skill is required for task.run", ErrValidation)
	}
	args := []string{"task", req.Skill, "--quiet", "--cwd", req.ProjectRoot}
	if req.Agent != "" {
		args = append(args, "--agent", req.Agent)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Args != "" {
		args = append(args, "--args", req.Args)
	}
	if req.Prompt != "" {
		args = append(args, "--prompt", req.Prompt)
	}
	if req.PromptFlag != "" {
		args = append(args, "--prompt-flag", req.PromptFlag)
	}
	if req.TaskCommit != nil {
		// nil → no commit; empty string → bare --task-commit (child derives
		// the message); non-empty → --task-commit=<msg> (=-form avoids the
		// next-arg-is-value ambiguity of the sentinel default).
		if *req.TaskCommit == "" {
			args = append(args, "--task-commit")
		} else {
			args = append(args, "--task-commit="+*req.TaskCommit)
		}
	}
	args = appendCommonRunFlags(args, req)
	return args, nil
}

// appendCommonRunFlags adds the commit/upload flags shared by pipeline and
// task.
func appendCommonRunFlags(args []string, req RunRequest) []string {
	if req.NoCommit {
		args = append(args, "--no-commit")
	}
	if req.CommitAllowDirty {
		args = append(args, "--commit-allow-dirty")
	}
	if req.UploadTranscripts {
		args = append(args, "--upload-transcripts")
	}
	return args
}

// Spawner starts accepted jobs as `ape` child processes. apeBin is the path
// to the ape binary (os.Executable() in production; a fake in tests). The
// NATS fields are injected into the child's environment so the child
// publishes its own PLAN-13 events — carrying the daemon-injected
// APE_JOB_ID — under the daemon's credential.
type Spawner struct {
	apeBin       string
	natsURL      string
	natsCreds    string
	eventsPrefix string  // "" → child uses its default (ape.evt)
	cfg          *Config // consulted by the script argv builder (allowlist + D5 gates)
}

// NewSpawner builds a Spawner. A blank apeBin falls back to the running
// executable. cfg supplies the allowlist and script gates to BuildArgs (may
// be nil when only pipeline/task/prompt jobs are spawned, e.g. in tests).
func NewSpawner(apeBin, natsURL, natsCreds, eventsPrefix string, cfg *Config) (*Spawner, error) {
	if strings.TrimSpace(apeBin) == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("service: resolve ape binary: %w", err)
		}
		apeBin = exe
	}
	return &Spawner{apeBin: apeBin, natsURL: natsURL, natsCreds: natsCreds, eventsPrefix: eventsPrefix, cfg: cfg}, nil
}

// Spawn assembles the argv, opens the per-job log, and starts the child in
// its own process group with cwd = req.ProjectRoot. It launches a goroutine
// that waits for exit and calls onExit(exitCode) exactly once (closing the
// log first). It returns the child's pid and the log path. A build/start
// failure returns an error and does not call onExit.
//
// The child is deliberately NOT bound to a context: it must outlive the
// request handler, and the daemon controls its lifetime explicitly via
// terminateGroup (job.stop / drain).
func (s *Spawner) Spawn(kind Kind, jobID string, req RunRequest, onExit func(exitCode int)) (pid int, logPath string, err error) {
	args, err := BuildArgs(kind, req, s.cfg)
	if err != nil {
		return 0, "", err
	}
	// `ape prompt` has no NATS/eventing flags (it does not publish PLAN-13
	// events), so only forward a non-default prefix to the kinds that accept
	// it — otherwise the prompt child rejects an unknown flag.
	if s.eventsPrefix != "" && kind != KindPrompt {
		args = append(args, "--events-subject-prefix", s.eventsPrefix)
	}

	logPath, logFile, err := openJobLog(req.ProjectRoot, jobID)
	if err != nil {
		return 0, "", err
	}

	cmd := exec.Command(s.apeBin, args...) //nolint:gosec,noctx // binary is ape itself; args are typed field→flag (never shell); detached daemon child must outlive the request ctx
	cmd.Dir = req.ProjectRoot
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// A script_source job delivers its Go source on stdin (`ape script -`);
	// the source never rides the argv.
	if kind == KindScript && strings.TrimSpace(req.ScriptSource) != "" && strings.TrimSpace(req.ScriptPath) == "" {
		cmd.Stdin = strings.NewReader(req.ScriptSource)
	}
	cmd.Env = s.childEnv(jobID)
	configureProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, "", fmt.Errorf("service: start %s job: %w", kind, err)
	}
	pid = cmd.Process.Pid

	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		onExit(exitCodeFromWait(waitErr))
	}()

	return pid, logPath, nil
}

// childEnv builds the child's environment: the daemon's own environment
// plus APE_JOB_ID (so child event subjects carry the job id) and the
// resolved NATS URL/creds (so the child publishes even when the daemon was
// configured via flags). Later entries win, so these override any inherited
// values.
func (s *Spawner) childEnv(jobID string) []string {
	env := append(os.Environ(), "APE_JOB_ID="+jobID)
	if s.natsURL != "" {
		env = append(env, "APE_NATS_URL="+s.natsURL)
	}
	if s.natsCreds != "" {
		env = append(env, "APE_NATS_CREDS="+s.natsCreds)
	}
	return env
}

// openJobLog creates <projectRoot>/_output/ape/service/ and opens the job's
// append log for the child's combined stdout+stderr.
func openJobLog(projectRoot, jobID string) (string, *os.File, error) {
	dir := filepath.Join(projectRoot, "_output", "ape", "service")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", nil, fmt.Errorf("service: job log dir: %w", err)
	}
	path := filepath.Join(dir, jobID+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("service: open job log: %w", err)
	}
	return path, f, nil
}

// exitCodeFromWait extracts a child's exit code from cmd.Wait's error: 0 on
// success, the process exit code on a normal non-zero exit, or -1 when the
// child was signalled or failed to run.
func exitCodeFromWait(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// newJobID mints a job id of the form YYYYMMDD-HHMMSS-<7hex>, matching the
// run-id format (internal/pipeline computeRunID). The hex suffix is random
// (not content-derived) so two jobs submitted in the same second collide
// only on a 1-in-256M chance.
func newJobID(now time.Time) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("service: job id entropy: %w", err)
	}
	return fmt.Sprintf("%s-%s", now.UTC().Format("20060102-150405"), hex.EncodeToString(b[:])[:7]), nil
}
