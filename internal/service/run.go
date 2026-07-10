package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/nats-io/nats.go/micro"
)

// ErrNoURL is returned by Run when no NATS URL is configured. The command
// maps it to a usage/config exit (exit 2).
var ErrNoURL = errors.New("service: no NATS URL configured — set --nats-url or APE_NATS_URL")

// ErrConfig wraps a config/usage failure (bad --name, missing/invalid
// service.yaml). The command maps it to a usage exit (exit 2); everything
// else Run returns (connect/registration failures) is a runtime exit (1).
var ErrConfig = errors.New("service: configuration error")

// validNameRe constrains --name so it is a valid single NATS subject token
// AND a valid micro service name (alnum, dash, underscore; lowercase to
// keep the $SRV name and the subject token identical).
var validNameRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// semVerRe is the SemVer shape micro.AddService requires for Version. ape's
// dev builds report "dev"/"(devel)", which is not SemVer — microVersion
// falls back to 0.0.0 for those (the real version still rides Metadata).
var semVerRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-.]+)?(?:\+[0-9A-Za-z-.]+)?$`)

// Options configures Run. All strings resolve flags → env upstream in the
// command; Run treats them as final.
type Options struct {
	Name         string        // service name (default "ape"); the <name> subject + $SRV token
	ConfigPath   string        // explicit service.yaml (else _apex/ → ~/.ape/)
	ProjectRoot  string        // cwd, for config resolution
	NatsURL      string        // required
	NatsCreds    string        // .creds file (identity baked into subjects)
	EventsPrefix string        // ape.evt override for job lifecycle events
	DrainTimeout time.Duration // 0 = indefinite (a second signal forces)
	ApeVersion   string        // apecmd.Version
	ApeBin       string        // "" → os.Executable()
	Stderr       io.Writer     // diagnostics sink (default os.Stderr)
}

// Run loads the allowlist config, connects to NATS, registers the micro
// service + endpoints, and serves until SIGINT/SIGTERM or ctx cancellation,
// then drains gracefully: it stops accepting new jobs, waits for in-flight
// children (indefinitely by default, or up to DrainTimeout), and a second
// signal force-terminates them.
func Run(ctx context.Context, o Options) error {
	stderr := o.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	name := strings.TrimSpace(o.Name)
	if name == "" {
		name = defaultServiceName
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("%w: invalid --name %q (use lowercase letters, digits, '-' or '_')", ErrConfig, name)
	}

	cfg, err := LoadConfig(o.ConfigPath, o.ProjectRoot)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConfig, err)
	}

	ncfg := natsconn.Resolve(o.NatsURL, o.NatsCreds)
	if !ncfg.Enabled() {
		return ErrNoURL
	}
	conn, err := natsconn.Connect(ctx, ncfg, "ape-service/"+o.ApeVersion)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Drain() }()
	identity, _ := ncfg.Identity() // best-effort; a missing/invalid creds file → anonymous token

	daemon, err := NewDaemon(ctx, DaemonConfig{
		Name:         name,
		Config:       cfg,
		Conn:         conn,
		Identity:     identity,
		EventsPrefix: o.EventsPrefix,
		ApeVersion:   o.ApeVersion,
		ApeBin:       o.ApeBin,
		NatsURL:      ncfg.URL,
		NatsCreds:    ncfg.CredsFile,
	})
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()
	svc, err := micro.AddService(conn, micro.Config{
		Name:        name,
		Version:     microVersion(o.ApeVersion),
		Description: "ape job daemon (PLAN-14): pipeline/task/command/script jobs over NATS request/reply",
		Metadata: map[string]string{
			"project":     cfg.ProjectRoot,
			"hostname":    hostname,
			"ape_version": o.ApeVersion,
		},
	})
	if err != nil {
		return fmt.Errorf("service: register micro service: %w", err)
	}
	if err := daemon.Register(svc); err != nil {
		return err
	}
	// Ensure the endpoint subscriptions are active server-side before we
	// announce readiness — otherwise an eager client could discover the
	// service (via $SRV) yet get no-responders on a just-added endpoint.
	_ = conn.Flush()

	fmt.Fprintf(stderr, "▶ ape service %q on ape.svc.%s.%s.>  (project %s, %d allowed root(s), %d running)\n",
		name, name, daemon.projectSlug, cfg.ProjectRoot, len(cfg.Allow), daemon.RunningCount())
	fmt.Fprintf(stderr, "  discovery: $SRV.PING.%s · stop: SIGTERM (drain), SIGTERM×2 (force)\n", name)

	return serveUntilSignal(ctx, daemon, o.DrainTimeout, stderr)
}

// serveUntilSignal blocks until the first shutdown trigger, then runs the
// graceful drain. Extracted so the signal/drain choreography is readable.
// The caller's deferred conn.Drain flushes pending job-end publishes after
// this returns.
func serveUntilSignal(ctx context.Context, daemon *Daemon, drainTimeout time.Duration, stderr io.Writer) error {
	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)

	select {
	case <-ctx.Done():
	case <-sigc:
	}

	fmt.Fprintln(stderr, "⇣ ape service: draining — not accepting new jobs")
	_ = daemon.StopAccepting()

	drained := make(chan struct{})
	go func() {
		daemon.WaitJobs()
		close(drained)
	}()

	var timeoutC <-chan time.Time
	if drainTimeout > 0 {
		t := time.NewTimer(drainTimeout)
		defer t.Stop()
		timeoutC = t.C
	}

	for {
		select {
		case <-drained:
			fmt.Fprintln(stderr, "✓ ape service: drained, shutting down")
			return nil
		case <-sigc:
			fmt.Fprintln(stderr, "⇣ ape service: second signal — terminating in-flight jobs")
			daemon.KillAll()
		case <-timeoutC:
			fmt.Fprintf(stderr, "⇣ ape service: drain timeout (%s) — terminating in-flight jobs\n", drainTimeout)
			daemon.KillAll()
			timeoutC = nil // fire once
		}
	}
}

// microVersion returns a SemVer string acceptable to micro.AddService: the
// ape version when it is already SemVer, else 0.0.0 (dev builds).
func microVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if semVerRe.MatchString(v) {
		return v
	}
	return "0.0.0"
}
