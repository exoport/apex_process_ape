//go:build linux || darwin

package aped

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestSDNotifySendsDatagram drives notifyTo against a real AF_UNIX DGRAM
// listener (no root, no systemd) — the same wire systemd's NOTIFY_SOCKET is.
func TestSDNotifySendsDatagram(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "notify.sock")
	lc, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	defer func() { _ = lc.Close() }()

	sent, err := notifyTo(sockPath, "READY=1")
	if err != nil {
		t.Fatalf("notifyTo: %v", err)
	}
	if !sent {
		t.Fatal("sent = false, want true")
	}

	buf := make([]byte, 64)
	_ = lc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := lc.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("read datagram: %v", err)
	}
	if got := string(buf[:n]); got != "READY=1" {
		t.Fatalf("datagram = %q, want READY=1", got)
	}
}

// TestSDNotifyNoSocketIsNoop asserts an unconfigured socket is a silent no-op
// (the daemon runs identically outside a Type=notify unit).
func TestSDNotifyNoSocketIsNoop(t *testing.T) {
	sent, err := notifyTo("", "READY=1")
	if sent || err != nil {
		t.Fatalf(`notifyTo("", …) = (%v, %v), want (false, nil)`, sent, err)
	}
}

// TestSDNotifyReadsEnv confirms sdNotify resolves NOTIFY_SOCKET from the
// environment (unset → no-op).
func TestSDNotifyReadsEnv(t *testing.T) {
	t.Setenv(notifySocketEnv, "")
	if sent, err := sdNotify("READY=1"); sent || err != nil {
		t.Fatalf("sdNotify with unset socket = (%v, %v), want (false, nil)", sent, err)
	}

	sockPath := filepath.Join(t.TempDir(), "notify.sock")
	lc, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	defer func() { _ = lc.Close() }()
	t.Setenv(notifySocketEnv, sockPath)
	if sent, err := sdNotify("WATCHDOG=1"); !sent || err != nil {
		t.Fatalf("sdNotify with socket set = (%v, %v), want (true, nil)", sent, err)
	}
	buf := make([]byte, 64)
	_ = lc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := lc.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("read datagram: %v", err)
	}
	if got := string(buf[:n]); got != "WATCHDOG=1" {
		t.Fatalf("datagram = %q, want WATCHDOG=1", got)
	}
}

// TestWatchdogInterval covers the WATCHDOG_USEC/WATCHDOG_PID decision.
func TestWatchdogInterval(t *testing.T) {
	// Unset → disabled.
	t.Setenv(watchdogUsecEnv, "")
	t.Setenv(watchdogPIDEnv, "")
	if d := watchdogInterval(); d != 0 {
		t.Fatalf("unset WATCHDOG_USEC → %v, want 0", d)
	}

	// 30s timeout → 15s ping interval (half the timeout).
	t.Setenv(watchdogUsecEnv, "30000000")
	if d := watchdogInterval(); d != 15*time.Second {
		t.Fatalf("30s timeout → %v, want 15s", d)
	}

	// Armed for a different pid → disabled.
	t.Setenv(watchdogPIDEnv, strconv.Itoa(os.Getpid()+1))
	if d := watchdogInterval(); d != 0 {
		t.Fatalf("foreign WATCHDOG_PID → %v, want 0", d)
	}

	// Armed for us → enabled.
	t.Setenv(watchdogPIDEnv, strconv.Itoa(os.Getpid()))
	if d := watchdogInterval(); d != 15*time.Second {
		t.Fatalf("own WATCHDOG_PID → %v, want 15s", d)
	}

	// Malformed → disabled (fail safe).
	t.Setenv(watchdogUsecEnv, "not-a-number")
	if d := watchdogInterval(); d != 0 {
		t.Fatalf("malformed WATCHDOG_USEC → %v, want 0", d)
	}
}
