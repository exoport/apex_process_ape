// Package natsconn resolves the optional NATS connection shared by the
// eventing (PLAN-13 D2) and transcript-blob (PLAN-13 D3) features and
// decodes the user identity out of the .creds file (PLAN-13 D1 / PLAN-17
// D1) so every published subject can carry a server-enforceable <user>
// token.
//
// Everything here is opt-in and fire-and-forget: with no NATS URL
// configured the connection is a documented no-op and all downstream
// features silently disable. A configured-but-unreachable cluster returns
// an error the caller warns on once and then proceeds local-only — it
// never fails a run. All diagnostics go to stderr, never stdout (the eval
// parses the `ape task --output-format json` envelope from stdout).
package natsconn

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
)

// Environment variables (D1: flags → env only; there is deliberately no
// project-config layer, so a repo can never turn publishing on for
// whoever runs in it, and credential paths never land in committed config).
const (
	EnvURL   = "APE_NATS_URL"
	EnvCreds = "APE_NATS_CREDS" //nolint:gosec // G101 false positive: an env var name, not a credential
)

// Config is the resolved NATS connection configuration. The zero value
// (empty URL) means NATS is disabled.
type Config struct {
	URL       string
	CredsFile string
}

// Enabled reports whether a NATS URL was configured.
func (c Config) Enabled() bool { return c.URL != "" }

// Resolve applies the flag → env resolution order: a non-empty flag wins,
// otherwise the matching env var is used.
func Resolve(urlFlag, credsFlag string) Config {
	url := urlFlag
	if url == "" {
		url = os.Getenv(EnvURL)
	}
	creds := credsFlag
	if creds == "" {
		creds = os.Getenv(EnvCreds)
	}
	return Config{URL: url, CredsFile: creds}
}

// connectTimeout bounds the initial dial so a misconfigured or unreachable
// cluster degrades to local-only quickly instead of hanging a run. Var (not
// const) so tests can shorten it.
var connectTimeout = 3 * time.Second

// Connect opens a connection for the given client name (e.g.
// "ape/0.0.42"). It returns (nil, nil) when cfg is disabled, so callers can
// treat "NATS off" and "no NATS" identically. On a reachable cluster the
// returned conn auto-reconnects with capped backoff; the caller should
// nc.Drain() at shutdown to flush pending publishes.
//
// A configured-but-unreachable cluster returns a non-nil error (dialed with
// RetryOnFailedConnect off so it fails fast); the caller logs one stderr
// warning and proceeds local-only.
func Connect(ctx context.Context, cfg Config, name string) (*nats.Conn, error) {
	if !cfg.Enabled() {
		return nil, nil //nolint:nilnil // disabled is a documented no-op, not an error
	}
	opts := []nats.Option{
		nats.Name(name),
		nats.Timeout(connectTimeout),
		nats.RetryOnFailedConnect(false),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.ReconnectJitter(500*time.Millisecond, time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠ nats: disconnected: %v\n", err)
			}
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}
			fmt.Fprintf(os.Stderr, "⚠ nats: async error (subject %q): %v\n", subject, err)
		}),
	}
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	}

	// Race the (bounded) dial against ctx so a cancelled run doesn't wait
	// out the connect timeout.
	type result struct {
		nc  *nats.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		nc, err := nats.Connect(cfg.URL, opts...)
		ch <- result{nc, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("natsconn: connect %q: %w", cfg.URL, r.err)
		}
		return r.nc, nil
	}
}
