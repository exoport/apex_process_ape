package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/exoport/apex_process_ape/internal/reporting"
	"github.com/exoport/apex_process_ape/internal/sessionref"
	"github.com/spf13/cobra"
)

// The PLAN-17 reporting commands (event/log/metrics/transcript) share the
// exit-code table in exitcodes.go with a reporting-specific reading:
//
//	0  published / uploaded
//	1  NATS publish or upload failed (connection was established)
//	2  usage error, no NATS configured, or the session was unresolvable
//
// All diagnostics go to stderr; stdout carries only the result object, so a
// consumer can parse `--output-format json` from stdout unambiguously.

// exitError couples an error with the exit code the command should return.
// The command cores return these; RunE maps them via runReport → os.Exit,
// so the cores stay os.Exit-free and testable in-process.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// usageErr → exit 2 (usage/config/unresolvable); failErr → exit 1 (an
// established connection then failed to publish/upload).
func usageErr(err error) error { return &exitError{code: ExitUsage, err: err} }
func failErr(err error) error  { return &exitError{code: ExitRunFailed, err: err} }

// runReport runs a reporting command core and maps its error onto the exit
// table: nil → nil (exit 0); an *exitError → stderr + os.Exit(code); any
// other error → stderr + exit 1.
func runReport(core func() error) error {
	err := core()
	if err == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	code := ExitRunFailed
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	os.Exit(code)
	return nil // unreachable
}

// reportFlags are the flags shared by the four reporting commands.
type reportFlags struct {
	natsURL          string
	natsCreds        string
	sessionID        string
	transcript       string
	cwd              string
	outputFormat     string
	jsonAlias        bool
	quiet            bool
	eventsPrefix     string
	debugSubjectUser string
}

// addReportFlags registers the shared flags. withEvtPrefix adds the ape.evt
// root override (only `ape event` and `ape transcript`, which publish on
// the evt root; log/metrics have fixed roots).
func addReportFlags(cmd *cobra.Command, f *reportFlags, withEvtPrefix bool) {
	cmd.Flags().StringVar(&f.natsURL, "nats-url", "", "NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2).")
	cmd.Flags().StringVar(&f.natsCreds, "nats-creds", "", "NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS).")
	cmd.Flags().StringVar(&f.sessionID, "session-id", "", "Claude session id to report for (default: auto-resolve the current project's newest).")
	cmd.Flags().StringVar(&f.transcript, "transcript", "", "Explicit transcript file; the session id is parsed from its name.")
	cmd.Flags().StringVar(&f.cwd, "cwd", "", "Project root for session auto-resolution (default: current working dir).")
	cmd.Flags().StringVar(&f.outputFormat, "output-format", "human", "Output format: human|json (result object on stdout, diagnostics on stderr).")
	cmd.Flags().BoolVar(&f.jsonAlias, "json", false, "Alias for --output-format json")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().BoolVar(&f.quiet, "quiet", false, "Suppress the human-mode confirmation line.")
	cmd.Flags().StringVar(&f.debugSubjectUser, "debug-subject-user", "", "TEST-ONLY: override the <user> subject token (demonstrates server-enforced identity).")
	_ = cmd.Flags().MarkHidden("debug-subject-user")
	if withEvtPrefix {
		cmd.Flags().StringVar(&f.eventsPrefix, "events-subject-prefix", eventing.DefaultPrefix, "Subject root for the published event.")
	}
}

func (f *reportFlags) jsonMode() bool { return f.jsonAlias || f.outputFormat == "json" }

// projectRoot resolves --cwd or the working directory.
func (f *reportFlags) projectRoot() (string, error) {
	if f.cwd != "" {
		return f.cwd, nil
	}
	return os.Getwd()
}

// setupReporter validates output-format, resolves the NATS config and the
// target session, and connects a Reporter. It returns exitErrors: exit 2
// for usage/no-config/unresolvable, exit 1 for a connection failure. The
// caller must defer r.Close() on success.
func setupReporter(ctx context.Context, f *reportFlags) (*reporting.Reporter, sessionref.Ref, error) {
	if !f.jsonMode() && f.outputFormat != "human" {
		return nil, sessionref.Ref{}, usageErr(fmt.Errorf("--output-format must be human or json, got %q", f.outputFormat))
	}
	cfg := natsconn.Resolve(f.natsURL, f.natsCreds)
	if !cfg.Enabled() {
		return nil, sessionref.Ref{}, usageErr(errors.New("no NATS URL configured — set --nats-url or APE_NATS_URL"))
	}
	project, err := f.projectRoot()
	if err != nil {
		return nil, sessionref.Ref{}, usageErr(err)
	}
	ref, err := sessionref.Resolve(sessionref.Options{
		SessionID:  f.sessionID,
		Transcript: f.transcript,
		Cwd:        project,
	})
	if err != nil {
		return nil, sessionref.Ref{}, usageErr(err) // unresolvable session is a usage/config error
	}
	r, err := reporting.Connect(ctx, cfg, "ape/"+Version, reporting.Options{
		Project:     project,
		EvtPrefix:   f.eventsPrefix,
		SubjectUser: f.debugSubjectUser,
	})
	if err != nil {
		return nil, sessionref.Ref{}, failErr(fmt.Errorf("connect NATS: %w", err)) // connection failure → exit 1
	}
	return r, ref, nil
}
