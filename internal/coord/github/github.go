// Package github is ape's own GitHub client for coordinated integration
// (v1.5 stage 1a, proposal section 5): open a pull request, enqueue it in
// the branch's merge queue, read its state and the queue's, and read the
// queue settings `ape project check` judges. net/http only: no gh CLI is
// assumed on a member's host. The token is the coordinated repository's
// fine-grained token (hot/github.token), sent only to the configured API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	url2 "net/url"
	"strconv"
	"strings"
	"time"
)

// Client calls one repository's API.
type Client struct {
	// API is the REST base (https://api.github.com, or a GitHub
	// Enterprise …/api/v3); Repo is <owner>/<name>.
	API, Repo, Token string
	HTTP             *http.Client
}

// New is a client with a bounded HTTP timeout.
func New(api, repo, token string) *Client {
	return &Client{API: strings.TrimRight(api, "/"), Repo: repo, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// ErrRateLimited: the API refused for its rate limit; Reset says when it
// lifts (zero when it did not say).
type ErrRateLimited struct{ Reset time.Time }

func (e *ErrRateLimited) Error() string {
	if e.Reset.IsZero() {
		return "github: rate limited"
	}
	return "github: rate limited until " + e.Reset.UTC().Format(time.RFC3339)
}

// APIError is any other refusal: its HTTP status and GitHub's message.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return "github: HTTP " + strconv.Itoa(e.Status) + ": " + e.Message
}

// graphqlURL is the GraphQL endpoint beside API: /graphql on github.com,
// /api/graphql beside an Enterprise /api/v3.
func (c *Client) graphqlURL() string {
	if base, ok := strings.CutSuffix(c.API, "/api/v3"); ok {
		return base + "/api/graphql"
	}
	return c.API + "/graphql"
}

func (c *Client) do(ctx context.Context, method, u string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		e := &ErrRateLimited{}
		if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			e.Reset = time.Unix(s, 0)
		} else if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			e.Reset = time.Now().Add(time.Duration(s) * time.Second)
		}
		return e
	}
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &m)
		if m.Message == "" {
			m.Message = strings.TrimSpace(string(data))
		}
		return &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// graphql runs one query or mutation; a GraphQL error is an APIError.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.do(ctx, http.MethodPost, c.graphqlURL(), map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return &APIError{Status: http.StatusOK, Message: strings.Join(msgs, "; ")}
	}
	return json.Unmarshal(resp.Data, out)
}

func (c *Client) owner() (owner, name string) {
	owner, name, _ = strings.Cut(c.Repo, "/")
	return owner, name
}

// PR is a pull request as ape tracks it.
type PR struct {
	Number int    `json:"number"`
	NodeID string `json:"node_id"`
	URL    string `json:"html_url"`
	Head   string `json:"-"`
	Base   string `json:"-"`
}

// OpenPR opens a pull request from head into base, or returns the open one
// that already exists for head (an archive re-run after a crash opens no
// second PR).
func (c *Client) OpenPR(ctx context.Context, head, base, title, body string) (*PR, error) {
	if pr, err := c.findOpen(ctx, head, base); err != nil || pr != nil {
		return pr, err
	}
	var pr PR
	err := c.do(ctx, http.MethodPost, c.API+"/repos/"+c.Repo+"/pulls", map[string]any{
		"head": head, "base": base, "title": title, "body": body, "maintainer_can_modify": false,
	}, &pr)
	if err != nil {
		return nil, err
	}
	pr.Head, pr.Base = head, base
	return &pr, nil
}

func (c *Client) findOpen(ctx context.Context, head, base string) (*PR, error) {
	owner, _ := c.owner()
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}, "base": {base}}
	var prs []PR
	if err := c.do(ctx, http.MethodGet, c.API+"/repos/"+c.Repo+"/pulls?"+q.Encode(), nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil //nolint:nilnil // none open: the caller opens one
	}
	prs[0].Head, prs[0].Base = head, base
	return &prs[0], nil
}

// Queue entry states (GraphQL MergeQueueEntryState).
const (
	QueueQueued    = "QUEUED"
	QueueAwaiting  = "AWAITING_CHECKS"
	QueueMergeable = "MERGEABLE"
	QueueUnmerge   = "UNMERGEABLE"
	QueueLocked    = "LOCKED"
)

// Enqueue puts the pull request in its base branch's merge queue and
// returns the entry's state. Enqueuing one already queued is no error.
func (c *Client) Enqueue(ctx context.Context, pr *PR) (string, error) {
	var out struct {
		EnqueuePullRequest struct {
			MergeQueueEntry struct {
				State string `json:"state"`
			} `json:"mergeQueueEntry"`
		} `json:"enqueuePullRequest"`
	}
	err := c.graphql(ctx, `mutation($id: ID!) { enqueuePullRequest(input: {pullRequestId: $id}) { mergeQueueEntry { state } } }`,
		map[string]any{"id": pr.NodeID}, &out)
	if err != nil {
		return "", fmt.Errorf("enqueue #%d: %w", pr.Number, err)
	}
	return out.EnqueuePullRequest.MergeQueueEntry.State, nil
}

