package apexv1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Item 10: the pre-tool hook denies a Bash command that creates, deletes
// or pushes a git tag, and nothing else.
func TestTagGuard_Commands(t *testing.T) {
	needSh(t)
	repo := t.TempDir()
	gitEnv(t)
	run(t, repo, "init", "-q", "-b", "main")
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	run(t, repo, "tag", "v1.2.3")
	run(t, repo, "branch", "feature")
	sub := filepath.Join(repo, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	t.Setenv(EnvAllowTag, "")

	for cmd, deny := range map[string]bool{
		// creating and deleting
		"git tag v1":                            true,
		"git tag -a v1 -m 'release 1'":          true,
		"git tag -s v1 -m x":                    true,
		"git tag -f v1 HEAD~1":                  true,
		"git tag -d v1.2.3":                     true,
		"git tag --delete v1.2.3":               true,
		"git -C /tmp/x tag v1":                  true,
		"git -c user.name=x tag v1":             true,
		"git --no-pager tag v2":                 true,
		"/usr/bin/git tag v1":                   true,
		"FOO=1 git tag v1":                      true,
		"env GIT_DIR=.git git tag v1":           true,
		"make build && git tag v1":              true,
		"make build; git tag v1":                true,
		"false || git tag v1":                   true,
		"git log | head -1 && git tag v1":       true,
		"git tag v1 2>&1 | tail -1":             true,
		"(cd sub && git tag v1)":                true,
		`sh -c "git tag x"`:                     true,
		`bash -lc 'git tag -a x -m y'`:          true,
		`eval "git tag x"`:                      true,
		"echo $(git tag x)":                     true,
		"echo `git tag x`":                      true,
		"git tag \\\n  v1":                      true,
		"git tag -m 'a && b' v1":                true,
		"git tag --contains HEAD v1":            false, // --contains lists, whatever follows
		"git tag -l v1 --sort=-version:refname": false,
		// listing and inspecting
		"git tag":                     false,
		"git tag -l":                  false,
		"git tag --list 'v*'":         false,
		"git tag -n5":                 false,
		"git tag --contains abc123":   false,
		"git tag --points-at HEAD":    false,
		"git tag --merged main":       false,
		"git tag --sort=-creatordate": false,
		"git tag -v v1.2.3":           false,
		"git tag 2>/dev/null":         false,
		"git tag | grep v1":           false,
		"git describe --tags":         false,
		"git log --tags --oneline":    false,
		"git fetch --tags":            false,
		// pushing
		"git push --tags":                         true,
		"git push origin --tags":                  true,
		"git push --follow-tags":                  true,
		"git push --mirror backup":                true,
		"git push origin v1.2.3":                  true,
		"git push origin +v1.2.3":                 true,
		"git push origin refs/tags/v9":            true,
		"git push origin HEAD:refs/tags/v9":       true,
		"git push origin :refs/tags/v9":           true,
		"git push origin tag v9":                  true,
		"git push origin --delete v1.2.3":         true,
		"git push -d origin v1.2.3":               true,
		"git push origin :v1.2.3":                 true,
		"git -C sub push origin v1.2.3":           true,
		"git push":                                false,
		"git push origin":                         false,
		"git push origin main":                    false,
		"git push -u origin feature":              false,
		"git push origin --delete feature":        false,
		"git push --force-with-lease origin main": false,
		"git push -o ci.skip origin main":         false,
		// not git at all, or git only in text
		`echo "git tag v1"`:             false,
		`echo 'git push --tags'`:        false,
		`printf '%s\n' "git tag -d v1"`: false,
		"ls -la":                        false,
		"grep -r 'git tag' docs/":       false,
		"git commit -m 'tag v1 later'":  false,
		"# git tag v1":                  false,
	} {
		got := TagGuard(cmd, repo)
		require.Equal(t, deny, got.Deny, "%q: %+v", cmd, got)
		if deny {
			require.Contains(t, got.Reason, "only a person tags or releases", cmd)
		}
	}
}

func TestTagGuard_OptOut(t *testing.T) {
	t.Setenv(EnvAllowTag, "1")
	require.False(t, TagGuard("git tag v1 && git push --tags", t.TempDir()).Deny)
	t.Setenv(EnvAllowTag, "0")
	require.True(t, TagGuard("git tag v1", t.TempDir()).Deny, "only 1 opts out")
}

// The hook path: a Bash payload goes to the tag guard, and the deny is
// PreToolUse JSON.
func TestPreTool_BashTagDeny(t *testing.T) {
	t.Setenv(EnvAllowTag, "")
	in, err := json.Marshal(map[string]string{"command": "git tag -a v1 -m release"})
	require.NoError(t, err)
	d := PreTool(HookPayload{ToolName: "Bash", ToolInput: in, Cwd: t.TempDir()})
	require.True(t, d.Deny)
	out, err := d.Output()
	require.NoError(t, err)
	require.Contains(t, string(out), `"permissionDecision":"deny"`)

	in, err = json.Marshal(map[string]string{"command": "go test ./..."})
	require.NoError(t, err)
	require.False(t, PreTool(HookPayload{ToolName: "Bash", ToolInput: in}).Deny)
}
