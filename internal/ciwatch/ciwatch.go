// Package ciwatch is `ape ci watch` (APEX v1.5, D876, queue/watch): the
// coordination host's process that tells a project's waiting closes their
// merge queue moved, with nothing run on GitHub and no secret on the
// repository. Every tick (Config.Every) it reads, over REST and conditionally (an ETag;
// a 304 costs no rate limit), main's tip and the merge groups' branches
// (gh-readonly-queue/main/pr-<n>-…), and publishes what CI's apex-queue.yml
// would have: close-merged when main's tip moved, ejected when a pull
// request's merge group left the queue without reaching main. The events
// decide nothing: they wake the close, which reads GitHub itself, and its
// QueuePoll stays the fallback. Stateless: a start's first look is a
// baseline and publishes nothing.
package ciwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// GroupPrefix is the merge groups' branch prefix on main.
const GroupPrefix = "refs/heads/gh-readonly-queue/main/"

// groupRe reads a merge group branch's pull request number:
// gh-readonly-queue/main/pr-<n>-<sha>.
var groupRe = regexp.MustCompile(`^` + regexp.QuoteMeta(GroupPrefix) + `pr-(\d+)-`)

// Ref is a branch as REST reports it.
type Ref struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// Published is one event a look decided on, and its message id
// (ci-watch-<event>-<sha>: two watchers of one merge publish it once).
type Published struct {
	Event apexv1.Event `json:"event"`
	MsgID string       `json:"msg_id"`
}

// Report is what one look read and decided: each endpoint's HTTP status
// (304 unchanged), main's tip and the merge groups by pull request number
// as they stand after the look.
type Report struct {
	MainStatus   int             `json:"main_status"`
	Main         string          `json:"main"`
	GroupsStatus int             `json:"groups_status"`
	Groups       map[int][]Ref   `json:"groups"`
	Compares     []CompareResult `json:"compares,omitempty"`
	Events       []Published     `json:"events"`
	Baseline     bool            `json:"baseline,omitempty"`
}

// CompareResult is a gone group's compare against main.
type CompareResult struct {
	PR     int    `json:"pr"`
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	Status string `json:"status"`
}

// Config is a watcher's.
type Config struct {
	// API is the REST base (https://api.github.com, or fakegh's).
	API string
	// Repo is <owner>/<name>.
	Repo string
	// Token returns the watch token; refresh asks for a new one (after a
	// 401).
	Token func(ctx context.Context, refresh bool) (string, error)
	// Publish publishes one event with its message id; nil is a dry run.
	Publish func(ctx context.Context, p Published) error
	// Every is the tick (default 10 s).
	Every time.Duration
	// Log gets one line per change of state and per event (nil: none).
	Log  io.Writer
	HTTP *http.Client
	Now  func() time.Time
}

// Watcher keeps the last look: ETags, main's tip, the groups by PR.
type Watcher struct {
	cfg                    Config
	mainETag, groupsETag   string
	main                   string
	groups                 map[int][]Ref
	mainKnown, groupsKnown bool
}

