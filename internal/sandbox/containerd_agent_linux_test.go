//go:build linux

package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The gate: BOTH per-VM variables, or no agent. Either alone is a
// misconfiguration the agent cannot recover from, and launching it to fail
// immediately would turn a deliberate no-agent workspace into a restart loop.
func TestHasAgentCredsNeedsBoth(t *testing.T) {
	for name, tc := range map[string]struct {
		env  []string
		want bool
	}{
		"both set": {
			env:  []string{"HOME=/sandbox/home", "APE_NATS_URL=nats://aped.internal:4222", "APE_NATS_CREDS=/sandbox/home/.config/ape/vm.creds"},
			want: true,
		},
		"url only":   {env: []string{"APE_NATS_URL=nats://aped.internal:4222"}},
		"creds only": {env: []string{"APE_NATS_CREDS=/sandbox/home/.config/ape/vm.creds"}},
		"neither":    {env: []string{"HOME=/sandbox/home"}},
		"empty url": {
			env: []string{"APE_NATS_URL=", "APE_NATS_CREDS=/sandbox/home/.config/ape/vm.creds"},
		},
		"empty creds": {
			env: []string{"APE_NATS_URL=nats://aped.internal:4222", "APE_NATS_CREDS="},
		},
		"nil env": {},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hasAgentCreds(tc.env); got != tc.want {
				t.Errorf("hasAgentCreds(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// The agent is the `ape` THIS daemon delivered, at the path PLAN-23 mounts it —
// not one from the image, and not a name resolved through PATH.
func TestAgentArgvUsesTheDeliveredApe(t *testing.T) {
	argv := agentArgv()
	if len(argv) != 2 {
		t.Fatalf("agentArgv() = %v", argv)
	}
	if !strings.HasPrefix(argv[0], ApeBinDest+"/") {
		t.Errorf("argv[0] = %q, want the delivered binary under %s", argv[0], ApeBinDest)
	}
	if argv[1] != "sandbox-agent" {
		t.Errorf("argv[1] = %q, want the agent subcommand", argv[1])
	}
}

func TestAgentSupervisorStopIsIdempotent(t *testing.T) {
	s := newAgentSupervisor(&strings.Builder{})
	stopped := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	s.running["dev"] = func() { cancel(); close(stopped) }

	s.stop("dev")
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel the supervision context")
	}
	if ctx.Err() == nil {
		t.Error("the supervision context was not cancelled")
	}
	// A Destroy after a Stop is the normal path, so a second stop must be a no-op
	// rather than a double-close panic.
	s.stop("dev")
	s.stop("never-existed")
}

func TestAgentSupervisorStopAll(t *testing.T) {
	s := newAgentSupervisor(nil) // nil writer must not panic
	n := 0
	for _, id := range []string{"a", "b", "c"} {
		s.running[id] = func() { n++ }
	}
	s.stopAll()
	if n != 3 {
		t.Errorf("cancelled %d supervisors, want 3", n)
	}
	if len(s.running) != 0 {
		t.Errorf("stopAll left %d entries behind", len(s.running))
	}
}

// A workspace with no credentials is a supported configuration, so supervision
// must END rather than retry forever. Retrying would churn the guest and the log
// for a workspace that is never going to run an agent.
func TestNoCredsIsTerminalForSupervision(t *testing.T) {
	if !isAgentTerminal(errNoAgentCreds) {
		t.Error("a workspace with no per-VM credentials should end supervision, not retry")
	}
	if isAgentTerminal(context.DeadlineExceeded) {
		t.Error("a transient error should be retried, not treated as terminal")
	}
	if isAgentTerminal(nil) {
		t.Error("nil is not a terminal error")
	}
}
