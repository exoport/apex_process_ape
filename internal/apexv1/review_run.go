package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvReview marks a review session started by `ape change review --run`
// (trial decision D18): its value is the change id. The edit hook then
// confines edits inside the project to that change's review.md, and a
// review session may not start another review (depth 1).
const EnvReview = "APE_REVIEW"

// ReviewRun is ape's record of one review session it started:
// _apex/state/logs/<id>/review-run.json (gitignored).
type ReviewRun struct {
	Change     string `json:"change"`
	DetachedID string `json:"detached_id"`
	StartedAt  string `json:"started_at"`
	// HeadSHA is HEAD when the session started: the post-run assertion
	// judges every commit after it.
	HeadSHA string `json:"head_sha"`
	// Parent is the APE_SESSION of the session that asked for the review,
	// "" from a plain shell.
	Parent string       `json:"parent,omitempty"`
	Launch ReviewLaunch `json:"launch"`
}

// ReviewRunPath is where a change's review-session record lives.
func (p *Project) ReviewRunPath(id string) string {
	return filepath.Join(p.LogDir(id), "review-run.json")
}

// LoadReviewRun reads the record; nil when there is none.
func (p *Project) LoadReviewRun(id string) *ReviewRun {
	data, err := os.ReadFile(p.ReviewRunPath(id))
	if err != nil {
		return nil
	}
	var r ReviewRun
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return &r
}

// SaveReviewRun writes the record.
func (p *Project) SaveReviewRun(r *ReviewRun) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p.ensureState()
	return writeFileAtomic(p.ReviewRunPath(r.Change), string(data)+"\n")
}

// ClearReviewRun removes the record once its outcome is settled.
func (p *Project) ClearReviewRun(id string) {
	_ = os.Remove(p.ReviewRunPath(id))
}

// PrepareReviewRun checks a change may be reviewed in a fresh session: it
// is verified, its verify is green and still covers the code, and its plan
// has not drifted. Returns the change.
func (p *Project) PrepareReviewRun(ctx context.Context, id string) (ChangeInfo, error) {
	if os.Getenv(EnvReview) != "" {
		return ChangeInfo{}, &UsageError{Code: "review.nested", Msg: "this is a review session (" + EnvReview + "=" + os.Getenv(EnvReview) +
			"): a review session cannot start another review"}
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return ci, err
	}
	st := ci.State
	if st.Status == StatusReviewed {
		if stale, files := p.reviewStale(ctx, st); stale {
			return ci, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is reviewed and its review is stale (code changed since: " + strings.Join(limit(files, 5), ", ") +
				") — `ape change review " + id + " --run` re-verifies it first, then reviews it again"}
		}
		return ci, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is reviewed and its review is fresh (no code changed since it) — nothing to review again; `ape change archive " + id + "`"}
	}
	if st.Status != StatusVerified {
		return ci, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + st.Status + " — a review session reviews a verified change (`ape change verify " + id + "`)"}
	}
	// The session runs in this checkout and records its verdict there:
	// on the change's branch (D38).
	if err := p.requireOnBranch(ctx, id); err != nil {
		return ci, err
	}
	if err := checkPlanFrozen(ci); err != nil {
		return ci, err
	}
	if st.Verify == nil || !st.Verify.Green {
		return ci, &FailError{Code: "verify.missing", Msg: "no green verify to review — `ape change verify " + id + "`"}
	}
	if moved, files, _ := p.codeMovedSince(ctx, st.Verify.SHA); moved {
		return ci, &FailError{Code: "verify.stale", Msg: "code changed since the last verify (" + strings.Join(limit(files, 5), ", ") + ") — `ape change verify " + id + "` first"}
	}
	return ci, nil
}

// reviewStale reports whether code moved since st's review — the
// archive's review.stale — with the files that moved. A reviewed change
// with no review recorded is stale.
func (p *Project) reviewStale(ctx context.Context, st *State) (stale bool, files []string) {
	if st.Review == nil {
		return true, nil
	}
	stale, files, _ = p.codeMovedSince(ctx, st.Review.SHA)
	return stale, files
}

// ReverifyStaleReview is `ape change review --run`'s first step (D51
// Q16): a reviewed change whose review is stale — code moved since its
// SHA, as the archive's review.stale judges it — is verified again first,
// in-process as `ape change verify` would (a green one makes it verified,
// so the review session reviews it as any verified change). A red verify
// returns verify.red (or the attempts limit's block) beside the result.
// Any other change is left alone: nil, nil.
func (p *Project) ReverifyStaleReview(ctx context.Context, id string, opt RunOptions) (*VerifyResult, error) {
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if ci.State.Status != StatusReviewed {
		return nil, nil //nolint:nilnil // not reviewed: nothing to verify, the documented nil
	}
	if stale, _ := p.reviewStale(ctx, ci.State); !stale {
		return nil, nil //nolint:nilnil // a fresh review: nothing to verify, the documented nil
	}
	res, err := p.Verify(ctx, id, opt)
	if err != nil || res.Green {
		return res, err
	}
	return res, &FailError{Code: "verify.red", Msg: fmt.Sprintf("change %s: its review was stale and the verify before reviewing it again is red: %d command check(s) red, attempt %d of %d — fix the code (never the check) and verify again, or `ape change block %s --reason ...`",
		id, len(res.Failing()), res.Attempts, res.AttemptsCap, id)}
}

