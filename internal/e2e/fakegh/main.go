// Command fakegh is a stand-in for the GitHub a coordination.github project
// talks to, for scratch fixtures only (D807+, H5): the REST and GraphQL calls
// internal/coord/github makes, over a local bare repository, with main's merge
// queue merging each enqueued pull request itself (a merge commit, as the
// queue's MERGE method makes) and ejecting one that conflicts. It is never
// linked into ape and never talks to GitHub.
//
// For `ape ci watch` (D876, queue/watch) it keeps each queued pull request's
// merge group as GitHub does, a branch refs/heads/gh-readonly-queue/main/
// pr-<n>-<main's sha> at the group's merge commit, which main fast-forwards
// to on the merge, and serves main's ref and those branches with ETags
// (304 on If-None-Match) and compare <a>...<b>. A pull request whose title
// holds "[fakegh:eject]" has its group built and then ejected.
//
//	fakegh -addr 127.0.0.1:25300 -bare <origin.git> [-delay 2s]
package main

import (
	"crypto/sha256"
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
	// Group is its merge group's branch while it is queued, and GroupSHA
	// the merge commit there.
	Group, GroupSHA string
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
	case strings.HasSuffix(path, "/git/ref/heads/main") && r.Method == http.MethodGet:
		s.mu.Lock()
		sha, err := s.git("rev-parse", "refs/heads/main")
		s.mu.Unlock()
		if err != nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		conditional(w, r, map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": sha, "type": "commit"}})
	case strings.Contains(path, "/git/matching-refs/heads/gh-readonly-queue/") && r.Method == http.MethodGet:
		prefix := "refs/" + path[strings.Index(path, "/git/matching-refs/")+len("/git/matching-refs/"):]
		s.mu.Lock()
		out, _ := s.git("for-each-ref", "--format=%(refname) %(objectname)", prefix)
		s.mu.Unlock()
		refs := []map[string]any{}
		for line := range strings.Lines(out) {
			if f := strings.Fields(line); len(f) == 2 {
				refs = append(refs, map[string]any{"ref": f[0], "object": map[string]string{"sha": f[1], "type": "commit"}})
			}
		}
		conditional(w, r, refs)
	case strings.Contains(path, "/compare/") && r.Method == http.MethodGet:
		a, b, _ := strings.Cut(path[strings.Index(path, "/compare/")+len("/compare/"):], "...")
		s.mu.Lock()
		status, err := s.compare(a, b)
		s.mu.Unlock()
		if err != nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		write(w, map[string]any{"status": status})
	case strings.HasSuffix(path, "/pulls") && r.Method == http.MethodPost:
		s.open(w, r)
	case strings.HasSuffix(path, "/pulls") && r.Method == http.MethodGet:
		s.list(w, r)
	default:
		http.Error(w, `{"message":"fakegh: not served"}`, http.StatusNotFound)
	}
}

// conditional writes v with an ETag, or 304 when the request's
// If-None-Match is it (GitHub's conditional request: no rate limit spent).
func conditional(w http.ResponseWriter, r *http.Request, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"message":"fakegh: encode"}`, http.StatusInternalServerError)
		return
	}
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// compare is GitHub's compare base...head status: identical, ahead (head
// has base in it), behind (base has head in it), else diverged.
func (s *server) compare(base, head string) (string, error) {
	b, err := s.git("rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil {
		return "", err
	}
	h, err := s.git("rev-parse", "--verify", "--quiet", head+"^{commit}")
	if err != nil {
		return "", err
	}
	switch {
	case b == h:
		return "identical", nil
	case s.ancestor(b, h):
		return "ahead", nil
	case s.ancestor(h, b):
		return "behind", nil
	}
	return "diverged", nil
}

func (s *server) ancestor(a, b string) bool {
	_, err := s.git("merge-base", "--is-ancestor", a, b)
	return err == nil
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
			s.buildGroup(p)
			go s.mergeLater(p.NodeID)
		}
		data["enqueuePullRequest"] = map[string]any{"mergeQueueEntry": map[string]any{"state": "QUEUED"}}
	case strings.Contains(q, "dequeuePullRequest"):
		if p := s.find(id, 0); p != nil && p.Queued {
			p.Queued = false
			p.Events = append(p.Events, event{kind: "removed", at: now, reason: "DEQUEUED"})
			s.dropGroup(p)
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

// buildGroup makes p's merge group as GitHub does on enqueue: its merge
// commit onto main, on gh-readonly-queue/main/pr-<n>-<main's sha>. A
// conflict builds none (the merge ejects it).
func (s *server) buildGroup(p *pr) {
	base, err := s.git("rev-parse", "refs/heads/"+p.Base)
	if err != nil {
		return
	}
	sha, err := s.mergeCommit(p, base)
	if err != nil {
		return
	}
	p.Group, p.GroupSHA = fmt.Sprintf("refs/heads/gh-readonly-queue/%s/pr-%d-%s", p.Base, p.Number, base), sha
	if _, err := s.git("update-ref", p.Group, sha); err != nil {
		log.Printf("fakegh: #%d group: %v", p.Number, err)
		p.Group, p.GroupSHA = "", ""
	}
}

func (s *server) dropGroup(p *pr) {
	if p.Group != "" {
		_, _ = s.git("update-ref", "-d", p.Group)
	}
	p.Group, p.GroupSHA = "", ""
}

// mergeLater is main's queue: after the delay it fast-forwards the base to
// the pull request's merge group (a merge commit; rebuilt if the base
// moved meanwhile), or ejects it on a conflict or the title's eject knob.
func (s *server) mergeLater(id string) {
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.find(id, 0)
	if p == nil || !p.Queued {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if strings.Contains(p.Title, "[fakegh:eject]") {
		p.Queued = false
		p.Events = append(p.Events, event{kind: "removed", at: now, reason: "FAILED_CHECKS"})
		s.dropGroup(p)
		log.Printf("fakegh: #%d ejected (the title's knob)", p.Number)
		return
	}
	sha, err := s.merge(p)
	p.Queued = false
	s.dropGroup(p)
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
	sha := p.GroupSHA
	if sha == "" || !strings.HasSuffix(p.Group, "-"+base) {
		if sha, err = s.mergeCommit(p, base); err != nil {
			return "", err
		}
	}
	if _, err := s.git("update-ref", "refs/heads/"+p.Base, sha, base); err != nil {
		return "", err
	}
	return sha, nil
}

// mergeCommit is p's head merged onto base: a merge commit, as the
// queue's MERGE method makes.
func (s *server) mergeCommit(p *pr, base string) (string, error) {
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
	return s.git("commit-tree", tree, "-p", base, "-p", head, "-m", msg)
}
