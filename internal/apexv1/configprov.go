package apexv1

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// D50 (rc.2): config.changed. _apex/config.yaml is writable by an Edit, and
// some of its blocks loosen what ape enforces: limits (H19), checks and
// requires (H11), retries (H12), timeouts (H10), caps (D69), regression
// (backlog 32: unset, the net is off), commit (hooks: skip skips the
// project's hooks on the squash), evidence (off keeps no evidence), and
// marketplaces, plugins and tools (what runs inside a session: hooks, MCP
// servers, the skills a session may start), and human_review (D75: who
// accepts a human-review task in a person's place), and secrets (D155:
// what the commit secret scan lets through). Since the v1 epoch, a commit
// that changes one of them must be ape's own (`apex: …`), or a person
// accepts it afterwards with `ape project accept-config`; else
// config.changed. A revert clears it, as for spec.edited. The guard
// compares whole blocks and is blind to direction: raising a cap removes a
// split trigger, lowering one hides work, and both are a person's call.

// configAcceptedSubject starts the commit `ape project accept-config`
// makes: every guarded change before it is a person's.
const configAcceptedSubject = "apex: config accepted"

// guardedConfigKeys are the config blocks a model must not loosen.
// A key the Config struct does not hold yet is compared all the same (a
// block absent on both sides is equal), so the list can name it first.
var guardedConfigKeys = []string{
	"limits", "checks", "retries", "requires", "timeouts", "caps", "regression",
	"commit", "evidence", "marketplaces", "plugins", "tools", "human_review", "release",
	// rc.6 (rc6-g): what the commit secret scan lets through (D155).
	"secrets",
}

func init() {
	findingRules = append(findingRules, rule("", `^guarded config keys`, "config.changed"))
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{"config.changed", 1, ActionPerson, "a commit since the v1 epoch changed a guarded block of _apex/config.yaml — limits, checks, retries, requires, timeouts, caps, regression, commit (commit.types included, D120), evidence, marketplaces, plugins, tools, human_review, release (D120) or secrets (D155) — and is not ape's (`apex: …`), and no person accepted it since (`ape project accept-config --by <who>`); a revert also clears it (D50, D69, D75, D120, D155)"},
	)
}

// configFindings walks the commits since epoch that touch the config,
// newest first: an acceptance covers everything older; a commit that is
// not ape's, not reverted, and changes a guarded block is a finding.
func (p *Project) configFindings(ctx context.Context, epoch string) []Finding {
	return p.configFindingsFor(ctx, epoch, guardedConfigKeys)
}

// configFindingsFor is configFindings over the blocks keys.
func (p *Project) configFindingsFor(ctx context.Context, epoch string, keys []string) []Finding {
	rel := configYAML
	out, err := gitRaw(ctx, p.Root, "log", "--format=%x1e%H%x1f%s%x1f%P%x1f%b", epoch+"..HEAD", "--", rel)
	if err != nil {
		return nil
	}
	type commit struct{ sha, subject, parent string }
	var commits []commit
	reverted := map[string]bool{}
	accepted := false
	for rec := range strings.SplitSeq(out, "\x1e") {
		parts := strings.SplitN(rec, "\x1f", 4)
		if len(parts) < 4 {
			continue
		}
		parent, _, _ := strings.Cut(strings.TrimSpace(parts[2]), " ")
		commits = append(commits, commit{sha: parts[0], subject: parts[1], parent: parent})
		if m := revertsRe.FindStringSubmatch(parts[3]); m != nil {
			reverted[m[1]], reverted[parts[0]] = true, true
		}
	}
	// An acceptance need not touch the config (it is an empty commit), so
	// it is looked for in the whole history since the epoch.
	acceptedAt := p.lastConfigAcceptance(ctx, epoch)
	var fs []Finding
	for _, c := range commits {
		if acceptedAt != "" && !accepted && p.isAncestor(ctx, c.sha, acceptedAt) {
			accepted = true
		}
		if accepted || reverted[c.sha] || strings.HasPrefix(c.subject, "apex:") || c.parent == "" {
			continue
		}
		changed := p.guardedConfigDiff(ctx, c.parent, c.sha, rel, keys)
		if len(changed) == 0 {
			continue
		}
		short := c.sha[:min(7, len(c.sha))]
		fs = append(fs, Finding{File: rel, Message: fmt.Sprintf("guarded config keys (%s) changed by commit %s (%q), which is not ape's"+
			" — limits, check rules and caps are a person's: a person reverts it (git revert %s), or accepts it: `ape project accept-config --by <who>`",
			strings.Join(changed, ", "), short, c.subject, short)})
	}
	return fs
}

