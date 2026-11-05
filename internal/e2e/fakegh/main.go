// Command fakegh is a stand-in for the GitHub a coordination.github project
// talks to, for scratch fixtures only (D807+, H5): the REST and GraphQL calls
// internal/coord/github makes, over a local bare repository, with main's merge
// queue merging each enqueued pull request itself (a merge commit, as the
// queue's MERGE method makes) and ejecting one that conflicts. It is never
// linked into ape and never talks to GitHub.
//
//	fakegh -addr 127.0.0.1:25300 -bare <origin.git> [-delay 2s]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type event struct {
	kind, at, reason string
}

type pr struct {
	Number      int
	NodeID      string
	Head, Base  string
	Title       string
	Closed      bool
	Merged      bool
	MergeCommit string
	MergedAt    string
	Queued      bool
	Events      []event
}

type server struct {
	mu    sync.Mutex
	prs   []*pr
	bare  string
	delay time.Duration
}

func main() {
	addr := flag.String("addr", "127.0.0.1:25300", "listen address")
	bare := flag.String("bare", "", "the bare repository standing in for GitHub's (required)")
	delay := flag.Duration("delay", 2*time.Second, "how long the queue takes to merge an enqueued pull request")
	flag.Parse()
	if *bare == "" {
		log.Fatal("fakegh: -bare is required")
	}
	s := &server{bare: *bare, delay: *delay}
	log.Printf("fakegh: serving %s on http://%s", *bare, *addr)
	srv := &http.Server{Addr: *addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %q", r.Method, r.URL.String()) //nolint:gosec // a scratch fixture's own request log
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/graphql"):
		s.graphql(w, r)
	case strings.Contains(path, "/rules/branches/"):
		branch := path[strings.Index(path, "/rules/branches/")+len("/rules/branches/"):]
		if branch == "main" {
			write(w, []map[string]any{{"type": "pull_request"}, {"type": "merge_queue", "parameters": map[string]any{"merge_method": "MERGE"}}})
			return
		}
		write(w, []any{})
	case strings.HasSuffix(path, "/pulls") && r.Method == http.MethodPost:
		s.open(w, r)
	case strings.HasSuffix(path, "/pulls") && r.Method == http.MethodGet:
		s.list(w, r)
	default:
		http.Error(w, `{"message":"fakegh: not served"}`, http.StatusNotFound)
	}
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("fakegh: write: %v", err)
	}
}

func (p *pr) rest() map[string]any {
	state := "open"
	if p.Closed {
		state = "closed"
	}
	m := map[string]any{"number": p.Number, "node_id": p.NodeID, "html_url": fmt.Sprintf("http://fakegh/pull/%d", p.Number), "state": state}
	if p.Merged {
		m["merge_commit_sha"], m["merged_at"] = p.MergeCommit, p.MergedAt
	}
	return m
}

func (s *server) open(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Head  string `json:"head"`
		Base  string `json:"base"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, `{"message":"bad body"}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &pr{Number: len(s.prs) + 1, Head: in.Head, Base: in.Base, Title: in.Title}
	p.NodeID = "PR_" + strconv.Itoa(p.Number)
	s.prs = append(s.prs, p)
	write(w, p.rest())
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	_, head, _ := strings.Cut(q.Get("head"), ":")
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, p := range slices.Backward(s.prs) { // newest first
		if (head != "" && p.Head != head) || (q.Get("base") != "" && p.Base != q.Get("base")) || (q.Get("state") == "open" && p.Closed) {
			continue
		}
		out = append(out, p.rest())
	}
	write(w, out)
}

func (s *server) find(id string, number int) *pr {
	for _, p := range s.prs {
		if (id != "" && p.NodeID == id) || (number != 0 && p.Number == number) {
			return p
		}
	}
	return nil
}

