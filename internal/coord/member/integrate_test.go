package member

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/github"
)

// Stage 1a step 6: a member project's Integrator is ape's GitHub client on
// coordination.github's repository and API, with hot/github.token as its
// bearer token, mapped onto archive's PRRef and PRState.
func TestIntegrator_TheConfiguredRepository(t *testing.T) {
	var mu sync.Mutex
	var seen, owners []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/shop/notes/pulls":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/shop/notes/pulls":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":3,"node_id":"PR_3","html_url":"https://github.example/shop/notes/pull/3"}`))
		case r.URL.Path == "/graphql" && strings.Contains(string(body), "enqueuePullRequest"):
			_, _ = w.Write([]byte(`{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"state":"QUEUED"}}}}`))
		case r.URL.Path == "/graphql":
			var q struct {
				Variables map[string]any `json:"variables"`
			}
			_ = json.Unmarshal(body, &q)
			mu.Lock()
			owners = append(owners, fmt.Sprint(q.Variables["owner"]))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"state":"MERGED","merged":true,"mergeCommit":{"oid":"abc"},"mergeQueueEntry":null}}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	p := project(t, block+"  github:\n    repo: shop/notes\n    api: "+srv.URL+"\n", true)
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, "hot", "github.token"), []byte("github_pat_t\n"), 0o600))
	in, err := Integrator(p)
	require.NoError(t, err)
	ctx := context.Background()
	pr, err := in.OpenPR(ctx, "apex/add-wave", "milestone/M1", "feat: wave", "Change: add-wave")
	require.NoError(t, err)
	require.Equal(t, apexv1.PRRef{Number: 3, NodeID: "PR_3", URL: "https://github.example/shop/notes/pull/3"}, pr)
	q, err := in.Enqueue(ctx, pr)
	require.NoError(t, err)
	require.Equal(t, "QUEUED", q)
	s, err := in.PRState(ctx, 3)
	require.NoError(t, err)
	require.Equal(t, apexv1.PRState{Merged: true, MergeCommit: "abc"}, s)
	require.Equal(t, []string{"shop"}, owners)
	for _, line := range seen {
		require.True(t, strings.HasSuffix(line, " Bearer github_pat_t"), line)
	}

	_, err = Integrator(project(t, block+"  github:\n    repo: shop/notes\n", true))
	var fe *apexv1.FailError
	require.ErrorAs(t, err, &fe, "no token")
	require.Equal(t, apexv1.CodeCoordMembership, fe.Code)
}

// The mirror's PRs #1 and #2: GitHub refuses an enqueue until its
// mergeability check completes; Enqueue waits it out, and refuses any
// other error at once.
func TestIntegrator_EnqueueWaitsForMergeability(t *testing.T) {
	old, oldEvery := enqueueRetries, enqueueRetryEvery
	enqueueRetries, enqueueRetryEvery = 5, 10*time.Millisecond
	t.Cleanup(func() { enqueueRetries, enqueueRetryEvery = old, oldEvery })
	var mu sync.Mutex
	calls, notYet := 0, 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= notYet {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Pull request mergeability check has not yet completed"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"state":"QUEUED"}}}}`))
	}))
	t.Cleanup(srv.Close)
	g := ghIntegrator{github.New(srv.URL, "shop/notes", "t")}
	st, err := g.Enqueue(context.Background(), apexv1.PRRef{Number: 2, NodeID: "PR_2"})
	require.NoError(t, err)
	require.Equal(t, "QUEUED", st)
	require.Equal(t, 3, calls, "two refusals waited out")

	mu.Lock()
	calls, notYet = 0, 100
	mu.Unlock()
	_, err = g.Enqueue(context.Background(), apexv1.PRRef{Number: 2, NodeID: "PR_2"})
	require.ErrorContains(t, err, "has not yet completed", "bounded")
	require.Equal(t, 6, calls)
}