// Dequeue takes the pull request out of its base branch's merge queue
// (a person's hold: `ape ghapp land --dequeue`).
func (c *Client) Dequeue(ctx context.Context, pr *PR) error {
	var out map[string]any
	err := c.graphql(ctx, `mutation($id: ID!) { dequeuePullRequest(input: {id: $id}) { clientMutationId } }`,
		map[string]any{"id": pr.NodeID}, &out)
	if err != nil {
		return fmt.Errorf("dequeue #%d: %w", pr.Number, err)
	}
	return nil
}

// LatestFor is the newest pull request from head into base in any state:
// its number, URL, state (open|closed), and the merge commit when merged.
func (c *Client) LatestFor(ctx context.Context, head, base string) (number int, url, state, merged string, err error) {
	owner, _ := c.owner()
	q := url2.Values{"state": {"all"}, "head": {owner + ":" + head}, "base": {base}, "sort": {"created"}, "direction": {"desc"}}
	var prs []struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
		State  string `json:"state"`
		Merge  string `json:"merge_commit_sha"`
		Merged string `json:"merged_at"`
	}
	if err = c.do(ctx, http.MethodGet, c.API+"/repos/"+c.Repo+"/pulls?"+q.Encode(), nil, &prs); err != nil || len(prs) == 0 {
		return 0, "", "", "", err
	}
	pr := prs[0]
	if pr.Merged != "" {
		merged = pr.Merge
	}
	return pr.Number, pr.URL, pr.State, merged, nil
}

// RulesFor is the rule types that apply to branch, as GitHub evaluates
// them by name (the branch need not exist), with each merge_queue rule's
// merge method.
func (c *Client) RulesFor(ctx context.Context, branch string) ([]string, error) {
	var rules []struct {
		Type       string         `json:"type"`
		Parameters map[string]any `json:"parameters"`
	}
	if err := c.do(ctx, http.MethodGet, c.API+"/repos/"+c.Repo+"/rules/branches/"+url2.PathEscape(branch), nil, &rules); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		t := r.Type
		if m, ok := r.Parameters["merge_method"].(string); ok {
			t += "(" + m + ")"
		}
		out = append(out, t)
	}
	return out, nil
}

// FindOpen is the open pull request from head into base, nil for none.
func (c *Client) FindOpen(ctx context.Context, head, base string) (*PR, error) {
	return c.findOpen(ctx, head, base)
}

// State is a pull request's state as the wait reads it.
type State struct {
	// Merged: the PR merged. Closed: closed without merging.
	Merged, Closed bool
	// Queue is the merge queue entry's state, "" when not in a queue
	// (never enqueued, merged, or ejected).
	Queue string
	// MergeCommit is the merge's sha once merged.
	MergeCommit string
}

