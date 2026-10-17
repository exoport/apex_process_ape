package apexv1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// D45 (H3): every git push is denied unless the session was launched
// with APE_ALLOW_PUSH=1.
func TestPushGuard(t *testing.T) {
	t.Setenv(EnvAllowPush, "")
	dir := t.TempDir()
	for cmd, deny := range map[string]bool{
		"git push":                                true,
		"git push origin":                         true,
		"git push origin main":                    true,
		"git push -u origin feature":              true,
		"git push --force-with-lease origin main": true,
		"git push --dry-run origin main":          true,
		"git -C /tmp/x push origin main":          true,
		"git -c push.followTags=false push":       true,
		"/usr/bin/git push":                       true,
		"make && git push":                        true,
		"go test ./... ; git push origin HEAD":    true,
		"(cd sub && git push)":                    true,
		`sh -c "git push origin main"`:            true,
		`bash -lc 'make; git push'`:               true,
		`eval "git push"`:                         true,
		"sudo -u ci git push":                     true,
		"env GIT_SSH_COMMAND=ssh git push":        true,
		"timeout 30 git push":                     true,
		"APE_ALLOW_PUSH=1 git push":               true,
		"env APE_ALLOW_PUSH=1 git push":           true,
		"export APE_ALLOW_PUSH=1; git push":       true,
		// not a push
		"git pull":                      false,
		"git fetch origin":              false,
		"git commit -m 'push it later'": false,
		`echo "git push"`:               false,
		"grep -r 'git push' docs/":      false,
		"git log --oneline origin/main": false,
		"git config --get push.default": false,
		"command -v git && git status":  false,
		"git stash push -m wip":         false, // git stash push is not git push
		"ls -la":                        false,
	} {
		got := PushGuard(cmd, dir)
		require.Equal(t, deny, got.Deny, "%q: %+v", cmd, got)
		if deny {
			require.Contains(t, got.Reason, "only a person pushes", cmd)
		}
	}
	got := PushGuard("APE_ALLOW_PUSH=1 git push", dir)
	require.Contains(t, got.Reason, "does not count", "an inline override is named as not counting")
	require.NotContains(t, PushGuard("git push", dir).Reason, "does not count")

	t.Setenv(EnvAllowPush, "1")
	require.False(t, PushGuard("git push origin main", dir).Deny)
	t.Setenv(EnvAllowPush, "true")
	require.True(t, PushGuard("git push", dir).Deny, "only 1 opts out")
}

// D46 (H40): pkill, killall (and killall5, skill) are denied in any form
// unless the session was launched with APE_ALLOW_KILL=1; kill <pid> stays
// allowed.
func TestKillGuard(t *testing.T) {
	t.Setenv(EnvAllowKill, "")
	dir := t.TempDir()
	for cmd, deny := range map[string]bool{
		"pkill -f 'go test'":                 true,
		"pkill server":                       true,
		"killall node":                       true,
		"killall5 -9":                        true,
		"skill -KILL -u diego":               true,
		"/usr/bin/pkill -f x":                true,
		"sudo pkill -f x":                    true,
		"sudo -u root killall x":             true,
		"env FOO=1 pkill x":                  true,
		"/usr/bin/env pkill x":               true,
		"env -S 'pkill x'":                   true,
		"nohup pkill x &":                    true,
		"timeout -s KILL 5 pkill x":          true,
		"nice -n 5 killall x":                true,
		"busybox pkill x":                    true,
		"xargs -0 pkill < names":             true,
		"make stop || pkill -f server":       true,
		"sleep 1 && killall x":               true,
		`sh -c "pkill -f x"`:                 true,
		`bash -c 'cd /tmp; killall x'`:       true,
		`eval "pkill x"`:                     true,
		"(pkill x)":                          true,
		"echo $(pkill x)":                    true,
		"APE_ALLOW_KILL=1 pkill x":           true,
		"export APE_ALLOW_KILL=1 && pkill x": true,
		// allowed
		"kill 12345":                     false,
		"kill -9 12345":                  false,
		"kill -TERM $PID":                false,
		"kill %1":                        false,
		"kill $(cat server.pid)":         false,
		"pgrep -f server":                false,
		"ps aux | grep pkill":            false,
		`echo "pkill x"`:                 false,
		"command -v pkill":               false,
		"which killall":                  false,
		"man pkill":                      false,
		"grep -r killall docs/":          false,
		"go test ./internal/killall/...": false,
	} {
		got := KillGuard(cmd, dir)
		require.Equal(t, deny, got.Deny, "%q: %+v", cmd, got)
		if deny {
			require.Contains(t, got.Reason, "stop the process you started by its PID (kill <pid>)", cmd)
		}
	}
	require.Contains(t, KillGuard("APE_ALLOW_KILL=1 pkill x", dir).Reason, "does not count")

	t.Setenv(EnvAllowKill, "1")
	require.False(t, KillGuard("pkill -f x && killall y", dir).Deny)
}

// D47 Q6: the three overrides behave alike — each counts only from the
// hook process's environment, never from an assignment in the command.
func TestOverrides_OnlyFromTheLaunchingEnvironment(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		env, inline string
		guard       func(string, string) PreToolDecision
	}{
		{EnvAllowTag, "git tag v1", TagGuard},
		{EnvAllowPush, "git push", PushGuard},
		{EnvAllowKill, "pkill x", KillGuard},
	} {
		t.Setenv(tc.env, "")
		for _, form := range []string{
			tc.env + "=1 " + tc.inline,
			"env " + tc.env + "=1 " + tc.inline,
			"export " + tc.env + "=1; " + tc.inline,
			"export " + tc.env + "=1 && " + tc.inline,
			`sh -c "` + tc.env + `=1 ` + tc.inline + `"`,
		} {
			d := tc.guard(form, dir)
			require.True(t, d.Deny, "%q", form)
			require.Contains(t, d.Reason, "does not count", form)
		}
		t.Setenv(tc.env, "1")
		require.False(t, tc.guard(tc.inline, dir).Deny, tc.env)
		t.Setenv(tc.env, "")
	}
}

// The hook path: push and kill denials come out as PreToolUse JSON, and
// a tag push names the tag rule first.
func TestPreTool_BashPushKillDeny(t *testing.T) {
	t.Setenv(EnvAllowTag, "")
	t.Setenv(EnvAllowPush, "")
	t.Setenv(EnvAllowKill, "")
	decide := func(command string) PreToolDecision {
		in, err := json.Marshal(map[string]string{"command": command})
		require.NoError(t, err)
		return PreTool(HookPayload{ToolName: "Bash", ToolInput: in, Cwd: t.TempDir()})
	}
	d := decide("git push origin main")
	require.True(t, d.Deny)
	require.Contains(t, d.Reason, "only a person pushes")
	d = decide("git push --tags")
	require.Contains(t, d.Reason, "only a person tags or releases")
	t.Setenv(EnvAllowTag, "1")
	d = decide("git push --tags")
	require.True(t, d.Deny, "a tag push needs both overrides")
	require.Contains(t, d.Reason, "only a person pushes")
	d = decide("pkill -f server")
	require.True(t, d.Deny)
	out, err := d.Output()
	require.NoError(t, err)
	require.Contains(t, string(out), `"permissionDecision":"deny"`)
	require.False(t, decide("kill 4242").Deny)
}
