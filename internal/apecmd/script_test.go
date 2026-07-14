package apecmd //nolint:testpackage // white-box: exercises unexported interpreter + runner helpers

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/apescript"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/stretchr/testify/require"
)

// scriptFixture returns the absolute path to a testdata script.
func scriptFixture(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "scripts", name))
	require.NoError(t, err)
	require.FileExists(t, abs)
	return abs
}

// evalScriptFixture composes the interpreter pipeline (build → eval → lookup →
// call Main) exactly as runScript does, minus os.Exit / eventing, so the pieces
// are unit-testable. Returns the eval error (nil when compilation succeeded)
// and the run error from Main.
func evalScriptFixture(ctx context.Context, t *testing.T, sandbox bool, name string, stdout *bytes.Buffer) (evalErr, runErr error) {
	t.Helper()
	path := scriptFixture(t, name)
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	i, tail := buildScriptInterp(sandbox, stdout)
	if evalErr := evalScript(i, string(src), path); evalErr != nil {
		return evalErr, nil
	}
	fn, err := lookupScriptMain(i)
	require.NoError(t, err)
	return nil, callScriptMain(ctx, fn, tail)
}

func TestReadScriptSource(t *testing.T) {
	// stdin variant.
	src, path, err := readScriptSource("-", bytes.NewBufferString("package main\n"))
	require.NoError(t, err)
	require.Equal(t, "package main\n", src)
	require.Empty(t, path)

	// file variant returns absolute path.
	f := scriptFixture(t, "hello.go")
	src, path, err = readScriptSource(f, nil)
	require.NoError(t, err)
	require.Contains(t, src, "func Main")
	require.Equal(t, f, path)

	// missing file.
	_, _, err = readScriptSource(filepath.Join(t.TempDir(), "nope.go"), nil)
	require.Error(t, err)
}

func TestScript_ArgsEcho(t *testing.T) {
	restore := apescript.Activate(apescript.Config{Args: []string{"alpha", "beta"}})
	defer restore()
	var out bytes.Buffer
	evalErr, runErr := evalScriptFixture(context.Background(), t, false, "args_echo.go", &out)
	require.NoError(t, evalErr)
	require.NoError(t, runErr)
	require.Equal(t, "arg[0]=alpha\narg[1]=beta\n", out.String())
}

func TestScript_LogCaptureAndQuiet(t *testing.T) {
	// Not quiet: both lines reach the log writer.
	var logBuf bytes.Buffer
	restore := apescript.Activate(apescript.Config{LogWriter: &logBuf})
	var out bytes.Buffer
	_, runErr := evalScriptFixture(context.Background(), t, false, "log_capture.go", &out)
	restore()
	require.NoError(t, runErr)
	require.Contains(t, logBuf.String(), "first log line")
	require.Contains(t, logBuf.String(), "second log line value=42")

	// Quiet: nothing.
	logBuf.Reset()
	restore = apescript.Activate(apescript.Config{LogWriter: &logBuf, Quiet: true})
	_, runErr = evalScriptFixture(context.Background(), t, false, "log_capture.go", &out)
	restore()
	require.NoError(t, runErr)
	require.Empty(t, logBuf.String())
}

func TestScript_PanicRecovery(t *testing.T) {
	restore := apescript.Activate(apescript.Config{})
	defer restore()
	var out bytes.Buffer
	_, runErr := evalScriptFixture(context.Background(), t, false, "panic.go", &out)
	require.Error(t, runErr)
	require.Contains(t, runErr.Error(), "script panicked")
	require.Contains(t, runErr.Error(), "deliberate script panic")
	// The yaegi source-position stack is included.
	require.Contains(t, runErr.Error(), "panic.go:")
}

func TestScript_ContextCancellation(t *testing.T) {
	restore := apescript.Activate(apescript.Config{})
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	var out bytes.Buffer
	_, runErr := evalScriptFixture(ctx, t, false, "cancel.go", &out)
	require.ErrorIs(t, runErr, context.Canceled)
	require.Contains(t, out.String(), "cancelled")
}

func TestScript_SandboxBlocksExec(t *testing.T) {
	restore := apescript.Activate(apescript.Config{})
	defer restore()
	var out bytes.Buffer

	// Sandbox: os/exec import is rejected at evaluation, before Main runs.
	evalErr, _ := evalScriptFixture(context.Background(), t, true, "exec_probe.go", &out)
	require.Error(t, evalErr)
	require.Contains(t, sandboxSymbolHint(evalErr, true), "not allowed in --sandbox mode")
	require.Contains(t, sandboxSymbolHint(evalErr, true), "os/exec")

	// Unrestricted: the same script compiles (Main not required to succeed —
	// we only assert evaluation passed the import gate).
	out.Reset()
	evalErr2, _ := evalScriptFixture(context.Background(), t, false, "exec_probe.go", &out)
	require.NoError(t, evalErr2)
}

