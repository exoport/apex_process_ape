package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHome builds a host ~/.claude with the given skills (name→SKILL.md
// body), agents (name→body), and a credentials file (mode-A source).
// Returns the home dir.
func fakeHome(t *testing.T, skills, agents map[string]string) string {
	t.Helper()
	home := t.TempDir()
	for name, body := range skills {
		dir := filepath.Join(home, ".claude", "skills", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644))
	}
	for name, body := range agents {
		dir := filepath.Join(home, ".claude", "agents")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644))
	}
	claudeDir := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(`{"oauth":"tok"}`), 0o600))
	return home
}

func TestComposeModeAOAuth(t *testing.T) {
	home := fakeHome(t, map[string]string{"apex-create-prd": "prd", "other-skill": "x"}, nil)
	staging := t.TempDir()
	p := &Profile{
		Name:        "a",
		Credentials: CredentialOAuth,
		Skills:      []string{"apex-create-prd"}, // only this one — not other-skill
		Preferences: map[string]any{"model": "opus"},
	}
	require.NoError(t, p.Validate())

	comp, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: home})
	require.NoError(t, err)

	// Only the hand-picked skill resolves; the host-present-but-unpicked one does not.
	assert.FileExists(t, filepath.Join(staging, ".claude", "skills", "apex-create-prd", "SKILL.md"))
	assert.NoFileExists(t, filepath.Join(staging, ".claude", "skills", "other-skill", "SKILL.md"))

	// Mode A binds the real credentials file rw; no API key env.
	require.Len(t, comp.Binds, 1)
	assert.Equal(t, filepath.Join(home, ".claude", ".credentials.json"), comp.Binds[0].Source)
	assert.False(t, comp.Binds[0].ReadOnly, "credentials bind must be rw for refresh")
	assert.Empty(t, comp.Env)

	// settings.json carries the preferences.
	var settings map[string]any
	readJSON(t, filepath.Join(staging, ".claude", "settings.json"), &settings)
	assert.Equal(t, "opus", settings["model"])

	// No API key material anywhere in the staged fs.
	assert.NoFileExists(t, filepath.Join(staging, ".claude", ".credentials.json"))
}

func TestComposeModeBAPIKey(t *testing.T) {
	t.Setenv("APE_JOB_KEY", "sk-ant-test")
	staging := t.TempDir()
	p := &Profile{
		Name:         "b",
		Credentials:  CredentialAPIKey,
		APIKeySource: "env:APE_JOB_KEY",
	}
	require.NoError(t, p.Validate())

	comp, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: t.TempDir()})
	require.NoError(t, err)

	assert.Contains(t, comp.Env, "ANTHROPIC_API_KEY=sk-ant-test")
	assert.Empty(t, comp.Binds, "mode B binds no credential files")
	// No OAuth material in the guest fs.
	assert.NoFileExists(t, filepath.Join(staging, ".claude", ".credentials.json"))
}

func TestComposeSkillByAbsolutePath(t *testing.T) {
	// A curated skill dir outside the host home, referenced by path.
	curated := t.TempDir()
	skillDir := filepath.Join(curated, "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("hi"), 0o644))

	staging := t.TempDir()
	p := &Profile{Name: "c", Credentials: CredentialOAuth, Skills: []string{skillDir}}
	_, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: fakeHome(t, nil, nil)})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(staging, ".claude", "skills", "my-skill", "SKILL.md"))
}

func TestComposeAgentsByNameAndPath(t *testing.T) {
	home := fakeHome(t, nil, map[string]string{"apex-agent-pm": "pm"})
	staging := t.TempDir()
	p := &Profile{Name: "d", Credentials: CredentialOAuth, Agents: []string{"apex-agent-pm"}}
	_, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: home})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(staging, ".claude", "agents", "apex-agent-pm.md"))
}

func TestComposeEmptyByDefault(t *testing.T) {
	staging := t.TempDir()
	p := &Profile{Name: "e", Credentials: CredentialOAuth}
	_, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: fakeHome(t, nil, nil)})
	require.NoError(t, err)
	// Default: no skills/agents dirs — nothing leaks in by omission.
	assert.NoDirExists(t, filepath.Join(staging, ".claude", "skills"))
	assert.NoDirExists(t, filepath.Join(staging, ".claude", "agents"))
	// .claude.json marks onboarding complete so the guest never prompts.
	var doc map[string]any
	readJSON(t, filepath.Join(staging, ".claude.json"), &doc)
	assert.Equal(t, true, doc["hasCompletedOnboarding"])
}

func TestComposeGitToken(t *testing.T) {
	t.Setenv("APE_GH", "ghp_secret")
	staging := t.TempDir()
	p := &Profile{
		Name:        "g",
		Credentials: CredentialOAuth,
		Git:         GitPolicy{Mode: GitToken, TokenSource: "env:APE_GH"},
	}
	require.NoError(t, p.Validate())
	comp, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: fakeHome(t, nil, nil)})
	require.NoError(t, err)

	cfg, err := os.ReadFile(filepath.Join(staging, ".gitconfig"))
	require.NoError(t, err)
	// The token is served from env by the helper, never written to the file.
	assert.NotContains(t, string(cfg), "ghp_secret")
	assert.Contains(t, string(cfg), "insteadOf")
	assert.Contains(t, comp.Env, "APE_GIT_TOKEN=ghp_secret")
}