// lastConfigAcceptance is the newest acceptance commit since epoch, "" when
// none.
func (p *Project) lastConfigAcceptance(ctx context.Context, epoch string) string {
	return p.lastConfigAcceptanceOn(ctx, epoch, "HEAD")
}

// lastConfigAcceptanceOn is lastConfigAcceptance on rev: the secret
// scan's allow baseline for a commit on rev (R23).
func (p *Project) lastConfigAcceptanceOn(ctx context.Context, epoch, rev string) string {
	out, err := git(ctx, p.Root, "log", "-1", "--format=%H", "--fixed-strings", "--grep="+configAcceptedSubject, epoch+".."+rev)
	if err != nil || out == "" {
		return ""
	}
	subject, err := git(ctx, p.Root, "log", "-1", "--format=%s", out)
	if err != nil || !strings.HasPrefix(subject, configAcceptedSubject) {
		return ""
	}
	return out
}

// guardedConfigDiff names the blocks of keys that differ between the
// config at from and at to (an absent or unparseable file reads as empty).
func (p *Project) guardedConfigDiff(ctx context.Context, from, to, rel string, keys []string) []string {
	a, b := p.configBlocksAt(ctx, from, rel), p.configBlocksAt(ctx, to, rel)
	var changed []string
	for _, k := range keys {
		if !reflect.DeepEqual(a[k], b[k]) {
			changed = append(changed, k)
		}
	}
	return changed
}

func (p *Project) configBlocksAt(ctx context.Context, rev, rel string) map[string]any {
	out := map[string]any{}
	data, err := gitRaw(ctx, p.Root, "show", rev+":"+rel)
	if err != nil {
		return out
	}
	_ = yaml.Unmarshal([]byte(data), &out)
	return out
}

// AcceptConfig is a person accepting the config as committed: an empty
// commit `apex: config accepted by <who>` on the base branch, after which
// the guarded changes before it are no longer config.changed. A person's
// act (D47 Q6), on a clean index, never on a change branch.
func (p *Project) AcceptConfig(ctx context.Context, by string) (string, error) {
	if err := requirePerson(ctx, ActAcceptConfig, "ape project accept-config"); err != nil {
		return "", err
	}
	by = strings.TrimSpace(by)
	if by == "" {
		return "", &UsageError{Code: "usage.by", Msg: "--by names the person who accepts the config"}
	}
	if err := p.refuseOnChangeBranch(ctx, "accepting the config"); err != nil {
		return "", err
	}
	if staged, err := git(ctx, p.Root, "diff", "--cached", "--name-only"); err != nil || staged != "" {
		return "", &FailError{Code: "tree.dirty", Msg: "the index holds staged changes — commit or unstage them, then accept the config"}
	}
	msg := configAcceptedSubject + " by " + by + "\n\nGuarded: " + strings.Join(guardedConfigKeys, ", ") + "\n"
	// Empty by construction (a clean index): scanned all the same, as
	// every commit ape makes is (D155).
	if err := ScanCommit(ctx, p.Root, configAcceptedSubject+" by "+by); err != nil {
		return "", err
	}
	if _, err := git(ctx, p.Root, "commit", "--allow-empty", "--no-verify", "-q", "-m", msg); err != nil {
		return "", &FailError{Code: CodeCommitFailed, Msg: "accepting the config failed: " + err.Error() + " — fix git and run it again"}
	}
	return HeadSHA(ctx, p.Root), nil
}
