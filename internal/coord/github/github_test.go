package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fake is the slice of GitHub's API the client calls: pulls (list, create)
// and GraphQL (enqueue, PR state, queue settings).
type fake struct {
	mu     sync.Mutex
	prs    []map[string]any
	queued map[string]string
	merged map[float64]string
	queues map[string]string
	// rules are the merge_queue rules of the rulesets applying to a
	// branch (REST rules/branches), branch → merge method; gqlRefuses is
	// GitHub's GraphQL refusing an App token the queue's configuration,
	// as it does on the mirror (FINDING-V15-1B-PRE-2).
	rules      map[string]string
	gqlRefuses bool
	limited    bool
	auth       []string
}

func newFake() *fake {
	return &fake{queued: map[string]string{}, merged: map[float64]string{}, queues: map[string]string{"main": "MERGE", "milestone/M1": "SQUASH"}, rules: map[string]string{}}
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.limited {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1893456000")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/shop/notes/pulls":
		head := strings.TrimPrefix(r.URL.Query().Get("head"), "shop:")
		out := []map[string]any{}
		for _, p := range f.prs {
			if p["head"] == head && p["base"] == r.URL.Query().Get("base") {
				out = append(out, p)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/shop/notes/pulls":
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if in["head"] == "missing" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed"}`))
			return
		}
		n := len(f.prs) + 1
		p := map[string]any{"number": n, "node_id": "PR_" + in["head"].(string), "html_url": "https://github.example/pull/" + string(rune('0'+n)), "head": in["head"], "base": in["base"]}
		f.prs = append(f.prs, p)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(p)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/shop/notes/rules/branches/"):
		rules := []map[string]any{{"type": "pull_request", "ruleset_id": 9}}
		if m, ok := f.rules[strings.TrimPrefix(r.URL.Path, "/repos/shop/notes/rules/branches/")]; ok {
			rules = append(rules, map[string]any{"type": "merge_queue", "ruleset_id": 9, "parameters": map[string]any{"merge_method": m, "grouping_strategy": "ALLGREEN"}})
		}
		_ = json.NewEncoder(w).Encode(rules)
	case r.Method == http.MethodPost && r.URL.Path == "/graphql":
		var q struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &q)
		switch {
		case strings.Contains(q.Query, "dequeuePullRequest"):
			delete(f.queued, q.Variables["id"].(string))
			_, _ = w.Write([]byte(`{"data":{"dequeuePullRequest":{"clientMutationId":null}}}`))
		case strings.Contains(q.Query, "enqueuePullRequest"):
			id := q.Variables["id"].(string)
			if id == "PR_nope" {
				_, _ = w.Write([]byte(`{"errors":[{"message":"Pull request is not mergeable"}]}`))
				return
			}
			f.queued[id] = "QUEUED"
			_, _ = w.Write([]byte(`{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"state":"QUEUED"}}}}`))
		case strings.Contains(q.Query, "pullRequest(number"):
			n := q.Variables["n"].(float64)
			if sha, ok := f.merged[n]; ok {
				_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"state":"MERGED","merged":true,"mergeCommit":{"oid":"` + sha + `"},"mergeQueueEntry":null}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"state":"OPEN","merged":false,"mergeCommit":null,"mergeQueueEntry":{"state":"AWAITING_CHECKS"}}}}}`))
		case strings.Contains(q.Query, "mergeQueue(branch") && f.gqlRefuses:
			_, _ = w.Write([]byte(`{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`))
		case strings.Contains(q.Query, "mergeQueue(branch"):
			m, ok := f.queues[q.Variables["b"].(string)]
			if !ok {
				_, _ = w.Write([]byte(`{"data":{"repository":{"mergeQueue":null}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"repository":{"mergeQueue":{"configuration":{"mergeMethod":"` + m + `"}}}}}`))
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func client(t *testing.T) (*Client, *fake) {
	t.Helper()
	f := newFake()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(srv.URL, "shop/notes", "github_pat_x"), f
}

func TestClient_OpenEnqueueState(t *testing.T) {
	c, f := client(t)
	ctx := context.Background()
	pr, err := c.OpenPR(ctx, "apex/add-export", "milestone/M1", "add-export", "Change: add-export")
	require.NoError(t, err)
	require.Equal(t, 1, pr.Number)
	again, err := c.OpenPR(ctx, "apex/add-export", "milestone/M1", "add-export", "")
	require.NoError(t, err)
	require.Equal(t, 1, again.Number, "an open PR for the head is reused, never a second")
	require.Len(t, f.prs, 1)

	st, err := c.Enqueue(ctx, pr)
	require.NoError(t, err)
	require.Equal(t, QueueQueued, st)
	s, err := c.PRState(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, State{Queue: QueueAwaiting}, s)
	f.merged[1] = "abc123"
	s, err = c.PRState(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, State{Merged: true, MergeCommit: "abc123"}, s)

	_, err = c.Enqueue(ctx, &PR{Number: 9, NodeID: "PR_nope"})
	var ae *APIError
	require.ErrorAs(t, err, &ae)
	require.Contains(t, ae.Message, "not mergeable")
	_, err = c.OpenPR(ctx, "missing", "main", "x", "")
	require.ErrorAs(t, err, &ae)
	require.Equal(t, http.StatusUnprocessableEntity, ae.Status)

	for _, a := range f.auth {
		require.Equal(t, "Bearer github_pat_x", a, "the token goes only as the bearer header")
	}
}

func TestClient_QueueSettingsAndRateLimit(t *testing.T) {
	c, f := client(t)
	ctx := context.Background()
	m, err := c.QueueMergeMethod(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, "MERGE", m)
	_, err = c.QueueMergeMethod(ctx, "milestone/M9")
	require.ErrorIs(t, err, ErrNoQueue)

	f.limited = true
	_, err = c.PRState(ctx, 1)
	var rl *ErrRateLimited
	require.True(t, errors.As(err, &rl), "%v", err)
	require.Equal(t, int64(1893456000), rl.Reset.Unix())
}

// FINDING-V15-1B-PRE-2, measured on the mirror with the App's read token:
// REST rules/branches/main answers (X-Accepted-GitHub-Permissions:
// metadata=read) with the merge_queue rule's merge_method, while GraphQL
// refuses mergeQueue.configuration ("Resource not accessible by
// integration"). The rulesets are read first; GraphQL is the fallback for
// a queue no ruleset carries. The control: no rule and GraphQL refusing —
// the refusal comes back, not a guess.
func TestClient_QueueMergeMethodFromRulesets(t *testing.T) {
	c, f := client(t)
	ctx := context.Background()
	f.gqlRefuses = true
	f.rules["main"] = "MERGE"
	m, err := c.QueueMergeMethod(ctx, "main")
	require.NoError(t, err, "the rulesets answer though GraphQL refuses")
	require.Equal(t, "MERGE", m)
	f.rules["main"] = "squash"
	m, err = c.QueueMergeMethod(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, "SQUASH", m, "GraphQL's spelling, whatever the case")

	delete(f.rules, "main")
	_, err = c.QueueMergeMethod(ctx, "main")
	require.ErrorContains(t, err, "Resource not accessible by integration", "control: no rule, GraphQL refuses")
	require.ErrorContains(t, err, "no merge_queue rule")
	require.NotErrorIs(t, err, ErrNoQueue)

	f.gqlRefuses = false
	m, err = c.QueueMergeMethod(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, "MERGE", m, "no rule: GraphQL's configuration, as before")
}

func TestClient_GraphQLURL(t *testing.T) {
	require.Equal(t, "https://api.github.com/graphql", New("https://api.github.com/", "a/b", "t").graphqlURL())
	require.Equal(t, "https://ghe.example/api/graphql", New("https://ghe.example/api/v3", "a/b", "t").graphqlURL())
}

// A person's hold and look (D708+): Dequeue takes a PR out of the queue;
// LatestFor reads the newest PR for a head.
func TestClient_DequeueAndLatest(t *testing.T) {
	c, f := client(t)
	ctx := context.Background()
	pr, err := c.OpenPR(ctx, "apex/accept", "main", "accept", "")
	require.NoError(t, err)
	_, err = c.Enqueue(ctx, pr)
	require.NoError(t, err)
	require.Contains(t, f.queued, pr.NodeID)
	require.NoError(t, c.Dequeue(ctx, pr))
	require.NotContains(t, f.queued, pr.NodeID)
	n, u, _, merged, err := c.LatestFor(ctx, "apex/accept", "main")
	require.NoError(t, err)
	require.Equal(t, pr.Number, n)
	require.Equal(t, pr.URL, u)
	require.Empty(t, merged, "not merged")
	n, _, _, _, err = c.LatestFor(ctx, "apex/none", "main")
	require.NoError(t, err)
	require.Zero(t, n)
}
