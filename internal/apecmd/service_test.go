package apecmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/exoport/apex_process_ape/internal/service"
)

func TestServiceExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"clean shutdown", nil, ExitOK},
		{"no url", service.ErrNoURL, ExitUsage},
		{"config error", service.ErrConfig, ExitUsage},
		{"wrapped config error", fmt.Errorf("startup: %w", service.ErrConfig), ExitUsage},
		{"runtime failure", errors.New("register micro service: boom"), ExitRunFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := serviceExitCode(tc.err); got != tc.want {
				t.Fatalf("serviceExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestServiceCmdConstructs guards the flag wiring + registration.
func TestServiceCmdConstructs(t *testing.T) {
	cmd := newServiceCmd()
	if cmd.Use != "service" {
		t.Fatalf("Use = %q, want service", cmd.Use)
	}
	for _, flag := range []string{"name", "config", "cwd", "nats-url", "nats-creds", "events-subject-prefix", "drain-timeout"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing --%s flag", flag)
		}
	}
	if cmd.Flags().Lookup("name").DefValue != "ape" {
		t.Errorf("--name default = %q, want ape", cmd.Flags().Lookup("name").DefValue)
	}
}