// New is a watcher of cfg.
func New(cfg Config) *Watcher {
	if cfg.Every <= 0 {
		cfg.Every = 10 * time.Second
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.API = strings.TrimRight(cfg.API, "/")
	return &Watcher{cfg: cfg}
}

// RateLimitError is GitHub's 403/429 with when to try again.
type RateLimitError struct {
	Status int
	Until  time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("ciwatch: rate limited (%d) until %s", e.Status, e.Until.UTC().Format(time.RFC3339))
}

// errUnauthorized: the token was refused (expired or revoked).
var errUnauthorized = errors.New("ciwatch: the token was refused (401)")

// get reads path into out, conditionally on etag: 304 leaves out alone.
func (w *Watcher) get(ctx context.Context, path, etag string, out any) (status int, newETag string, err error) {
	for attempt := range 2 {
		tok, err := w.cfg.Token(ctx, attempt > 0)
		if err != nil {
			return 0, "", fmt.Errorf("ciwatch: the watch token: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.cfg.API+path, http.NoBody)
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-Github-Api-Version", "2022-11-28")
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := w.cfg.HTTP.Do(req)
		if err != nil {
			return 0, "", fmt.Errorf("ciwatch: GET %s: %w", path, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotModified:
			return resp.StatusCode, etag, nil
		case resp.StatusCode == http.StatusUnauthorized && attempt == 0:
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			return resp.StatusCode, "", errUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden && limited(resp):
			return resp.StatusCode, "", &RateLimitError{Status: resp.StatusCode, Until: w.retryAt(resp)}
		case resp.StatusCode == http.StatusNotFound:
			return resp.StatusCode, "", nil
		case resp.StatusCode != http.StatusOK:
			return resp.StatusCode, "", fmt.Errorf("ciwatch: GET %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, "", fmt.Errorf("ciwatch: GET %s: %w", path, err)
		}
		return resp.StatusCode, resp.Header.Get("ETag"), nil
	}
	return 0, "", errUnauthorized
}

// limited: a 403 that is GitHub's rate limit, not a permission refusal.
func limited(resp *http.Response) bool {
	return resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-Ratelimit-Remaining") == "0"
}

// retryAt is when GitHub says to try again: Retry-After (seconds), else
// X-RateLimit-Reset (epoch seconds), else a minute.
func (w *Watcher) retryAt(resp *http.Response) time.Time {
	now := w.cfg.Now()
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
		return now.Add(time.Duration(s) * time.Second)
	}
	if s, err := strconv.ParseInt(resp.Header.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
		return time.Unix(s, 0)
	}
	return now.Add(time.Minute)
}

func (w *Watcher) logf(format string, a ...any) {
	if w.cfg.Log != nil {
		fmt.Fprintf(w.cfg.Log, "%s ape ci watch: %s\n", w.cfg.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}
}

func (w *Watcher) event(name, ref, sha string) Published {
	return Published{Event: apexv1.CIEvent(name, ref, sha, "", w.cfg.Now()), MsgID: "ci-watch-" + name + "-" + sha}
}

// Look is one tick: main's tip, then the merge groups. The first look of
// each is a baseline (nothing published). A pull request with no group
// branch left is compared with main, every group sha last seen for it
// (DIM-1: a sha is in main iff compare {sha}...main is identical or
// ahead; DIM-4: merged iff any is): merged, nothing (its tip's move is
// close-merged); else ejected, with the ref and sha listed last. Keyed by
// pull request (DIM-2), so a group re-created under a new sha is no
// ejection. The watcher's state moves only once the look's events are
// published (DIM-6): a failed publish is looked at, and published, again.
func (w *Watcher) Look(ctx context.Context) (Report, error) {
	r := Report{Groups: map[int][]Ref{}}
	next := *w
	err := next.lookMain(ctx, &r)
	if err == nil {
		err = next.lookGroups(ctx, &r)
	}
	if perr := w.publish(ctx, r); perr != nil {
		return r, errors.Join(err, perr)
	}
	*w = next
	r.Main = w.main
	maps.Copy(r.Groups, w.groups)
	return r, err
}

// lookMain reads main's tip into next, and its move as close-merged.
func (w *Watcher) lookMain(ctx context.Context, r *Report) error {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	status, etag, err := w.get(ctx, "/repos/"+w.cfg.Repo+"/git/ref/heads/main", w.mainETag, &ref)
	r.MainStatus = status
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		w.mainETag = etag
		if sha := ref.Object.SHA; sha != "" && sha != w.main {
			if w.mainKnown {
				r.Events = append(r.Events, w.event("close-merged", "main", sha))
				w.logf("main moved %.7s → %.7s", w.main, sha)
			}
			w.main = sha
		}
	}
	if !w.mainKnown && w.main != "" {
		w.mainKnown, r.Baseline = true, true
	}
	return nil
}

// lookGroups reads the merge groups into next, and judges each pull
// request whose groups are all gone.
func (w *Watcher) lookGroups(ctx context.Context, r *Report) error {
	var refs []struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	status, etag, err := w.get(ctx, "/repos/"+w.cfg.Repo+"/git/matching-refs/"+strings.TrimPrefix(GroupPrefix, "refs/"), w.groupsETag, &refs)
	r.GroupsStatus = status
	if err != nil || status != http.StatusOK && status != http.StatusNotFound {
		return err
	}
	now := map[int][]Ref{}
	for _, g := range refs {
		if m := groupRe.FindStringSubmatch(g.Ref); m != nil {
			n, _ := strconv.Atoi(m[1])
			now[n] = append(now[n], Ref{Ref: g.Ref, SHA: g.Object.SHA})
		}
	}
	if !w.groupsKnown {
		r.Baseline = true
	}
	for _, n := range sortedKeys(w.groups) {
		if _, still := now[n]; !still && w.groupsKnown {
			if err := w.judge(ctx, r, n, w.groups[n]); err != nil {
				return err // the groups stay as they were: judged again next look
			}
		}
	}
	w.groups, w.groupsKnown, w.groupsETag = now, true, etag
	return nil
}

// judge is a gone pull request's verdict (DIM-1, DIM-4): merged when any
// of its last-seen group shas is in main; ejected (the ref listed last)
// when none is and GitHub knows at least one; nothing when GitHub knows
// none. A failed compare is the look's error.
func (w *Watcher) judge(ctx context.Context, r *Report, n int, groups []Ref) error {
	known := false
	for _, g := range groups {
		c, err := w.compare(ctx, n, g)
		r.Compares = append(r.Compares, c)
		if err != nil {
			return fmt.Errorf("ciwatch: pr #%d's group %s left the queue, and its compare with main failed: %w", n, g.Ref, err)
		}
		switch c.Status {
		case "identical", "ahead":
			w.logf("pr #%d's group %.7s is in main (%s)", n, g.SHA, c.Status)
			return nil
		case "":
		default:
			known = true
		}
	}
	last := groups[len(groups)-1]
	if !known {
		w.logf("pr #%d's group %s left the queue and GitHub knows none of its shas: nothing published", n, last.Ref)
		return nil
	}
	r.Events = append(r.Events, w.event("ejected", last.Ref, last.SHA))
	w.logf("pr #%d's group %.7s left the queue without reaching main: ejected", n, last.SHA)
	return nil
}

// publish publishes r's events in order (a dry run publishes none).
func (w *Watcher) publish(ctx context.Context, r Report) error {
	if w.cfg.Publish == nil {
		return nil
	}
	for i := range r.Events {
		p := &r.Events[i]
		if err := w.cfg.Publish(ctx, *p); err != nil {
			return fmt.Errorf("ciwatch: publish %s: %w", p.MsgID, err)
		}
		w.logf("published %s (%s) as %s", p.Event.Event, p.Event.Reason, p.MsgID)
	}
	return nil
}

// compare is GitHub's compare of the group's sha with main: base...head is
// {sha}...main, so "ahead" means main is ahead of the sha, which is in it.
// A 404 (the sha unknown) is Status "".
func (w *Watcher) compare(ctx context.Context, n int, g Ref) (CompareResult, error) {
	c := CompareResult{PR: n, Ref: g.Ref, SHA: g.SHA}
	var out struct {
		Status string `json:"status"`
	}
	_, _, err := w.get(ctx, "/repos/"+w.cfg.Repo+"/compare/"+url.PathEscape(g.SHA)+"...main", "", &out)
	c.Status = out.Status
	return c, err
}

func sortedKeys(m map[int][]Ref) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// MaxBackoff bounds a failed look's wait: from Every, doubling, up to it.
var MaxBackoff = 5 * time.Minute

// Run looks every Every until ctx ends. A failed look backs off
// exponentially (Every, doubling, at most MaxBackoff); a rate limit waits
// until GitHub's reset. Its error is logged, never fatal.
func (w *Watcher) Run(ctx context.Context) error {
	wait := w.cfg.Every
	for {
		_, err := w.Look(ctx)
		next := w.cfg.Every
		var rl *RateLimitError
		switch {
		case err == nil:
			wait = w.cfg.Every
		case errors.As(err, &rl):
			next = max(time.Until(rl.Until), w.cfg.Every)
			w.logf("%v: waiting %s", err, next.Round(time.Second))
		default:
			wait = min(wait*2, MaxBackoff)
			next = wait
			w.logf("look failed (%v): next in %s", err, next)
		}
		t := time.NewTimer(next)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}
