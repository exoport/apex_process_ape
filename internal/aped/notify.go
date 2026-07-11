package aped

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// sd_notify(3) support (PLAN-18 D4). Both aped processes run under Type=notify
// units: they signal READY=1 once serving and ping WATCHDOG=1 on a timer when
// WatchdogSec is set. Everything here is a silent no-op when the relevant env
// var is unset, so the daemon runs identically standalone or under Type=exec —
// a notify failure must never fail the daemon. RestrictAddressFamilies=AF_UNIX
// already permits the notify socket.
const (
	notifySocketEnv = "NOTIFY_SOCKET" // systemd sd_notify datagram socket
	watchdogUsecEnv = "WATCHDOG_USEC" // systemd watchdog timeout (µs)
	watchdogPIDEnv  = "WATCHDOG_PID"  // pid the watchdog is armed for
)

// signalReady tells the service manager the daemon is up (READY=1) and starts
// the watchdog pinger if the unit set WatchdogSec. Both are no-ops outside a
// Type=notify unit. Call it once the daemon is actually serving.
func signalReady(ctx context.Context) {
	_, _ = sdNotify("READY=1")
	startWatchdog(ctx)
}

// signalStopping tells the service manager the daemon is shutting down
// intentionally (STOPPING=1), so a clean drain is not read as a crash. No-op
// outside a Type=notify unit.
func signalStopping() { _, _ = sdNotify("STOPPING=1") }

// sdNotify sends one sd_notify(3) datagram (e.g. "READY=1", "WATCHDOG=1") to
// $NOTIFY_SOCKET. It reports whether a socket was configured (false + nil err =
// not under a notify unit), so callers can treat notify as best-effort.
func sdNotify(state string) (bool, error) {
	return notifyTo(os.Getenv(notifySocketEnv), state)
}

// notifyTo is sdNotify with an explicit socket address — the seam the unit tests
// drive against a real AF_UNIX DGRAM listener (no root, no systemd). A leading
// '@' selects the Linux abstract namespace.
func notifyTo(socket, state string) (bool, error) {
	if socket == "" {
		return false, nil // not under a Type=notify unit
	}
	addr := socket
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:] // abstract namespace
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return false, fmt.Errorf("aped: dial notify socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return false, fmt.Errorf("aped: write notify %q: %w", state, err)
	}
	return true, nil
}

// startWatchdog spawns a goroutine pinging WATCHDOG=1 every watchdogInterval()
// until ctx is done. No-op when the watchdog is not armed for this process.
func startWatchdog(ctx context.Context) {
	interval := watchdogInterval()
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = sdNotify("WATCHDOG=1")
			}
		}
	}()
}

// watchdogInterval returns how often to ping WATCHDOG=1 — half the systemd
// WatchdogSec (WATCHDOG_USEC), the sd_watchdog_enabled(3) recommendation — or 0
// when the watchdog is not armed. It honors WATCHDOG_PID so a forked child does
// not ping on the parent's behalf.
func watchdogInterval() time.Duration {
	usec := os.Getenv(watchdogUsecEnv)
	if usec == "" {
		return 0
	}
	if pid := os.Getenv(watchdogPIDEnv); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0 // armed for a different process
	}
	n, err := strconv.ParseInt(usec, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Microsecond / 2
}
