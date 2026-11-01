package member

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/github"
)

// QueueFindings is project check's reading of the coordinated
// repository's merge queue (v1.5 stage 1a; coord.queue-settings): main
// needs one that merges with a merge commit — the close PR's merge commit
// is the release point (OQ17). Milestone branches have none: ape lands on
// them itself (D702). Nothing for a project not integrating through GitHub. A
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
	// D702: the merge queue is main's only; milestone branches are ape's.
	branches := []string{"main"}
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

func queueWhy(string) string {
	return "the close PR's merge commit is the release point, OQ17"
}
