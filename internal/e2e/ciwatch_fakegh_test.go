//go:build !windows

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/ciwatch"
)

// D876 (d): `ape ci watch`'s watcher against fakegh's merge queue, which
// keeps each queued pull request's merge group branch as GitHub does. A
// merged group: close-merged with main's new tip, which is the group's sha
// last seen (DIM-3's shape), and no ejected. An ejected group: ejected
// with its ref and sha last seen.
func TestCIWatch_AgainstFakegh(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakegh")
	build := exec.Command("go", "build", "-o", bin, "./internal/e2e/fakegh")
	build.Dir = repoRoot(t)
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "build fakegh: %s", out)

	git := func(d string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := c.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	bare, work := filepath.Join(dir, "origin.git"), filepath.Join(dir, "work")
	git(dir, "init", "-q", "--bare", "-b", "main", bare)
	git(dir, "init", "-q", "-b", "main", work)
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644))
	git(work, "add", "-A")
	git(work, "commit", "-q", "-m", "base")
	git(work, "push", "-q", bare, "main")
	for _, b := range []string{"one", "two"} {
		git(work, "switch", "-q", "-c", b, "main")
		require.NoError(t, os.WriteFile(filepath.Join(work, b+".txt"), []byte(b+"\n"), 0o644))
		git(work, "add", "-A")
		git(work, "commit", "-q", "-m", b)
		git(work, "push", "-q", bare, b)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	srv := exec.Command(bin, "-addr", addr, "-bare", bare, "-delay", "1500ms")
	srv.Stderr = os.Stderr
	require.NoError(t, srv.Start())
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })
	api := "http://" + addr
	require.Eventually(t, func() bool {
		resp, err := http.Get(api + "/repos/shop/notes/git/ref/heads/main")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 50*time.Millisecond)

	post := func(path string, body any) map[string]any {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		resp, err := http.Post(api+path, "application/json", bytes.NewReader(data))
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out
	}
	enqueue := func(head, title string) {
		t.Helper()
		pr := post("/repos/shop/notes/pulls", map[string]string{"head": head, "base": "main", "title": title})
		res := post("/graphql", map[string]any{"query": "mutation($id: ID!) { enqueuePullRequest(input: {pullRequestId: $id}) { mergeQueueEntry { state } } }", "variables": map[string]any{"id": pr["node_id"]}})
		require.NotNil(t, res["data"], "%v", res)
	}

	var got []ciwatch.Published
	w := ciwatch.New(ciwatch.Config{
		API: api, Repo: "shop/notes",
		Token:   func(context.Context, bool) (string, error) { return "fakegh-token", nil },
		Publish: func(_ context.Context, p ciwatch.Published) error { got = append(got, p); return nil },
	})
	ctx := context.Background()
	look := func() ciwatch.Report {
		t.Helper()
		r, err := w.Look(ctx)
		require.NoError(t, err)
		return r
	}
	base := look().Main
	require.Equal(t, git(bare, "rev-parse", "main"), base)
	require.Empty(t, got, "the first look is a baseline")

	enqueue("one", "one")
	r := look()
	require.Len(t, r.Groups[1], 1)
	group := r.Groups[1][0]
	require.Equal(t, "refs/heads/gh-readonly-queue/main/pr-1-"+base, group.Ref)
	require.Empty(t, got, "queued: nothing yet")
	require.Eventually(t, func() bool { return git(bare, "rev-parse", "main") != base }, 10*time.Second, 100*time.Millisecond)
	r = look()
	require.Equal(t, group.SHA, r.Main, "main's new tip is the group's sha last seen")
	require.Len(t, got, 1)
	require.Equal(t, "close-merged", got[0].Event.Event)
	require.Equal(t, "main "+group.SHA, got[0].Event.Reason)
	require.Equal(t, []ciwatch.CompareResult{{PR: 1, Ref: group.Ref, SHA: group.SHA, Status: "identical"}}, r.Compares)

	enqueue("two", "two [fakegh:eject]")
	r = look()
	require.Len(t, r.Groups[2], 1)
	ejected := r.Groups[2][0]
	time.Sleep(2 * time.Second)
	r = look()
	require.Len(t, got, 2, "%v", got)
	require.Equal(t, "ejected", got[1].Event.Event)
	require.Equal(t, ejected.Ref+" "+ejected.SHA, got[1].Event.Reason)
	require.Equal(t, "ci-watch-ejected-"+ejected.SHA, got[1].MsgID)
	require.Equal(t, "behind", r.Compares[0].Status, "the group's merge commit sits on main, which is behind it: not in main")
	require.Equal(t, group.SHA, git(bare, "rev-parse", "main"), "nothing merged")
}
