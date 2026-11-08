package ciwatch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// fake is GitHub's REST for the watcher: main's ref and the merge groups
// with ETags (304 on a match), a compare per sha, and the token it takes.
type fake struct {
	mu      sync.Mutex
	main    string
	groups  []Ref
	compare map[string]string // sha → status; absent: 404
	token   string
	limited bool
	// compareFails answers every compare 500.
	compareFails bool
	gets         map[string]int
	notMod       int
	authSeen     []string
}

func (f *fake) etag(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`"%x"`, sha256.Sum256(data))
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets[r.URL.Path]++
	f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.limited {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", "4102444800")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	reply := func(v any) {
		tag := f.etag(v)
		if r.Header.Get("If-None-Match") == tag {
			f.notMod++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", tag)
		if err := json.NewEncoder(w).Encode(v); err != nil {
			panic(err)
		}
	}
	switch p := r.URL.Path; {
	case p == "/repos/shop/notes/git/ref/heads/main":
		reply(map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": f.main}})
	case p == "/repos/shop/notes/git/matching-refs/heads/gh-readonly-queue/main/":
		out := []map[string]any{}
		for _, g := range f.groups {
			out = append(out, map[string]any{"ref": g.Ref, "object": map[string]string{"sha": g.SHA}})
		}
		reply(out)
	case strings.HasPrefix(p, "/repos/shop/notes/compare/") && f.compareFails:
		w.WriteHeader(http.StatusInternalServerError)
	case strings.HasPrefix(p, "/repos/shop/notes/compare/"):
		sha := strings.TrimSuffix(strings.TrimPrefix(p, "/repos/shop/notes/compare/"), "...main")
		st, ok := f.compare[sha]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"status": st}); err != nil {
			panic(err)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fake) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func group(pr int, sha string) Ref {
	return Ref{Ref: fmt.Sprintf("%spr-%d-%s", GroupPrefix, pr, "base0000"), SHA: sha}
}

func newWatcher(t *testing.T, f *fake, published *[]Published) *Watcher {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var refreshes int
	return New(Config{
		API: srv.URL, Repo: "shop/notes",
		Token: func(_ context.Context, refresh bool) (string, error) {
			if refresh {
				refreshes++
				return "tok2", nil
			}
			if refreshes > 0 {
				return "tok2", nil
			}
			return "tok1", nil
		},
		Publish: func(_ context.Context, p Published) error { *published = append(*published, p); return nil },
	})
}

func events(ps []Published) []string {
	out := make([]string, 0, len(ps))
	for i := range ps {
		out = append(out, ps[i].Event.Event+" "+ps[i].Event.Reason)
	}
	return out
}

// D876 (d): the first look is a baseline; main's move is close-merged with
// the new tip (CI's github.ref_name, github.sha); unchanged reads are 304s.
func TestLook_BaselineThenMainMoves(t *testing.T) {
	f := &fake{main: "aaa1", token: "tok1", gets: map[string]int{}, compare: map[string]string{}}
	var got []Published
	w := newWatcher(t, f, &got)
	r, err := w.Look(context.Background())
	require.NoError(t, err)
	require.True(t, r.Baseline)
	require.Empty(t, got, "restart: the first look publishes nothing")

	_, err = w.Look(context.Background())
	require.NoError(t, err)
	require.Empty(t, got)
	require.Equal(t, 2, f.notMod, "an unchanged second look is two 304s")

	f.set(func() { f.main = "bbb2" })
	_, err = w.Look(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"close-merged main bbb2"}, events(got))
	require.Equal(t, "ci-watch-close-merged-bbb2", got[0].MsgID)
	require.Empty(t, got[0].Event.Milestone)
}