// TestScript_RunTaskBothModes proves the apescript orchestration surface is
// reachable in BOTH sandbox and unrestricted mode: a script calling
// apescript.RunTask dispatches through the installed hook either way.
func TestScript_RunTaskBothModes(t *testing.T) {
	for _, sandbox := range []bool{false, true} {
		called := false
		restore := apescript.Activate(apescript.Config{
			Args: []string{"apex-x"},
			RunTask: func(_ context.Context, o apescript.TaskOpts) (apescript.RunResult, error) {
				called = true
				require.Equal(t, "apex-x", o.Skill)
				return apescript.RunResult{RunID: "rid", Status: "completed"}, nil
			},
		})
		var out bytes.Buffer
		evalErr, runErr := evalScriptFixture(context.Background(), t, sandbox, "run_task.go", &out)
		restore()
		require.NoError(t, evalErr, "sandbox=%v", sandbox)
		require.NoError(t, runErr, "sandbox=%v", sandbox)
		require.True(t, called, "RunTask hook must fire (sandbox=%v)", sandbox)
		require.Contains(t, out.String(), "status=completed")
	}
}

// TestScriptRunner_RunTaskAgainstBashStandin drives a real RunTask through the
// interactive PTY runner against a bash stand-in for claude (mirrors
// internal/pipeline's singlestep test). The shim never fires a Stop hook, so
// the step idles out — but the run still spawns claude in a PTY, delivers the
// prompt, and finalizes a manifest, which is what RunTask must surface.
func TestScriptRunner_RunTaskAgainstBashStandin(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("POSIX PTY test; skipping on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("init")
	git("commit", "--allow-empty", "-m", "base")

	// Framework skill so PreflightSkills resolves.
	skillDir := filepath.Join(root, ".claude", "skills", "apex-shard-doc")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# shard"), 0o644))

	// bash stand-in for claude: presents a ❯ prompt.
	shim := filepath.Join(t.TempDir(), "claude-shim.sh")
	shimBody := "#!/bin/sh\nPS1='❯ '\nexport PS1\nexec bash --noprofile --norc\n"
	require.NoError(t, os.WriteFile(shim, []byte(shimBody), 0o755))

	runner := &scriptRunner{projectRoot: root, quiet: true, claudeBin: shim}
	res, runErr := runner.runTask(ctx, apescript.TaskOpts{Skill: "apex-shard-doc", IdleTimeout: 3 * time.Second})

	// The idle backstop fires (no bridge Stop hook from bash), so runErr is
	// non-nil — but a manifest was finalized and RunResult carries it.
	require.Error(t, runErr)
	require.NotEmpty(t, res.RunID, "manifest run_id must be surfaced")
	require.FileExists(t, res.ManifestPath)
	require.NotEmpty(t, res.Status)
	require.NotNil(t, res.CommitSHAs)
}

func TestSandboxSymbolHint(t *testing.T) {
	// The reason after `import "<pkg>" error:` is OS-specific; the rewrite
	// must fire on both. Linux: the "unable to find source / GoPath" form.
	// Windows: yaegi's source-load fallback fails with a filesystem error.
	linux := `/x/foo.go:6:2: import "os/exec" error: unable to find source related to: "os/exec". Either the GOPATH environment variable, or the Interpreter.Options.GoPath needs to be set`
	windows := `C:\x\foo.go:6:2: import "os/exec" error: open src\D:\a\repo\repo\testdata\vendor: The filename, directory name, or volume label syntax is incorrect.`

	for name, raw := range map[string]string{"linux": linux, "windows": windows} {
		t.Run(name, func(t *testing.T) {
			// Non-sandbox leaves it verbatim.
			require.Equal(t, raw, sandboxSymbolHint(stubError(raw), false))
			// Sandbox rewrites to the clear message with package + the
			// actionable --sandbox explanation, dropping the raw reason.
			got := sandboxSymbolHint(stubError(raw), true)
			require.Contains(t, got, `package "os/exec" is not allowed in --sandbox mode`)
			require.NotContains(t, got, "GOPATH")
			require.NotContains(t, got, "syntax is incorrect")
		})
	}
}

func TestScriptCommandFlagSurface(t *testing.T) {
	cmd := newScriptCmd()
	for _, name := range []string{
		"cwd", "sandbox", "quiet", "output-format",
		"nats-url", "nats-creds", "events-subject-prefix",
		"upload-transcripts", "transcript-store",
	} {
		require.NotNil(t, cmd.Flags().Lookup(name), "missing flag --%s", name)
	}
}

func TestScriptEnvelopeShape(t *testing.T) {
	env := scriptEnvelope{Result: "ok", Success: true, DurationSeconds: 1.5, CostUSD: 0.25}
	var buf bytes.Buffer
	require.NoError(t, output.Print(&buf, output.FormatJSON, env))
	for _, key := range []string{`"result"`, `"success"`, `"duration"`, `"cost_usd"`} {
		require.Contains(t, buf.String(), key)
	}
}

// stubError is a trivial error carrying a fixed message.
type stubError string

func (e stubError) Error() string { return string(e) }
