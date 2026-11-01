package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/ghapp"
)

// PLAN-V15-GHAPP §7: what ape prints for the operator is what the service
// reads — the printed config loads, and the unit carries the hardening the
// plan and research's finding 7 ask for.
func TestGhappPrintConfigAndUnit(t *testing.T) {
	stdout, _, err := v1Exec(t, newGhappCmd, "print-config", "--repo", "exoar/oxid_atomix_engine_throwaway",
		"--setup-app-id", "11", "--setup-installation", "21", "--member-app-id", "12", "--member-installation", "22",
		"--nats-url", "nats://127.0.0.1:14222,nats://127.0.0.1:14223", "--members", "diegos, ci", "--operators", "diegos")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(stdout), 0o644))
	cfg, err := ghapp.LoadConfig(path)
	require.NoError(t, err, stdout)
	require.Equal(t, int64(12), cfg.Member.AppID)
	require.Equal(t, int64(22), cfg.Member.Installations["exoar/oxid_atomix_engine_throwaway"])
	require.Equal(t, []string{"diegos", "ci"}, cfg.Repos["exoar/oxid_atomix_engine_throwaway"].Members)
	require.Equal(t, []string{"diegos"}, cfg.Repos["exoar/oxid_atomix_engine_throwaway"].Operators)
	require.Equal(t, "${CREDENTIALS_DIRECTORY}/member.pem", cfg.Member.Key)
	_, _, err = v1Exec(t, newGhappCmd, "print-config", "--repo", "x/y")
	require.Equal(t, ExitUsage, exitCodeOf(t, err))

	stdout, _, err = v1Exec(t, newGhappCmd, "print-unit", "--ape", "/opt/ape/bin/ape")
	require.NoError(t, err)
	for _, want := range []string{
		"User=apex-ghapp", "ExecStart=/opt/ape/bin/ape ghapp serve", "LoadCredential=member.pem:/etc/apex-ghapp/keys/member.pem",
		"LimitCORE=0", "UMask=0077", "ProtectProc=invisible", "NoNewPrivileges=yes", "CapabilityBoundingSet=\n", "ProtectHome=yes",
	} {
		require.Contains(t, stdout, want)
	}
}