// DIM-1 and DIM-2: a pull request's group that left the queue is merged
// iff compare {sha}...main is identical or ahead (no event; its tip move
// is close-merged), else ejected with the ref and sha last seen; a group
// re-created under a new sha is no ejection; a sha GitHub does not know
// publishes nothing.
func TestLook_GroupsLeavingTheQueue(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   []string
	}{
		{"identical", []string{}},
		{"ahead", []string{}},
		{"behind", []string{"ejected " + group(7, "g7").Ref + " g7"}},
		{"diverged", []string{"ejected " + group(7, "g7").Ref + " g7"}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			f := &fake{main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{"g7": tc.status}, groups: []Ref{group(7, "g7")}}
			var got []Published
			w := newWatcher(t, f, &got)
			_, err := w.Look(context.Background())
			require.NoError(t, err)
			f.set(func() { f.groups = nil })
			r, err := w.Look(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.want, events(got))
			require.Equal(t, []CompareResult{{PR: 7, Ref: group(7, "g7").Ref, SHA: "g7", Status: tc.status}}, r.Compares)
		})
	}

	t.Run("re-created under a new sha", func(t *testing.T) {
		f := &fake{main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{"g7": "diverged"}, groups: []Ref{group(7, "g7")}}
		var got []Published
		w := newWatcher(t, f, &got)
		_, err := w.Look(context.Background())
		require.NoError(t, err)
		f.set(func() { f.groups = []Ref{{Ref: GroupPrefix + "pr-7-base1111", SHA: "g7b"}} })
		_, err = w.Look(context.Background())
		require.NoError(t, err)
		require.Empty(t, got, "pr #7 still has a group: nothing")
		require.Zero(t, f.gets["/repos/shop/notes/compare/g7...main"], "and no compare")
	})
	t.Run("an unknown sha", func(t *testing.T) {
		f := &fake{main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{}, groups: []Ref{group(9, "g9")}}
		var got []Published
		w := newWatcher(t, f, &got)
		_, err := w.Look(context.Background())
		require.NoError(t, err)
		f.set(func() { f.groups = nil })
		_, err = w.Look(context.Background())
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("merged: the tip moves to the group", func(t *testing.T) {
		f := &fake{main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{"g7": "identical"}, groups: []Ref{group(7, "g7")}}
		var got []Published
		w := newWatcher(t, f, &got)
		_, err := w.Look(context.Background())
		require.NoError(t, err)
		f.set(func() { f.groups, f.main = nil, "g7" })
		_, err = w.Look(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"close-merged main g7"}, events(got))
	})
}

// A refused token (401) is asked for again once; a rate limit says until
// when; a dry run (no Publish) decides and publishes nothing.
func TestLook_TokenRateLimitDryRun(t *testing.T) {
	f := &fake{main: "m1", token: "tok2", gets: map[string]int{}, compare: map[string]string{}}
	var got []Published
	w := newWatcher(t, f, &got)
	_, err := w.Look(context.Background())
	require.NoError(t, err, "the 401 refreshed the token")
	require.Contains(t, f.authSeen, "Bearer tok1")
	require.Contains(t, f.authSeen, "Bearer tok2")

	f.set(func() { f.limited = true })
	_, err = w.Look(context.Background())
	var rl *RateLimitError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, time.Unix(4102444800, 0), rl.Until)

	dry := New(Config{API: w.cfg.API, Repo: "shop/notes", Token: func(context.Context, bool) (string, error) { return "tok2", nil }})
	f.set(func() { f.limited = false })
	_, err = dry.Look(context.Background())
	require.NoError(t, err)
	f.set(func() { f.main = "m2" })
	r, err := dry.Look(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"close-merged main m2"}, events(r.Events), "decided")
	require.Empty(t, got, "published nothing")
}

// research's DIM point 3: the watcher's event is `ape ci event`'s, byte
// for byte, at the same moment — one builder (apexv1.CIEvent).
func TestEvent_EqualsCIEvent(t *testing.T) {
	at := time.Date(2026, 11, 8, 20, 0, 0, 123, time.UTC)
	w := New(Config{Now: func() time.Time { return at }})
	for _, tc := range [][3]string{{"close-merged", "main", "abc"}, {"ejected", GroupPrefix + "pr-3-x", "def"}} {
		got, err := json.Marshal(w.event(tc[0], tc[1], tc[2]).Event)
		require.NoError(t, err)
		want, err := json.Marshal(apexv1.CIEvent(tc[0], tc[1], tc[2], "", at))
		require.NoError(t, err)
		require.Equal(t, string(want), string(got))
	}
}