func TestComposeGitDeployKey(t *testing.T) {
	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "deploy_key")
	require.NoError(t, os.WriteFile(keyPath, []byte("PRIVATE"), 0o600))

	staging := t.TempDir()
	p := &Profile{
		Name:        "dk",
		Credentials: CredentialOAuth,
		Git:         GitPolicy{Mode: GitDeployKey, DeployKey: keyPath},
	}
	require.NoError(t, p.Validate())
	comp, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: fakeHome(t, nil, nil)})
	require.NoError(t, err)

	kh, err := os.ReadFile(filepath.Join(staging, ".ssh", "known_hosts"))
	require.NoError(t, err)
	assert.Contains(t, string(kh), "github.com")
	assert.FileExists(t, filepath.Join(staging, ".ssh", "config"))

	// The key is bound read-only, never copied into the staging tree.
	keyBind := findBind(t, comp.Binds, keyPath)
	assert.True(t, keyBind.ReadOnly)
	assert.Contains(t, keyBind.Dest, "id_ed25519")
	assert.NoFileExists(t, filepath.Join(staging, ".ssh", "id_ed25519"), "the key is bound, not copied")
}

func TestComposeGitAgent(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-agent.sock")
	staging := t.TempDir()
	p := &Profile{Name: "ag", Credentials: CredentialOAuth, Git: GitPolicy{Mode: GitAgent}}
	require.NoError(t, p.Validate())
	comp, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: fakeHome(t, nil, nil)})
	require.NoError(t, err)

	sockBind := findBind(t, comp.Binds, "/tmp/ssh-agent.sock")
	assert.Contains(t, sockBind.Dest, "agent.sock")
	var found bool
	for _, e := range comp.Env {
		if strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
			found = true
		}
	}
	assert.True(t, found, "SSH_AUTH_SOCK should be rewritten to the in-guest path")
}

// findBind returns the bind whose Source matches, failing the test if
// none does. Lets git-mode tests ignore the mode-A credentials bind that
// also appears when the profile uses OAuth.
func findBind(t *testing.T, binds []BindMount, source string) BindMount {
	t.Helper()
	for _, b := range binds {
		if b.Source == source {
			return b
		}
	}
	t.Fatalf("no bind with source %s in %v", source, binds)
	return BindMount{}
}

func TestComposeAuthorizedKeysLiteralAndPath(t *testing.T) {
	// A .pub file on the host, plus an inline literal key.
	home := fakeHome(t, nil, nil)
	pubPath := filepath.Join(home, ".ssh", "id_ed25519.pub")
	require.NoError(t, os.MkdirAll(filepath.Dir(pubPath), 0o700))
	require.NoError(t, os.WriteFile(pubPath, []byte("ssh-ed25519 AAAAFROMFILE me@host\n"), 0o600))

	staging := t.TempDir()
	p := &Profile{
		Name:        "k",
		Credentials: CredentialOAuth,
		Access: AccessPolicy{AuthorizedKeys: []string{
			"~/.ssh/id_ed25519.pub",          // path (~ expands to host home)
			"ssh-ed25519 AAAALITERAL me@lit", // inline literal
		}},
	}
	require.NoError(t, p.Validate())
	_, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: home})
	require.NoError(t, err)

	akPath := filepath.Join(staging, ".ssh", "authorized_keys")
	data, err := os.ReadFile(akPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ssh-ed25519 AAAAFROMFILE me@host")
	assert.Contains(t, string(data), "ssh-ed25519 AAAALITERAL me@lit")

	// authorized_keys must be 0600 (sshd refuses loose perms). Windows does
	// not honour Unix mode bits, so the perm assertion is Linux/macOS-only;
	// the guest that consumes this file is Linux regardless.
	info, err := os.Stat(akPath)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestComposeAuthorizedKeysEmptyWritesNoFile(t *testing.T) {
	staging := t.TempDir()
	p := &Profile{Name: "k0", Credentials: CredentialOAuth}
	_, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: fakeHome(t, nil, nil)})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(staging, ".ssh", "authorized_keys"))
}

func TestComposeAuthorizedKeysMissingPathFails(t *testing.T) {
	staging := t.TempDir()
	p := &Profile{
		Name:        "kbad",
		Credentials: CredentialOAuth,
		Access:      AccessPolicy{AuthorizedKeys: []string{"~/.ssh/nope.pub"}},
	}
	_, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: fakeHome(t, nil, nil)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authorized_keys")
}

func TestComposeModeAMissingCredentialsFails(t *testing.T) {
	staging := t.TempDir()
	p := &Profile{Name: "a", Credentials: CredentialOAuth}
	_, err := Compose(ComposeOptions{Profile: p, StagingDir: staging, HostHome: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credentials file not found")
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, v))
}
