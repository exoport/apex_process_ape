package apexv1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The archive's squash commit (H15). Projects rely on their commits: a
// changelog generated from conventional-commit types, pre-commit hooks, and
// trailers binding artifacts to the change that moved them. _apex/config.yaml
//
//	commit:
//	  subject: "{type}: {title}"   # {type}, {title}, {id}; default "{title}"
//	  hooks: run                   # run (default) | skip
//
// shapes the squash: its subject from the template, the project's git hooks
// run on it (only on it: ape's own status commits keep --no-verify), and
// its body carrying the branch commits' own subjects and trailers.

// CommitTypes are the values change.md's `type:` may take (D47).
var CommitTypes = []string{"feat", "fix", "perf", "refactor", "docs", "test", "build", "ci", "chore"}

// DefaultSubject is the squash subject when commit.subject is not set:
// the change's title.
const DefaultSubject = "{title}"

// CommitConfig is _apex/config.yaml's commit block.
type CommitConfig struct {
	Subject string `yaml:"subject"`
	// Hooks is "run" (the default, also when absent) or "skip".
	Hooks string `yaml:"hooks"`
}

// UnmarshalYAML validates the block as it parses.
func (c *CommitConfig) UnmarshalYAML(n *yaml.Node) error {
	type plain CommitConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	p.Hooks = strings.TrimSpace(p.Hooks)
	switch p.Hooks {
	case "", "run", "skip":
	default:
		return fmt.Errorf("commit.hooks: %q is not run or skip", p.Hooks)
	}
	if strings.Contains(p.Subject, "\n") {
		return errors.New("commit.subject: must be one line")
	}
	rest := strings.NewReplacer("{type}", "", "{title}", "", "{id}", "").Replace(p.Subject)
	if strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("commit.subject: %q holds a placeholder other than {type}, {title}, {id}", p.Subject)
	}
	*c = CommitConfig(p)
	return nil
}

// SubjectTemplate is commit.subject, else DefaultSubject.
func (c CommitConfig) SubjectTemplate() string {
	if strings.TrimSpace(c.Subject) == "" {
		return DefaultSubject
	}
	return c.Subject
}

// RunHooks reports whether the squash runs the project's git hooks.
func (c CommitConfig) RunHooks() bool { return c.Hooks != "skip" }

// commitConfig is the project's commit block; the defaults when the config
// does not parse (Open has refused that already).
func (p *Project) commitConfig() CommitConfig {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return CommitConfig{}
	}
	return cfg.Commit
}

// typeFindings checks change.md's `type:`: one of CommitTypes when set,
// and set when the subject template uses {type}.
func typeFindings(file string, c *Change, tmpl string) []Finding {
	t := strings.TrimSpace(c.Meta.Type)
	switch {
	case t == "" && strings.Contains(tmpl, "{type}"):
		return []Finding{{File: file, Line: 1, Message: "frontmatter has no `type:`, and _apex/config.yaml's commit.subject " + fmt.Sprintf("%q", tmpl) +
			" uses {type} — add `type: <" + strings.Join(CommitTypes, "|") + ">`"}}
	case t != "" && !slices.Contains(CommitTypes, t):
		return []Finding{{File: file, Line: 1, Message: "frontmatter type \"" + t + "\" is not one of " + strings.Join(CommitTypes, ", ") + " — pick the one that fits"}}
	}
	return nil
}

// archiveSubject renders the squash commit's subject from the template.
func archiveSubject(tmpl string, c *Change, id string) string {
	title := strings.TrimSpace(c.Title)
	if title == "" {
		title = titleFromSlug(id)
	}
	return strings.TrimSpace(strings.NewReplacer("{type}", strings.TrimSpace(c.Meta.Type), "{title}", title, "{id}", id).Replace(tmpl))
}

// squashMessage is the squash commit's message: the subject; the branch
// commits' own subjects (not ape's `apex:` ones), deduplicated, oldest
// first; then their trailers other than `Change:`, deduplicated, and
// `Change: <id>` last.
func (p *Project) squashMessage(ctx context.Context, subject, base, branch, id string) string {
	log, _ := git(ctx, p.Root, "log", "--reverse", "--format=%s%x1f%(trailers:only,unfold)%x1e", "refs/heads/"+base+"..refs/heads/"+branch)
	var subjects, trailers []string
	seen := map[string]bool{}
	for rec := range strings.SplitSeq(log, "\x1e") {
		s, tr, _ := strings.Cut(strings.TrimLeft(rec, "\n"), "\x1f")
		if s = strings.TrimSpace(s); s != "" && !strings.HasPrefix(s, "apex:") && !seen["s:"+s] {
			seen["s:"+s] = true
			subjects = append(subjects, "- "+s)
		}
		for line := range strings.SplitSeq(tr, "\n") {
			line = strings.TrimSpace(line)
			key, _, ok := strings.Cut(line, ":")
			if !ok || strings.EqualFold(strings.TrimSpace(key), "Change") || seen["t:"+line] {
				continue
			}
			seen["t:"+line] = true
			trailers = append(trailers, line)
		}
	}
	msg := subject + "\n\n"
	if len(subjects) > 0 {
		msg += strings.Join(subjects, "\n") + "\n\n"
	}
	return msg + strings.Join(append(trailers, "Change: "+id), "\n") + "\n"
}

// hasCommitHooks reports a hook git runs on a commit — pre-commit,
// prepare-commit-msg or commit-msg — present and executable (core.hooksPath
// honoured).
func (p *Project) hasCommitHooks(ctx context.Context) bool {
	for _, h := range []string{"pre-commit", "prepare-commit-msg", "commit-msg"} {
		path, err := git(ctx, p.Root, "rev-parse", "--git-path", "hooks/"+h)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Root, path)
		}
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// commitSquash commits the staged squash with msg — with the project's
// hooks unless skip — and returns git's combined output on failure, which
// is where a hook says why it refused.
func (p *Project) commitSquash(ctx context.Context, msg string, hooks bool) (string, error) {
	args := []string{"commit", "-q", "-m", msg}
	if !hooks {
		args = []string{"commit", "--no-verify", "-q", "-m", msg}
	}
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = p.Root
	out, err := c.CombinedOutput()
	return string(out), err
}
