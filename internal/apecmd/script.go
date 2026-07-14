package apecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
	"github.com/traefik/yaegi/stdlib/syscall"
	"github.com/traefik/yaegi/stdlib/unrestricted"
	"github.com/traefik/yaegi/stdlib/unsafe"

	"github.com/exoport/apex_process_ape/apescript"
	"github.com/exoport/apex_process_ape/internal/apescriptsym"
	"github.com/exoport/apex_process_ape/internal/blobstore"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// scriptStdinArg is the positional value that switches `ape script` to read
// the Go source from stdin.
const scriptStdinArg = "-"

// panicTailBytes bounds the yaegi stderr tail retained for panic reporting.
const panicTailBytes = 8 << 10

// scriptOptions bundles the resolved `ape script` invocation parameters.
type scriptOptions struct {
	source       string // the Go source text
	scriptPath   string // absolute path when read from a file; "" for stdin
	scriptArgs   []string
	sandbox      bool
	quiet        bool
	projectRoot  string
	format       output.Format
	base         runConfig // shared NATS/eventing knobs
	claudeBin    string    // test seam: overrides the spawned claude
	interpStdout io.Writer // test seam: overrides the interpreter's stdout
}

func newScriptCmd() *cobra.Command {
	var (
		cwdFlag           string
		sandboxFlag       bool
		quietFlag         bool
		outputFormat      string
		natsURLFlag       string
		natsCredsFlag     string
		eventsPrefixFlag  string
		uploadTranscripts bool
		transcriptStore   string
	)
	cmd := &cobra.Command{
		Use:   "script <file.go> [flags] [-- script-args...]",
		Short: "Run a Go orchestration script through the yaegi interpreter",
		Long: `Run a plain Go file inside ape's process under the yaegi interpreter,
with the apescript library injected so the script can drive ape's
primitives — run a pipeline, task, or prompt (all PTY-backed, the same
runners the CLI uses), read manifests, scan transcripts, log, publish
events, and upload blobs — as one deterministic, version-controlled Go
file instead of a shell wrapper around the CLI.

The file must define:

    func Main(ctx context.Context) error

ape evaluates the file, then calls Main. A non-nil error (or a panic,
which is recovered and reported with the yaegi stack) exits 1; SIGINT
cancels the context so the in-flight run tears down cleanly.

Use "-" as the file to read the script from stdin. Everything after a
"--" separator is exposed to the script as apescript.Args().

  ape script ops/nightly.go -- --target ./component-a
  cat ops/nightly.go | ape script -

By default the interpreter is unrestricted (full stdlib — arbitrary
trusted code, same trust level as your shell). --sandbox switches to
yaegi's restricted symbol set, which blocks os/exec, os.Exit, syscall,
and unsafe while keeping the apescript orchestration surface fully
available. See docs/reference/apescript.md for the per-group rules.

Exit codes: 0 success · 1 the script returned an error, panicked, or a
launched run failed · 2 usage or read error (no file, bad flags).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format := output.Format(outputFormat)
			if format != output.FormatHuman && format != output.FormatJSON && format != output.FormatYAML {
				fmt.Fprintf(os.Stderr, "Error: --output-format must be human, json, or yaml, got %q\n", outputFormat)
				os.Exit(ExitUsage)
			}

			// argsLenAtDash splits the positional file from the script args
			// after `--`. Everything before the dash beyond the file is a
			// usage error; everything after is exposed via apescript.Args().
			scriptArgs := []string{}
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				scriptArgs = args[dash:]
				args = args[:dash]
			}
			if len(args) != 1 {
				fmt.Fprintln(os.Stderr, "Error: ape script takes exactly one <file.go> (or -) before any -- separator")
				os.Exit(ExitUsage)
			}

			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: cannot determine working directory: %s\n", err)
					os.Exit(ExitUsage)
				}
				projectRoot = wd
			}

			source, scriptPath, err := readScriptSource(args[0], os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err)
				os.Exit(ExitUsage)
			}

			// SIGINT cancels the context so an in-flight run tears down and
			// Main sees a cancelled ctx.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			return runScript(ctx, scriptOptions{
				source:      source,
				scriptPath:  scriptPath,
				scriptArgs:  scriptArgs,
				sandbox:     sandboxFlag,
				quiet:       quietFlag,
				projectRoot: projectRoot,
				format:      format,
				base: runConfig{
					natsURL:           natsURLFlag,
					natsCreds:         natsCredsFlag,
					eventsPrefix:      eventsPrefixFlag,
					uploadTranscripts: uploadTranscripts,
					transcriptStore:   transcriptStore,
				},
			})
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	cmd.Flags().BoolVar(&sandboxFlag, "sandbox", false, "Run the script in the restricted interpreter (blocks os/exec, os.Exit, syscall, unsafe)")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "Suppress apescript.Log output")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml (json/yaml wrap the run in {result, duration, cost_usd})")
	addNatsFlags(cmd, &natsURLFlag, &natsCredsFlag, &eventsPrefixFlag, &uploadTranscripts, &transcriptStore)
	return cmd
}

// readScriptSource resolves the positional argument to Go source: "-" reads
// stdin, anything else is a file path (returned absolute for byte-exact
// compile-error positions).
func readScriptSource(arg string, stdin io.Reader) (source, scriptPath string, err error) {
	if arg == scriptStdinArg {
		data, rerr := io.ReadAll(stdin)
		if rerr != nil {
			return "", "", fmt.Errorf("read script from stdin: %w", rerr)
		}
		return string(data), "", nil
	}
	abs, aerr := filepath.Abs(arg)
	if aerr != nil {
		return "", "", fmt.Errorf("resolve script path: %w", aerr)
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		return "", "", fmt.Errorf("read script %s: %w", abs, rerr)
	}
	return string(data), abs, nil
}

// scriptEnvelope is the `--output-format json|yaml` result printed on stdout.
//
//nolint:tagliatelle // wire contract: result / duration / cost_usd
type scriptEnvelope struct {
	Result          string  `json:"result"   yaml:"result"`
	Success         bool    `json:"success"  yaml:"success"`
	DurationSeconds float64 `json:"duration" yaml:"duration"`
	CostUSD         float64 `json:"cost_usd" yaml:"cost_usd"`
}

// runScript builds the interpreter, wires the apescript runtime, evaluates the
// script, and calls Main — mapping the outcome onto the exit-code table and
// (in json/yaml mode) the result envelope.
func runScript(ctx context.Context, o scriptOptions) error {
	runID := runlog.NewChatID(time.Now(), o.projectRoot, os.Getpid())

	cfg := o.base
	cfg.kind = eventing.KindScript
	cfg.quiet = o.quiet
	eventConn, identity := startEventing(ctx, cfg)
	defer func() {
		if eventConn != nil {
			_ = eventConn.Drain()
		}
	}()
	pub := newEventPublisher(eventConn, identity, o.projectRoot, runID, cfg)
	defer pub.Close()

	runner := &scriptRunner{
		projectRoot: o.projectRoot,
		quiet:       o.quiet,
		base:        cfg,
		claudeBin:   o.claudeBin,
	}

	// stdout routing: human mode lets the script print to stdout; json/yaml
	// mode reserves stdout for the envelope and diverts script prints to
	// stderr. Overridable for tests.
	interpStdout := o.interpStdout
	if interpStdout == nil {
		if o.format == output.FormatHuman {
			interpStdout = os.Stdout
		} else {
			interpStdout = os.Stderr
		}
	}

	acfg := apescript.Config{
		ProjectRoot: o.projectRoot,
		Args:        o.scriptArgs,
		Quiet:       o.quiet,
		Sandbox:     o.sandbox,
		RunID:       runID,
		LogWriter:   os.Stderr,
		RunPipeline: runner.runPipeline,
		RunTask:     runner.runTask,
		RunPrompt:   runner.runPrompt,
	}
	if pub != nil {
		acfg.Publish = func(event string, v any) error {
			pub.Emit(event, map[string]any{"payload": v})
			return nil
		}
	}
	if eventConn != nil {
		acfg.PutBlob = scriptBlobPutter(eventConn, o.projectRoot, runID, cfg.transcriptStore)
	}
	restore := apescript.Activate(acfg)
	defer restore()

	i, tail := buildScriptInterp(o.sandbox, interpStdout)

	start := time.Now()
	if err := evalScript(i, o.source, o.scriptPath); err != nil {
		// Compile/evaluation error (includes file:line from yaegi). No Main
		// was called, so no claude was ever spawned. Returning the error lets
		// the deferred eventing teardown run before the process exits 1.
		return errors.New(sandboxSymbolHint(err, o.sandbox))
	}
	mainFn, err := lookupScriptMain(i)
	if err != nil {
		return err
	}

	runErr := callScriptMain(ctx, mainFn, tail)
	duration := time.Since(start)

	if o.format != output.FormatHuman {
		env := scriptEnvelope{
			Result:          "ok",
			Success:         runErr == nil,
			DurationSeconds: duration.Seconds(),
			CostUSD:         runner.totalCost(),
		}
		if runErr != nil {
			env.Result = runErr.Error()
		}
		if perr := output.Print(os.Stdout, o.format, env); perr != nil {
			return perr
		}
	}

	// A non-nil runErr propagates: main prints "Error: …" and exits 1 with the
	// deferred publisher flush / conn drain already run.
	return runErr
}

// buildScriptInterp constructs the yaegi interpreter with the stdlib + apescript
// symbol sets appropriate for the mode. Unrestricted (default) loads the full
// stdlib plus syscall/unsafe/unrestricted overrides; sandbox loads only the
// restricted stdlib. It returns the interpreter and the tail writer that
// captures yaegi's panic stack for reporting.
func buildScriptInterp(sandbox bool, stdout io.Writer) (*interp.Interpreter, *tailWriter) {
	tail := newTailWriter(os.Stderr, panicTailBytes)
	i := interp.New(interp.Options{
		Unrestricted: !sandbox,
		Env:          os.Environ(),
		Stdout:       stdout,
		Stderr:       tail,
	})
	// stdlib first so the unrestricted overrides below replace the sandboxed
	// versions of os.Exit et al. (order matters — see yaegi cmd/run.go).
	_ = i.Use(stdlib.Symbols)
	if !sandbox {
		_ = i.Use(syscall.Symbols)
		_ = i.Use(unsafe.Symbols)
		_ = i.Use(unrestricted.Symbols)
	}
	// The apescript orchestration surface is available in BOTH modes: it is
	// the intended, guard-railed side-effect channel.
	_ = i.Use(apescriptsym.Symbols)
	return i, tail
}

// sandboxSymbolHint rewrites yaegi's opaque import error into a clear
// symbol-not-allowed message when running --sandbox, so a script reaching for
// os/exec (or another restricted package) gets an actionable explanation
// instead of a raw source-resolution error.
//
// yaegi renders a blocked/unresolvable import as
//
//	<file>:<line>:<col>: import "<pkg>" error: <reason>
//
// where <reason> is OS-specific: Linux says "unable to find source related to
// … GoPath", Windows fails the source-load fallback with an "open <path>: The
// filename … is incorrect". We key the rewrite on the stable `import "<pkg>"
// error:` prefix, not the platform-specific reason, so it fires on every OS.
// In --sandbox mode any import that fails to resolve is a package outside the
// restricted symbol set, which is exactly what we want to report as blocked.
func sandboxSymbolHint(err error, sandbox bool) string {
	msg := err.Error()
	impIdx := strings.Index(msg, `import "`)
	if !sandbox || impIdx < 0 || !strings.Contains(msg[impIdx:], `" error:`) {
		return msg
	}
	pkg := ""
	rest := msg[impIdx+len(`import "`):]
	if j := strings.Index(rest, `"`); j >= 0 {
		pkg = rest[:j]
	}
	// yaegi's message is "<file>:<line>:<col>: import "pkg" error: …"; the
	// text before `: import "` is the source location.
	loc := msg
	if i := strings.Index(msg, `: import "`); i >= 0 {
		loc = msg[:i]
	}
	return fmt.Sprintf("%s: package %q is not allowed in --sandbox mode "+
		"(the restricted interpreter blocks os/exec, os.Exit, syscall, unsafe and other escape hatches — "+
		"drop --sandbox to run unrestricted, or use the apescript orchestration functions instead)", loc, pkg)
}

// evalScript evaluates the script source. A file path is evaluated via
// EvalPath so compile errors carry the real filename:line; stdin source is
// evaluated in-memory. Either way a compile error returns before any top-level
// code runs, so a broken script never spawns claude.
func evalScript(i *interp.Interpreter, source, scriptPath string) error {
	if scriptPath != "" {
		_, err := i.EvalPath(scriptPath)
		return err
	}
	_, err := i.Eval(source)
	return err
}

// lookupScriptMain resolves the script's `func Main(ctx context.Context) error`.
func lookupScriptMain(i *interp.Interpreter) (func(context.Context) error, error) {
	v, err := i.Eval("main.Main")
	if err != nil {
		return nil, errors.New("script must define `func Main(ctx context.Context) error` in package main")
	}
	fn, ok := v.Interface().(func(context.Context) error)
	if !ok {
		return nil, fmt.Errorf("script Main has wrong signature %s; want func(context.Context) error", v.Type())
	}
	return fn, nil
}

// callScriptMain invokes Main with panic recovery. A recovered panic is
// reported with the yaegi source-position stack (captured on tail) and mapped
// to a run error.
func callScriptMain(ctx context.Context, fn func(context.Context) error, tail *tailWriter) (err error) {
	defer func() {
		if r := recover(); r != nil {
			val := r
			if rv, ok := r.(reflect.Value); ok {
				val = rv.Interface()
			}
			stack := tail.String()
			if stack != "" {
				err = fmt.Errorf("script panicked: %v\nyaegi stack:\n%s", val, stack)
			} else {
				err = fmt.Errorf("script panicked: %v", val)
			}
		}
	}()
	return fn(ctx)
}

// scriptBlobPutter returns a PutBlob hook backed by a lazily-created blob
// store on the run's NATS connection. The store is built on first use so a
// script that never calls PutBlob pays nothing.
func scriptBlobPutter(conn *nats.Conn, projectRoot, runID, storeKind string) func(context.Context, io.Reader) (apescript.Digest, string, error) {
	var (
		once  sync.Once
		store blobstore.Store
		sErr  error
	)
	return func(ctx context.Context, r io.Reader) (apescript.Digest, string, error) {
		once.Do(func() { store, sErr = newTranscriptStore(ctx, conn, projectRoot, runID, storeKind) })
		if sErr != nil {
			return apescript.Digest{}, "", sErr
		}
		raw, err := io.ReadAll(r)
		if err != nil {
			return apescript.Digest{}, "", fmt.Errorf("apescript.PutBlob: read: %w", err)
		}
		d := blobstore.DigestOf(raw)
		comp, err := blobstore.Compress(raw)
		if err != nil {
			return apescript.Digest{}, "", fmt.Errorf("apescript.PutBlob: compress: %w", err)
		}
		uri, _, err := store.Put(ctx, d, int64(len(raw)), int64(len(comp)), bytes.NewReader(comp))
		if err != nil {
			return apescript.Digest{}, "", err
		}
		return d, uri, nil
	}
}

// tailWriter tees writes to an underlying writer while retaining the last n
// bytes, so a panic stack yaegi prints to Stderr can be recovered afterward.
type tailWriter struct {
	w   io.Writer
	max int

	mu  sync.Mutex
	buf []byte
}

func newTailWriter(w io.Writer, maxBytes int) *tailWriter {
	return &tailWriter{w: w, max: maxBytes}
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	t.mu.Unlock()
	if t.w != nil {
		return t.w.Write(p)
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(append([]byte(nil), t.buf...)))
}
