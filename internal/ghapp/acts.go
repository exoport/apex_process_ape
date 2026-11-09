package ghapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/nacl/box"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// The set-up acts (PLAN-V15-GHAPP §4, §6): templated, nothing else. Each
// takes an apex-warden installation token for the repository.

// RequiredCheck is the status check R4 requires: the queue workflow's job
// (ciworkflow.go names its job so).
const RequiredCheck = "apex-queue-check"

// queueCheckTimeoutMinutes bounds a merge group's check in main's queue:
// a close runs the milestone gate (atomix's ≈ 50 minutes), with room.
const queueCheckTimeoutMinutes = 360

// Rule names, one ruleset each (research: a bypass covers every rule of its
// ruleset, so they are split).
const (
	RulesetR1 = "apex R1 milestone writes"
	RulesetR2 = "apex R2 no force pushes"
	RulesetR3 = "apex R3 no milestone deletions"
	RulesetR4 = "apex R4 main through the merge queue"
)

// Ruleset is a repository ruleset as the REST API takes it.
type Ruleset struct {
	ID           int64            `json:"id,omitempty"`
	Name         string           `json:"name"`
	Target       string           `json:"target"`
	Enforcement  string           `json:"enforcement"`
	Conditions   map[string]any   `json:"conditions"`
	Rules        []map[string]any `json:"rules"`
	BypassActors []map[string]any `json:"bypass_actors"`
}

func refs(include ...string) map[string]any {
	return map[string]any{"ref_name": map[string]any{"include": include, "exclude": []string{}}}
}

// Rulesets are R1–R4 for a repository whose member App is pusherAppID
// (research CHECK-V15-SETUP; D703: R3 with no bypass for 1a). withCheck
// adds R4's required check; without the queue workflow on the default
// branch it is left out, since no PR could pass it — the workflow's own
// PR included.
func Rulesets(pusherAppID int64, withCheck bool) []Ruleset {
	member := []map[string]any{{"actor_id": pusherAppID, "actor_type": "Integration", "bypass_mode": "always"}}
	rs := []Ruleset{
		{
			Name: RulesetR1, Target: "branch", Enforcement: "active", Conditions: refs("refs/heads/milestone/*"),
			Rules: []map[string]any{{"type": "creation"}, {"type": "update"}}, BypassActors: member,
		},
		{
			Name: RulesetR2, Target: "branch", Enforcement: "active", Conditions: refs("refs/heads/milestone/*", "~DEFAULT_BRANCH"),
			Rules: []map[string]any{{"type": "non_fast_forward"}}, BypassActors: []map[string]any{},
		},
		{
			Name: RulesetR3, Target: "branch", Enforcement: "active", Conditions: refs("refs/heads/milestone/*", "~DEFAULT_BRANCH"),
			Rules: []map[string]any{{"type": "deletion"}}, BypassActors: []map[string]any{},
		},
		{
			Name: RulesetR4, Target: "branch", Enforcement: "active", Conditions: refs("~DEFAULT_BRANCH"),
			Rules: []map[string]any{
				{"type": "pull_request", "parameters": map[string]any{
					"required_approving_review_count": 0, "dismiss_stale_reviews_on_push": false, "require_code_owner_review": false,
					"require_last_push_approval": false, "required_review_thread_resolution": false,
				}},
				{"type": "merge_queue", "parameters": map[string]any{
					"check_response_timeout_minutes": queueCheckTimeoutMinutes, "grouping_strategy": "ALLGREEN", "max_entries_to_build": 1,
					"max_entries_to_merge": 1, "merge_method": "MERGE", "min_entries_to_merge": 1, "min_entries_to_merge_wait_minutes": 0,
				}},
			}, BypassActors: []map[string]any{},
		},
	}
	if withCheck {
		r4 := &rs[3]
		r4.Rules = append(r4.Rules, map[string]any{"type": "required_status_checks", "parameters": map[string]any{
			"required_status_checks": []map[string]any{{"context": RequiredCheck}}, "strict_required_status_checks_policy": false,
		}})
	}
	return rs
}