// ReviewOutcome is what a finished review session left behind.
type ReviewOutcome struct {
	Verdict    string   `json:"verdict,omitempty"`
	Findings   string   `json:"findings,omitempty"`
	Violations []string `json:"violations,omitempty"`
	Voided     bool     `json:"voided,omitempty"`
	Status     string   `json:"status"`
}

// reviewMayTouch reports whether a review session may change path
// (project-relative): its review.md, the state ape commits for it, and
// ape's own records.
func (p *Project) reviewMayTouch(id, rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	changeDir := p.DevRel + "/changes/" + id + "/"
	return rel == changeDir+"review.md" || rel == changeDir+"state.json" ||
		strings.HasPrefix(rel, p.StateRel()+"/")
}

// SettleReviewRun is the post-run assertion. A review session may leave
// exactly two traces: review.md (committed or not) and the state ape
// commits when it records the verdict. Anything else — a commit touching
// another path, or another dirty file — voids the verdict: the review is
// read-only by contract, and a verdict from a session that changed the
// code under review judges code nobody verified. Bash-tool writes cannot
// be prevented; this is where they are caught.
func (p *Project) SettleReviewRun(ctx context.Context, run *ReviewRun) (*ReviewOutcome, error) {
	id := run.Change
	var violations []string
	// A session that left HEAD on another branch changed more than its
	// review.md; the verdict lives on the change's branch (D38).
	if cur := CurrentBranch(ctx, p.Root); p.isWorkTree(ctx) && cur != ChangeBranch(id) {
		violations = append(violations, "switched HEAD to "+branchName(cur))
		if err := p.switchBranch(ctx, ChangeBranch(id)); err != nil {
			return nil, err
		}
	}
	if run.HeadSHA != "" {
		out, err := git(ctx, p.Root, "diff", "--name-only", run.HeadSHA, "HEAD")
		if err != nil {
			return nil, err
		}
		for f := range strings.SplitSeq(out, "\n") {
			if f != "" && !p.reviewMayTouch(id, f) {
				violations = append(violations, "committed "+f)
			}
		}
	}
	status, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	for line := range strings.SplitSeq(status, "\n") {
		if len(line) <= 3 {
			continue
		}
		path := strings.Trim(line[3:], "\"")
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		if !p.reviewMayTouch(id, path) {
			violations = append(violations, "left "+path)
		}
	}

	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	st := ci.State
	started, _ := time.Parse(time.RFC3339, run.StartedAt)
	recorded := st.Review != nil
	if recorded {
		if at, err := time.Parse(time.RFC3339, st.Review.At); err != nil || at.Before(started) {
			recorded = false
		}
	}
	out := &ReviewOutcome{Violations: violations, Status: st.Status}
	if recorded {
		out.Verdict, out.Findings = st.Review.Verdict, st.Review.Findings
	}
	if len(violations) == 0 {
		return out, nil
	}
	// Void the verdict, whatever it was.
	st.ReviewVoided = append(st.ReviewVoided, ReviewVoid{
		At: Stamp(), Run: run.DetachedID, Verdict: out.Verdict, Violations: violations,
	})
	st.Review = nil
	st.Status = StatusVerified
	if st.Verify == nil || !st.Verify.Green {
		st.Status = StatusInProgress
	} else if moved, _, _ := p.codeMovedSince(ctx, st.Verify.SHA); moved {
		st.Status = StatusInProgress
	}
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	out.Voided = true
	out.Status = st.Status
	return out, nil
}

// ErrNoVerdict: the review session ended without recording one.
var ErrNoVerdict = errors.New("the review session ended without a verdict")

// ReviewSkill is the skill a review session runs.
const ReviewSkill = "apex-review"

// ReviewLaunch is a review session's Launch (D22); the review's run
// manifest records it as review_launch.
type ReviewLaunch = Launch

// DecideReviewLaunch is DecideLaunch for a review session with no
// top-level config effort: the flag, models.apex-review, the asking
// session, the default.
func DecideReviewLaunch(flagModel, flagEffort string, cfg SkillModel, parentModel, parentEffort string) ReviewLaunch {
	return DecideLaunch(flagModel, flagEffort, cfg, "", parentModel, parentEffort)
}