func (s *server) graphql(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, `{"message":"bad body"}`, http.StatusBadRequest)
		return
	}
	id, _ := in.Variables["id"].(string)
	n := 0
	if f, ok := in.Variables["n"].(float64); ok {
		n = int(f)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	data := map[string]any{}
	switch q := in.Query; {
	case strings.Contains(q, "enqueuePullRequest"):
		p := s.find(id, 0)
		if p == nil || p.Closed {
			write(w, map[string]any{"errors": []any{map[string]any{"message": "fakegh: no open pull request " + id}}})
			return
		}
		if !p.Queued {
			p.Queued = true
			p.Events = append(p.Events, event{kind: "added", at: now})
			go s.mergeLater(p.NodeID)
		}
		data["enqueuePullRequest"] = map[string]any{"mergeQueueEntry": map[string]any{"state": "QUEUED"}}
	case strings.Contains(q, "dequeuePullRequest"):
		if p := s.find(id, 0); p != nil && p.Queued {
			p.Queued = false
			p.Events = append(p.Events, event{kind: "removed", at: now, reason: "DEQUEUED"})
		}
		data["dequeuePullRequest"] = map[string]any{"clientMutationId": nil}
	case strings.Contains(q, "mergeQueue(branch"):
		data["repository"] = map[string]any{"mergeQueue": map[string]any{"configuration": map[string]any{"mergeMethod": "MERGE"}}}
	case strings.Contains(q, "timelineItems"):
		p := s.find("", n)
		if p == nil {
			data["repository"] = map[string]any{"pullRequest": nil}
			break
		}
		nodes := []map[string]any{}
		for _, e := range p.Events {
			t := "AddedToMergeQueueEvent"
			if e.kind == "removed" {
				t = "RemovedFromMergeQueueEvent"
			}
			nodes = append(nodes, map[string]any{"__typename": t, "createdAt": e.at, "actor": map[string]any{"login": "fakegh"}, "reason": e.reason})
		}
		data["repository"] = map[string]any{"pullRequest": map[string]any{"mergeStateStatus": "CLEAN", "mergeQueueEntry": queueEntry(p), "timelineItems": map[string]any{"nodes": nodes}}}
	case strings.Contains(q, "pullRequest(number"):
		p := s.find("", n)
		if p == nil {
			data["repository"] = map[string]any{"pullRequest": nil}
			break
		}
		data["repository"] = map[string]any{"pullRequest": prState(p)}
	default:
		write(w, map[string]any{"errors": []any{map[string]any{"message": "fakegh: query not served"}}})
		return
	}
	write(w, map[string]any{"data": data})
}

func queueEntry(p *pr) any {
	if !p.Queued {
		return nil
	}
	return map[string]any{"state": "QUEUED"}
}

func prState(p *pr) map[string]any {
	state := "OPEN"
	switch {
	case p.Merged:
		state = "MERGED"
	case p.Closed:
		state = "CLOSED"
	}
	var mc any
	if p.Merged {
		mc = map[string]any{"oid": p.MergeCommit}
	}
	return map[string]any{"state": state, "merged": p.Merged, "mergeCommit": mc, "mergeQueueEntry": queueEntry(p)}
}

// mergeLater is main's queue: after the delay it merges the pull request's
// head into its base in the bare repository (a merge commit), or ejects it
// on a conflict.
func (s *server) mergeLater(id string) {
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.find(id, 0)
	if p == nil || !p.Queued {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	sha, err := s.merge(p)
	p.Queued = false
	if err != nil {
		log.Printf("fakegh: #%d ejected: %v", p.Number, err)
		p.Events = append(p.Events, event{kind: "removed", at: now, reason: "MERGE_CONFLICT"})
		return
	}
	p.Merged, p.Closed, p.MergeCommit, p.MergedAt = true, true, sha, now
	log.Printf("fakegh: #%d merged %s into %s as %s", p.Number, p.Head, p.Base, sha)
}

func (s *server) git(args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", s.bare}, args...)...) //nolint:gosec,noctx // the fixture's own bare repository and ref names
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=fakegh", "GIT_AUTHOR_EMAIL=fakegh@example.invalid",
		"GIT_COMMITTER_NAME=fakegh", "GIT_COMMITTER_EMAIL=fakegh@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *server) merge(p *pr) (string, error) {
	base, err := s.git("rev-parse", "refs/heads/"+p.Base)
	if err != nil {
		return "", err
	}
	head, err := s.git("rev-parse", "refs/heads/"+p.Head)
	if err != nil {
		return "", err
	}
	tree, err := s.git("merge-tree", "--write-tree", base, head)
	if err != nil {
		return "", err
	}
	tree = strings.Fields(tree)[0]
	msg := fmt.Sprintf("Merge pull request #%d from %s\n\n%s", p.Number, p.Head, p.Title)
	sha, err := s.git("commit-tree", tree, "-p", base, "-p", head, "-m", msg)
	if err != nil {
		return "", err
	}
	if _, err := s.git("update-ref", "refs/heads/"+p.Base, sha, base); err != nil {
		return "", err
	}
	return sha, nil
}
