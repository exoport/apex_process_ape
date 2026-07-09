// Package reporting is the single source of the four PLAN-17 report
// shapes — event, log, metrics, transcript-upload — published over NATS.
// Both entry points use it: the standalone `ape event`/`log`/`metrics`/
// `transcript` commands and the PTY runners at finalize, so a supervised
// run and a self-reporting agent emit byte-compatible payloads on the same
// taxonomy (docs/reference/events.md).
//
// Unlike the fire-and-forget eventing publisher, reporting publishes
// SYNCHRONOUSLY and surfaces failures: a publish rejected by the server
// (an identity the credential is not scoped for) is detected via
// nc.LastError() after a Flush round-trip and returned as an error, so the
// standalone commands can exit non-zero (PLAN-17 D5). The runner reuses the
// same builders but ignores the error (its taps stay fire-and-forget).
//
// Every subject carries the <user> token decoded from the .creds identity
// (natsconn.Identity) — server-enforceable when the operator scopes publish
// permissions to `ape.*.<token>.>` — and every payload carries the resolved
// session id.
package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/natsconn"
	"github.com/nats-io/nats.go"
)

// Subject roots. ape.evt is overridable (--events-subject-prefix); ape.log
// and ape.metrics are fixed, versioned, additive-only roots.
const (
	rootLog     = "ape.log"
	rootMetrics = "ape.metrics"

	// LevelDebug…LevelError are the valid `ape log` levels.
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"

	// EventTranscriptUploaded is the companion event `ape transcript upload`
	// publishes with the uploaded blobs' digest map.
	EventTranscriptUploaded = "transcript-uploaded"
)

// Options configures a Reporter.
type Options struct {
	// Identity is the decoded credential identity; its SubjectToken is the
	// <user> subject segment and Name/PublicKey fill the payload user block.
	Identity natsconn.Identity
	// Project is the project root; slugged into the <project> segment.
	Project string
	// EvtPrefix overrides the ape.evt root (default eventing.DefaultPrefix).
	EvtPrefix string
	// SubjectUser overrides the <user> token (test-only seam behind
	// --debug-subject-user, to prove server-enforced identity rejects a
	// forged token). Empty uses Identity.SubjectToken.
	SubjectUser string
}

// Reporter publishes the four report shapes on one connection.
type Reporter struct {
	nc      *nats.Conn
	owns    bool // true when Connect opened nc (Close drains it)
	prefix  string
	userTok string
	userBlk eventing.User
	project string
	now     func() time.Time
}

// LevelValid reports whether level is one of the four accepted log levels.
func LevelValid(level string) bool {
	switch level {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
		return true
	default:
		return false
	}
}

// Connect opens a reporting connection for the standalone commands. It
// swaps the default stderr async-error handler for a silent one; publish
// rejections are detected synchronously via nc.LastError() and returned by
// the publish methods (PLAN-17 D5). Returns ErrDisabled when cfg carries no
// URL. The caller must Close the returned Reporter.
func Connect(ctx context.Context, cfg natsconn.Config, name string, opts Options) (*Reporter, error) {
	if !cfg.Enabled() {
		return nil, ErrDisabled
	}
	// Decode the identity from the credential offline unless the caller
	// supplied one. A missing/invalid creds file leaves a zero identity
	// (the "anonymous" subject token) — the server is the authority.
	if (opts.Identity == natsconn.Identity{}) {
		if id, err := cfg.Identity(); err == nil {
			opts.Identity = id
		}
	}
	nc, err := natsconn.Connect(ctx, cfg, name, nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
	if err != nil {
		return nil, err
	}
	r := New(nc, opts)
	r.owns = true
	return r, nil
}

// ErrDisabled is returned by Connect when no NATS URL is configured — the
// standalone commands map it to a usage/config exit code (exit 2).
var ErrDisabled = errors.New("reporting: no NATS URL configured")

// New wraps an existing connection (the runner's shared conn). The returned
// Reporter does not own nc and Close is a no-op on it.
func New(nc *nats.Conn, opts Options) *Reporter {
	prefix := opts.EvtPrefix
	if prefix == "" {
		prefix = eventing.DefaultPrefix
	}
	userTok := opts.SubjectUser
	if userTok == "" {
		userTok = opts.Identity.SubjectToken
	}
	if userTok == "" {
		userTok = "anonymous"
	}
	return &Reporter{
		nc:      nc,
		prefix:  prefix,
		userTok: tok(userTok),
		userBlk: eventing.User{Name: opts.Identity.Name, PublicKey: opts.Identity.Subject},
		project: eventing.ProjectSlug(opts.Project),
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// Close drains and closes the connection when the Reporter owns it.
func (r *Reporter) Close() {
	if r != nil && r.owns && r.nc != nil {
		_ = r.nc.Drain()
	}
}

// Event publishes a caller-named session event on
// ape.evt.<user>.<project>.session.<sid>.<event>. payload is the caller's
// arbitrary JSON (nil when none).
func (r *Reporter) Event(sessionID, event string, payload json.RawMessage) error {
	subject := strings.Join([]string{r.prefix, r.userTok, r.project, "session", tok(sessionID), tok(event)}, ".")
	extra := map[string]any{"event": event}
	if len(payload) > 0 {
		extra["payload"] = payload
	}
	return r.publish(subject, r.envelope(sessionID, extra))
}

// Log publishes a structured log record on
// ape.log.<user>.<project>.<sid>.<level>.
func (r *Reporter) Log(sessionID, level, msg string, fields map[string]string) error {
	subject := strings.Join([]string{rootLog, r.userTok, r.project, tok(sessionID), tok(level)}, ".")
	if fields == nil {
		fields = map[string]string{}
	}
	return r.publish(subject, r.envelope(sessionID, map[string]any{
		"level":  level,
		"msg":    msg,
		"fields": fields,
	}))
}

// envelope builds the versioned common payload; extra fields merge on top.
func (r *Reporter) envelope(sessionID string, extra map[string]any) map[string]any {
	m := map[string]any{
		"v":          eventing.SchemaVersion,
		"ts":         r.now().Format(time.RFC3339Nano),
		"user":       r.userBlk,
		"project":    r.project,
		"session_id": sessionID,
	}
	maps.Copy(m, extra)
	return m
}

// publish marshals m and publishes it synchronously. A Flush round-trip
// forces the server's response, so a publish-permission rejection has set
// nc.LastError() by the time Flush returns (processTransientError runs on
// the read loop before Flush's PONG) — detected here and returned so the
// standalone caller can exit non-zero.
func (r *Reporter) publish(subject string, m map[string]any) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("reporting: marshal payload: %w", err)
	}
	if err := r.nc.Publish(subject, data); err != nil {
		return fmt.Errorf("reporting: publish %s: %w", subject, err)
	}
	if err := r.nc.Flush(); err != nil {
		return fmt.Errorf("reporting: flush %s: %w", subject, err)
	}
	if lastErr := r.nc.LastError(); lastErr != nil && errors.Is(lastErr, nats.ErrPermissionViolation) {
		return fmt.Errorf("reporting: publish to %q rejected — the credential is not authorized for this identity: %w", subject, lastErr)
	}
	return nil
}

// tok sanitizes an arbitrary string into a single subject token so a stray
// id/event/level value can never inject extra subject levels or wildcards.
func tok(s string) string {
	slug := natsconn.SubjectToken(s)
	if slug == "" {
		return "unknown"
	}
	return slug
}
