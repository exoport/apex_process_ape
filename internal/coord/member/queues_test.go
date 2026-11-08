package member

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// Stage 1a step 10 (moved from step 8): project check reads the merge
// queues of main and of each milestone branch on the remote; each must
// merge with a merge commit. A queue that cannot be read is a note, not a
// finding.
func TestQueueFindings(t *testing.T) {
	queues := map[string]string{"main": "SQUASH", "milestone/M1": "MERGE"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var q struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &q)
		b, _ := q.Variables["b"].(string)
		if b == "milestone/M3" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		m, ok := queues[b]
		if !ok {
			_, _ = w.Write([]byte(`{"data":{"repository":{"mergeQueue":null}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"mergeQueue":{"configuration":{"mergeMethod":"` + m + `"}}}}}`))
	}))
	t.Cleanup(srv.Close)

	require.Nil(t, QueueFindings(context.Background(), project(t, block, true), io.Discard), "no coordination.github: nothing read")

	p := project(t, queued(block)+"  github:\n    repo: shop/notes\n    api: "+srv.URL+"\n", true)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "github.token"), []byte("t\n"), 0o600))
	git := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", p.Root}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	for _, b := range []string{"milestone/M1", "milestone/M2", "milestone/M3"} {
		git("update-ref", "refs/remotes/origin/"+b, "HEAD")
	}

	var note bytes.Buffer
	fs := QueueFindings(context.Background(), p, &note)
	msgs := make([]string, 0, len(fs))
	for _, f := range apexv1.ClassifyFindings(fs) {
		require.Equal(t, apexv1.CodeQueueSettings, f.Code)
		msgs = append(msgs, f.Message)
	}
	require.Len(t, msgs, 1, "%q", msgs)
	require.Contains(t, msgs[0], "main on shop/notes merges by squash")
	require.Empty(t, note.String(), "D702: milestone branches have no queue, and are not read")
}
