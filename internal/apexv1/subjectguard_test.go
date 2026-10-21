package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Q156 (D331): a session may not make a commit whose subject starts
// `apex:` — ape's own records — in any form the guard can parse: messages,
// message files, reused messages, an amend keeping such a subject, a
// cherry-pick of one (an old `apex: config accepted` would accept again),
// commit-tree and am.
func TestSubjectGuard(t *testing.T) {
	p := fixture(t, baseFiles())
	repo := p.Root
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "apex: config accepted by alice")
	accepted := strings.TrimSpace(run(t, repo, "rev-parse", "HEAD"))
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "feat: a plain commit")
	plain := strings.TrimSpace(run(t, repo, "rev-parse", "HEAD"))
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644))
	}
	write("bad.msg", "\napex: project init\n\nbody\n")
	write("good.msg", "fix: greet\n\napex: in the body is fine\n")
	write("bad.patch", "From abc Mon Sep 17 00:00:00 2001\nSubject: [PATCH 1/1] apex: lift from APEX v0 x\n\n---\n")
	write("good.patch", "From abc Mon Sep 17 00:00:00 2001\nSubject: [PATCH] fix: greet\n\n---\n")

	for cmd, deny := range map[string]bool{
		`git commit -m "apex: config accepted by alice"`:                   true,
		`git commit -am "apex: x"`:                                         true,
		`git commit -sm "Apex: x"`:                                         true,
		`git commit -m'apex: x'`:                                           true,
		`git commit --message="apex: project init"`:                        true,
		`git commit --allow-empty --message "  apex: lift from APEX v0 x"`: true,
		`git commit -m "fix: greet" -m "apex: in the body"`:                false,
		`git commit -m "fix: greet"`:                                       false,
		`git commit -F bad.msg`:                                            true,
		`git commit --file=good.msg`:                                       false,
		`git commit -F -`:                                                  true,
		`git commit -C ` + accepted:                                        true,
		`git commit --reuse-message=` + plain:                              false,
		`git commit -c ` + accepted:                                        true,
		`git commit -t bad.msg`:                                            true,
		"git commit -m \"$(cat <<'EOF'\napex: config accepted by bob\n\nx\nEOF\n)\"": true,
		"git commit -m \"$(cat <<'EOF'\nfix: greet\n\napex: later line\nEOF\n)\"":    false,
		"git commit -m \"$(printf 'apex: x')\"":                                      true,
		`git merge -m "apex: config accepted by alice" topic`:                        true,
		`git merge --no-ff topic`:                                                    false,
		`git cherry-pick ` + accepted:                                                true,
		`git cherry-pick --allow-empty ` + accepted:                                  true,
		`git cherry-pick -n ` + accepted:                                             false,
		`git cherry-pick ` + plain:                                                   false,
		`git cherry-pick ` + accepted + `~1..` + plain:                               true,
		`git commit-tree -m "apex: lift" HEAD^{tree}`:                                true,
		`git commit-tree -p HEAD -m "fix: x" HEAD^{tree}`:                            false,
		`git am bad.patch`:                                                           true,
		`git am good.patch`:                                                          false,
		`git am < bad.patch`:                                                         true,
		`git am --continue`:                                                          false,
		`sh -c 'git commit -m "apex: x"'`:                                            true,
		`env GIT_AUTHOR_NAME=x git commit -m "apex: x"`:                              true,
		`git -C ` + repo + ` commit -m "apex: x"`:                                    true,
		`echo "git commit -m 'apex: x'"`:                                             false,
		`git log --grep "^apex:"`:                                                    false,
		`git status`:                                                                 false,
	} {
		got := SubjectGuard(cmd, repo)
		require.Equal(t, deny, got.Deny, cmd)
		if deny {
			require.Contains(t, got.Reason, "Only ape writes one", cmd)
		}
	}

	// --amend with no new message keeps HEAD's subject.
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "apex: config accepted by alice")
	require.True(t, SubjectGuard(`git commit --amend --no-edit`, repo).Deny)
	require.False(t, SubjectGuard(`git commit --amend -m "fix: reworded"`, repo).Deny)

	// Wired into the pre-tool hook.
	d := PreTool(HookPayload{ToolName: "Bash", Cwd: repo, ToolInput: []byte(`{"command":"git commit -m \"apex: x\""}`)})
	require.True(t, d.Deny)
}