// ApplyRulesets creates R1–R4, or updates the ones that exist by name;
// R4 gets its required check once workflowPath is on the default branch,
// and never while this release does not ship queue/runner (D884): the
// queue/poll and queue/watch modes run nothing on GitHub, so a required
// check would hold every merge (D878). It returns what it did, one line
// each.
func (c *Client) ApplyRulesets(ctx context.Context, token, repo string, pusherAppID int64, workflowPath string) ([]string, error) {
	var done []string
	withCheck := true
	if apexv1.RunnerModeAvailable() != nil {
		withCheck = false
		done = append(done, "R4 without a required check: this release's queue modes (queue/poll, queue/watch) run nothing on GitHub, so main's queue merges with no check of ape's (D878, D884)")
	} else {
		err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/contents/"+workflowPath, token, nil, nil)
		var ae *APIError
		switch {
		case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
			withCheck = false
			done = append(done, "R4 without its required check "+RequiredCheck+": the queue workflow is not on the default branch yet — merge its PR (setup act workflow), then run rulesets again")
		case err != nil:
			return nil, err
		}
	}
	var have []Ruleset
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/rulesets?includes_parents=false", token, nil, &have); err != nil {
		return nil, err
	}
	byName := map[string]int64{}
	for _, r := range have {
		byName[r.Name] = r.ID
	}
	for _, r := range Rulesets(pusherAppID, withCheck) {
		if id, ok := byName[r.Name]; ok {
			if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/rulesets/%d", repo, id), token, r, nil); err != nil {
				return done, fmt.Errorf("update %q: %w", r.Name, err)
			}
			done = append(done, "updated "+r.Name)
			continue
		}
		if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/rulesets", token, r, nil); err != nil {
			return done, fmt.Errorf("create %q: %w", r.Name, err)
		}
		done = append(done, "created "+r.Name)
	}
	return done, nil
}

// SetSecret sets one Actions secret, sealed with the repository's public
// key as GitHub requires; the value is never logged.
func (c *Client) SetSecret(ctx context.Context, token, repo, name, value string) error {
	var pk struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/actions/secrets/public-key", token, nil, &pk); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(pk.Key)
	if err != nil || len(raw) != 32 {
		return errors.New("ghapp: the repository's public key is not a 32-byte key")
	}
	var recipient [32]byte
	copy(recipient[:], raw)
	sealed, err := box.SealAnonymous(nil, []byte(value), &recipient, rand.Reader)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/repos/"+repo+"/actions/secrets/"+url.PathEscape(name), token,
		map[string]string{"encrypted_value": base64.StdEncoding.EncodeToString(sealed), "key_id": pk.KeyID}, nil)
}

// SecretNames are the only secrets the service sets.
var SecretNames = map[string]bool{"APE_NATS_URL": true, "APE_NATS_CREDS": true}

// RunnerToken asks for a self-hosted runner registration token (one hour).
func (c *Client) RunnerToken(ctx context.Context, token, repo string) (Token, error) {
	var t Token
	err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/actions/runners/registration-token", token, nil, &t)
	return t, err
}

// WorkflowBranch is the branch the workflow PR comes from.
const WorkflowBranch = "apex/setup-workflow"

// WorkflowPR opens a PR adding path=content to the default branch, from
// WorkflowBranch cut at the default branch's tip; it returns the PR's URL.
// An open PR from that branch is returned as it is.
func (c *Client) WorkflowPR(ctx context.Context, token, repo, path, content string) (string, error) {
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo, token, nil, &info); err != nil {
		return "", err
	}
	owner, _, _ := strings.Cut(repo, "/")
	var open []struct {
		URL string `json:"html_url"`
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + WorkflowBranch}}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), token, nil, &open); err != nil {
		return "", err
	}
	if len(open) > 0 {
		return open[0].URL, nil
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/git/ref/heads/"+info.DefaultBranch, token, nil, &ref); err != nil {
		return "", err
	}
	err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/git/refs", token, map[string]string{"ref": "refs/heads/" + WorkflowBranch, "sha": ref.Object.SHA}, nil)
	var ae *APIError
	if err != nil && (!errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity) {
		return "", err
	}
	if err := c.do(ctx, http.MethodPut, "/repos/"+repo+"/contents/"+path, token, map[string]string{
		"message": "ci: the merge queue's workflow (written by ape)", "content": base64.StdEncoding.EncodeToString([]byte(content)), "branch": WorkflowBranch,
	}, nil); err != nil {
		return "", err
	}
	var pr struct {
		URL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/pulls", token, map[string]any{
		"head": WorkflowBranch, "base": info.DefaultBranch, "title": "ci: the merge queue's workflow",
		"body": "Written by ape's setup service (APEX v1.5, PLAN-V15-GHAPP): the job `" + RequiredCheck + "` is main's required check.",
	}, &pr); err != nil {
		return "", err
	}
	return pr.URL, nil
}
