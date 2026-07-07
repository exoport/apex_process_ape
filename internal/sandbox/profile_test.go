package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadValidAPIKeyProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "ci", `
name: ci-profile
credentials: api-key
api_key_source: env:APE_JOB_ANTHROPIC_KEY
skills:
  - apex-create-prd
  - /abs/path/curated/my-skill
ignore_project_settings: true
network:
  authorized_domains:
    - api.anthropic.com
    - "*.githubusercontent.com"
  direct_allow:
    - nats.example.com:4222
git:
  mode: token
  token_source: env:APE_JOB_GITHUB_TOKEN
`)

	p, err := Load(dir, "ci")
	require.NoError(t, err)
	assert.Equal(t, "ci-profile", p.Name)
	assert.Equal(t, CredentialAPIKey, p.Credentials)
	assert.Equal(t, GitToken, p.Git.Mode)
	assert.Equal(t, []string{"apex-create-prd", "/abs/path/curated/my-skill"}, p.Skills)
	assert.Equal(t, ProfilePath(dir, "ci"), p.Path())
}

func TestLoadOAuthProfileDefaultsGitNone(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "oauth", `
name: oauth-profile
credentials: oauth
`)
	p, err := Load(dir, "oauth")
	require.NoError(t, err)
	assert.Equal(t, CredentialOAuth, p.Credentials)
	assert.Equal(t, GitNone, p.Git.Mode, "git.mode should default to none")
}

func TestLoadDefaultsBackendVMMMount(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "min", `
name: min
credentials: oauth
`)
	p, err := Load(dir, "min")
	require.NoError(t, err)
	assert.Equal(t, BackendKata, p.Backend, "backend defaults to kata")
	assert.Equal(t, VMMCloudHypervisor, p.VMM, "vmm defaults to clh")
	assert.Equal(t, MountHostFS, p.Mount, "mount defaults to host-fs")
	assert.Empty(t, p.Image, "image empty → official ape-sandbox at run time")
}

func TestLoadWorkspaceFields(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "gpu", `
name: gpu
backend: kata
vmm: qemu
image: ghcr.io/acme/custom:1
mount: volume
credentials: oauth
`)
	p, err := Load(dir, "gpu")
	require.NoError(t, err)
	assert.Equal(t, VMMQemu, p.VMM)
	assert.Equal(t, MountVolume, p.Mount)
	assert.Equal(t, "ghcr.io/acme/custom:1", p.Image)
	assert.Equal(t, DefaultImage, ResolveImage(&Profile{}), "sanity: empty image resolves to the default")
}

func TestValidateRejectsBadProfiles(t *testing.T) {
	cases := map[string]struct {
		yaml string
		want string
	}{
		"missing credentials": {
			yaml: "name: x\ngit:\n  mode: none\n",
			want: "credentials is required",
		},
		"api-key without source": {
			yaml: "name: x\ncredentials: api-key\n",
			want: "requires api_key_source",
		},
		"oauth with api_key_source": {
			yaml: "name: x\ncredentials: oauth\napi_key_source: env:FOO\n",
			want: "only valid with credentials: api-key",
		},
		"bad secret scheme": {
			yaml: "name: x\ncredentials: api-key\napi_key_source: vault:FOO\n",
			want: "unsupported scheme",
		},
		"token mode without source": {
			yaml: "name: x\ncredentials: oauth\ngit:\n  mode: token\n",
			want: "requires git.token_source",
		},
		"deploy-key without path": {
			yaml: "name: x\ncredentials: oauth\ngit:\n  mode: deploy-key\n",
			want: "requires git.deploy_key",
		},
		"bad git mode": {
			yaml: "name: x\ncredentials: oauth\ngit:\n  mode: sftp\n",
			want: "git.mode must be",
		},
		"direct_allow wildcard": {
			yaml: "name: x\ncredentials: oauth\nnetwork:\n  direct_allow:\n    - \"*.foo.com:22\"\n",
			want: "wildcards not allowed",
		},
		"direct_allow no port": {
			yaml: "name: x\ncredentials: oauth\nnetwork:\n  direct_allow:\n    - foo.com\n",
			want: "expected host:port",
		},
		"domain double wildcard": {
			yaml: "name: x\ncredentials: oauth\nnetwork:\n  authorized_domains:\n    - \"*.*.com\"\n",
			want: "single leading-wildcard",
		},
		"domain mid wildcard": {
			yaml: "name: x\ncredentials: oauth\nnetwork:\n  authorized_domains:\n    - \"api.*.com\"\n",
			want: "leading label",
		},
		"unknown key": {
			yaml: "name: x\ncredentials: oauth\nbogus_key: true\n",
			want: "parse profile",
		},
		"non-kata backend": {
			yaml: "name: x\nbackend: gvisor\ncredentials: oauth\n",
			want: "backend must be kata",
		},
		"bad vmm": {
			yaml: "name: x\nvmm: firecracker\ncredentials: oauth\n",
			want: "vmm must be clh or qemu",
		},
		"bad mount": {
			yaml: "name: x\nmount: nfs\ncredentials: oauth\n",
			want: "mount must be host-fs",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeProfile(t, dir, "p", tc.yaml)
			_, err := Load(dir, "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestLoadRejectsPathyNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../evil", "a/b", `a\b`, "..", ""} {
		_, err := Load(dir, name)
		require.Error(t, err, "name %q should be rejected", name)
	}
}

func TestLoadMissingProfileNamesPath(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), ProfilePath(dir, "nope"))
}

// writeProfile drops a profile file at _apex/sandbox/<name>.yaml under dir.
func writeProfile(t *testing.T, dir, name, body string) {
	t.Helper()
	sub := filepath.Join(dir, filepath.FromSlash(ProfilesDirName))
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, name+".yaml"), []byte(body), 0o600))
}