// PRState reads a pull request's state and its queue entry.
func (c *Client) PRState(ctx context.Context, number int) (State, error) {
	owner, name := c.owner()
	var out struct {
		Repository struct {
			PullRequest struct {
				State       string `json:"state"`
				Merged      bool   `json:"merged"`
				MergeCommit *struct {
					OID string `json:"oid"`
				} `json:"mergeCommit"`
				MergeQueueEntry *struct {
					State string `json:"state"`
				} `json:"mergeQueueEntry"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	err := c.graphql(ctx, `query($owner: String!, $name: String!, $n: Int!) { repository(owner: $owner, name: $name) {
  pullRequest(number: $n) { state merged mergeCommit { oid } mergeQueueEntry { state } } } }`,
		map[string]any{"owner": owner, "name": name, "n": number}, &out)
	if err != nil {
		return State{}, err
	}
	pr := out.Repository.PullRequest
	s := State{Merged: pr.Merged, Closed: pr.State == "CLOSED"}
	if pr.MergeCommit != nil {
		s.MergeCommit = pr.MergeCommit.OID
	}
	if pr.MergeQueueEntry != nil {
		s.Queue = pr.MergeQueueEntry.State
	}
	return s, nil
}

// QueueEvent is one entry of a pull request's merge-queue history: added
// to the queue, or removed from it with GitHub's reason.
type QueueEvent struct {
	Kind   string // "added" or "removed"
	At     string
	Actor  string
	Reason string
}

// QueueHistory is a pull request's queue entry now ("" when it is in no
// queue, as far as the token can see) and its last added/removed events,
// oldest first (FINDING-V15-1A-7: why a queued PR left the queue).
func (c *Client) QueueHistory(ctx context.Context, number int) (entry, mergeState string, events []QueueEvent, err error) {
	owner, name := c.owner()
	type actor struct {
		Login string `json:"login"`
	}
	var out struct {
		Repository struct {
			PullRequest struct {
				MergeStateStatus string `json:"mergeStateStatus"`
				MergeQueueEntry  *struct {
					State string `json:"state"`
				} `json:"mergeQueueEntry"`
				TimelineItems struct {
					Nodes []struct {
						Type      string `json:"__typename"`
						CreatedAt string `json:"createdAt"`
						Actor     *actor `json:"actor"`
						Reason    string `json:"reason"`
					} `json:"nodes"`
				} `json:"timelineItems"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	err = c.graphql(ctx, `query($owner: String!, $name: String!, $n: Int!) { repository(owner: $owner, name: $name) {
  pullRequest(number: $n) { mergeStateStatus mergeQueueEntry { state }
    timelineItems(last: 20, itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT, REMOVED_FROM_MERGE_QUEUE_EVENT]) { nodes { __typename
      ... on AddedToMergeQueueEvent { createdAt actor { login } }
      ... on RemovedFromMergeQueueEvent { createdAt actor { login } reason } } } } } }`,
		map[string]any{"owner": owner, "name": name, "n": number}, &out)
	if err != nil {
		return "", "", nil, err
	}
	pr := out.Repository.PullRequest
	if pr.MergeQueueEntry != nil {
		entry = pr.MergeQueueEntry.State
	}
	for _, n := range pr.TimelineItems.Nodes {
		e := QueueEvent{At: n.CreatedAt, Reason: n.Reason}
		if n.Actor != nil {
			e.Actor = n.Actor.Login
		}
		switch n.Type {
		case "AddedToMergeQueueEvent":
			e.Kind = "added"
		case "RemovedFromMergeQueueEvent":
			e.Kind = "removed"
		default:
			continue
		}
		events = append(events, e)
	}
	return entry, pr.MergeStateStatus, events, nil
}

// ErrNoQueue: the branch has no merge queue.
var ErrNoQueue = errors.New("github: the branch has no merge queue")

// QueueMergeMethod is the merge method of branch's merge queue (MERGE,
// SQUASH or REBASE); ErrNoQueue when it has none. `ape project check`
// requires "MERGE" on main (section 5.3, OQ17).
//
// It reads the merge_queue rule of the rulesets that apply to branch
// first (REST rules/branches, Metadata: read): measured on the mirror
// (FINDING-V15-1B-PRE-2), GitHub's GraphQL refuses an App token
// mergeQueue.configuration even with Merge queues: read, and the
// rulesets answer. GraphQL stays the fallback for a queue no ruleset
// carries; its refusal then comes back with why the rulesets did not
// answer.
func (c *Client) QueueMergeMethod(ctx context.Context, branch string) (string, error) {
	method, rulesErr := c.rulesetQueueMethod(ctx, branch)
	if rulesErr == nil {
		return method, nil
	}
	owner, name := c.owner()
	var out struct {
		Repository struct {
			MergeQueue *struct {
				Configuration *struct {
					MergeMethod string `json:"mergeMethod"`
				} `json:"configuration"`
			} `json:"mergeQueue"`
		} `json:"repository"`
	}
	err := c.graphql(ctx, `query($owner: String!, $name: String!, $b: String!) { repository(owner: $owner, name: $name) {
  mergeQueue(branch: $b) { configuration { mergeMethod } } } }`,
		map[string]any{"owner": owner, "name": name, "b": branch}, &out)
	if err != nil {
		return "", fmt.Errorf("%w; GraphQL: %w", rulesErr, err)
	}
	mq := out.Repository.MergeQueue
	if mq == nil || mq.Configuration == nil {
		return "", ErrNoQueue
	}
	return mq.Configuration.MergeMethod, nil
}

// errNoQueueRule: no ruleset applying to the branch carries a merge_queue
// rule (a queue may still be set outside the rulesets).
var errNoQueueRule = errors.New("no merge_queue rule in the rulesets that apply to it")

// rulesetQueueMethod is the merge_method of the merge_queue rule among the
// rules that apply to branch, upper-cased as GraphQL spells it.
func (c *Client) rulesetQueueMethod(ctx context.Context, branch string) (string, error) {
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			MergeMethod string `json:"merge_method"`
		} `json:"parameters"`
	}
	if err := c.do(ctx, http.MethodGet, c.API+"/repos/"+c.Repo+"/rules/branches/"+branch, nil, &rules); err != nil {
		return "", fmt.Errorf("rulesets: %w", err)
	}
	for _, r := range rules {
		if r.Type == "merge_queue" && r.Parameters.MergeMethod != "" {
			return strings.ToUpper(r.Parameters.MergeMethod), nil
		}
	}
	return "", errNoQueueRule
}