// DIM-4 (research's probe): a pull request listing two group refs at its
// last look is merged when any of their shas is in main — here the one
// listed first — so its leaving is close-merged and no ejected.
func TestLook_TwoGroupsOneMerged(t *testing.T) {
	f := &fake{
		main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{"aanew": "ahead", "zzold": "diverged"},
		groups: []Ref{{Ref: GroupPrefix + "pr-7-aaaa", SHA: "aanew"}, {Ref: GroupPrefix + "pr-7-zzzz", SHA: "zzold"}},
	}
	var got []Published
	w := newWatcher(t, f, &got)
	_, err := w.Look(context.Background())
	require.NoError(t, err)
	f.set(func() { f.groups, f.main = nil, "aanew" })
	_, err = w.Look(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"close-merged main aanew"}, events(got), "no false ejection of a merged pull request")

	// The control: neither sha in main is an ejection, of the ref listed last.
	f2 := &fake{
		main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{"aanew": "behind", "zzold": "diverged"},
		groups: []Ref{{Ref: GroupPrefix + "pr-7-aaaa", SHA: "aanew"}, {Ref: GroupPrefix + "pr-7-zzzz", SHA: "zzold"}},
	}
	var got2 []Published
	w2 := newWatcher(t, f2, &got2)
	_, err = w2.Look(context.Background())
	require.NoError(t, err)
	f2.set(func() { f2.groups = nil })
	_, err = w2.Look(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"ejected " + GroupPrefix + "pr-7-zzzz zzold"}, events(got2))
}

// DIM-6: the watcher's state moves only once the look's events are
// published. A publish that fails (NATS down) leaves it as it was, so the
// next look sees the same move — not a 304 — and publishes it; an
// ejection likewise. A failed compare is retried the same way.
func TestLook_StateOnlyAfterAPublish(t *testing.T) {
	f := &fake{main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{"g7": "diverged"}, groups: []Ref{group(7, "g7")}}
	var got []Published
	failing := true
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	w := New(Config{
		API: srv.URL, Repo: "shop/notes",
		Token: func(context.Context, bool) (string, error) { return "tok1", nil },
		Publish: func(_ context.Context, p Published) error {
			if failing {
				return errors.New("nats: no servers available")
			}
			got = append(got, p)
			return nil
		},
	})
	_, err := w.Look(context.Background())
	require.NoError(t, err, "a baseline publishes nothing, so nothing fails")
	f.set(func() { f.main, f.groups = "m2", nil })
	_, err = w.Look(context.Background())
	require.ErrorContains(t, err, "no servers available")
	require.Empty(t, got)
	_, err = w.Look(context.Background())
	require.Error(t, err, "still failing: still not moved")

	failing = false
	_, err = w.Look(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"close-merged main m2", "ejected " + group(7, "g7").Ref + " g7"}, events(got), "published once NATS is back")
	_, err = w.Look(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2, "and then the state moved: nothing again")

	// A compare that fails (here a 500) keeps the groups: judged again.
	f3 := &fake{main: "m1", token: "tok1", gets: map[string]int{}, compare: map[string]string{}, groups: []Ref{group(9, "g9")}, compareFails: true}
	var got3 []Published
	w3 := newWatcher(t, f3, &got3)
	_, err = w3.Look(context.Background())
	require.NoError(t, err)
	f3.set(func() { f3.groups = nil })
	_, err = w3.Look(context.Background())
	require.ErrorContains(t, err, "compare with main failed")
	f3.set(func() { f3.compareFails, f3.compare["g9"] = false, "diverged" })
	_, err = w3.Look(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"ejected " + group(9, "g9").Ref + " g9"}, events(got3))
}
