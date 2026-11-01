package member

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
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
	tok, missing, err := m.gitHubToken(ctx, "read")
	if err != nil {
		fmt.Fprintf(note, "note: no GitHub token to read %s's merge queue (%v); not judged\n", m.GitHubRepo, err)
		return nil
	}
	if slices.Contains(missing, "merge_queues: read") {
		fmt.Fprintln(note, queueBlind(m.GitHubRepo, "the App service minted the read token without it"))
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
		case err != nil && strings.Contains(err.Error(), "Resource not accessible by integration"):
			fmt.Fprintln(note, queueBlind(m.GitHubRepo, "GitHub: "+err.Error()))
		case err != nil:
			fmt.Fprintf(note, "note: the merge queue of %s on %s could not be read (%v); not judged\n", b, m.GitHubRepo, err)
		case method != "MERGE":
			out = append(out, apexv1.Finding{File: "_apex/config.yaml", Message: fmt.Sprintf("merge queue: %s on %s merges by %s — it must merge with a merge commit (%s); a person changes the queue's merge method", b, m.GitHubRepo, strings.ToLower(method), queueWhy(b))})
		}
	}
	return out
}

// QueueBlindRoute is how a person gives the apex-pusher App the
// permission main's queue configuration needs (FINDING-V15-1A-7 c).
const QueueBlindRoute = "on GitHub, the apex-pusher App's settings (Settings → Developer settings → GitHub Apps → the App → Edit) → Permissions & events → Repository permissions → Merge queues: Read-only → Save changes; then the installation's owner accepts it (Settings → GitHub Apps → the App → Configure → Review request → Accept new permissions); the App service's next token carries it"

// queueBlind is the loud note when main's queue configuration cannot be
// read for want of the Merge queues permission.
func queueBlind(repo, why string) string {
	return "warning: main's merge-queue settings on " + repo + " are NOT judged: the App's token lacks Merge queues: read (" + why + ") — " + QueueBlindRoute
}

func queueWhy(string) string {
	return "the close PR's merge commit is the release point, OQ17"
}
