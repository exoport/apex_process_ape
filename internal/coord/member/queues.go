package member

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/github"
)

// QueueFindings is project check's reading of the coordinated
// repository's merge queues (v1.5 stage 1a; coord.queue-settings): main
// and every milestone/<M> branch on main's remote need a queue that
// merges with a merge commit — main's close PR is the release point
// (OQ17), and a take-in merges main into a milestone, never squashed
// (section 5.2). Nothing for a project not integrating through GitHub. A
// queue that cannot be read (network, rate limit) is written to note and
// is no finding: project check judges settings, not the network.
func QueueFindings(ctx context.Context, p *apexv1.Project, note io.Writer) []apexv1.Finding {
	m, err := Load(p)
	if err != nil || m.GitHubRepo == "" {
		return nil
	}
	tok, err := m.ReadGitHubToken()
	if err != nil {
		return nil
	}
	c := github.New(m.GitHubAPI, m.GitHubRepo, tok)
	branches := append([]string{"main"}, remoteMilestones(ctx, p.Root)...)
	var out []apexv1.Finding
	for _, b := range branches {
		method, err := c.QueueMergeMethod(ctx, b)
		switch {
		case errors.Is(err, github.ErrNoQueue):
			out = append(out, apexv1.Finding{File: "_apex/config.yaml", Message: fmt.Sprintf("merge queue: %s on %s has no merge queue — ape's pull requests merge only through one; a person adds it in the repository's rulesets, merging with a merge commit", b, m.GitHubRepo)})
		case err != nil:
			fmt.Fprintf(note, "note: the merge queue of %s on %s could not be read (%v); not judged\n", b, m.GitHubRepo, err)
		case method != "MERGE":
			out = append(out, apexv1.Finding{File: "_apex/config.yaml", Message: fmt.Sprintf("merge queue: %s on %s merges by %s — it must merge with a merge commit (%s); a person changes the queue's merge method", b, m.GitHubRepo, strings.ToLower(method), queueWhy(b))})
		}
	}
	return out
}

func queueWhy(branch string) string {
	if branch == "main" {
		return "the close PR's merge commit is the release point, OQ17"
	}
	return "a take-in merges main into the milestone and must not be squashed, section 5.2"
}

// remoteMilestones is every milestone/<M> branch main's remote has, as
// last fetched.
func remoteMilestones(ctx context.Context, root string) []string {
	remote := "origin"
	if out, err := exec.CommandContext(ctx, "git", "-C", root, "config", "branch.main.remote").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		remote = strings.TrimSpace(string(out))
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "for-each-ref", "--format=%(refname:short)", "refs/remotes/"+remote+"/"+apexv1.MilestoneBranchPrefix).Output()
	if err != nil {
		return nil
	}
	var bs []string
	for l := range strings.FieldsSeq(string(out)) {
		bs = append(bs, strings.TrimPrefix(l, remote+"/"))
	}
	return bs
}
