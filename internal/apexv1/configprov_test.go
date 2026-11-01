package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// mergeWithBody merges branch into main with a merge commit as GitHub's
// pull request merge makes it: the subject names the PR, the body repeats
// its title.
func mergeWithBody(t *testing.T, p *Project, branch, title string) {
	t.Helper()
	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "merge", "-q", "--no-ff", "-m", "Merge pull request #2 from shop/"+branch+"\n\n"+title, branch)
}

// 1.4.4: an acceptance landed through a pull request arrives with a merge
// commit whose body repeats the PR's title, "apex: config accepted by …".
// The guard takes the commit whose subject is the acceptance, not the
// newest whose message mentions it — that merge — so the change it
// accepted is no finding.
func TestConfigAcceptance_ThroughAPullRequestMerge(t *testing.T) {
	p := fixture(t, baseFiles())
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "_apex/config.yaml", "human_review: {}\n")
	commitAll(t, p, "chore: let a skill accept")
	require.Contains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys (human_review)")

	run(t, p.Root, "switch", "-q", "-c", "apex/accept-config")
	_, err := p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	mergeWithBody(t, p, "apex/accept-config", "apex: config accepted by ann")
	require.NotContains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys", "the acceptance in the merged branch counts")
}

// The control: a merge whose body names an acceptance that no commit
// makes accepts nothing.
func TestConfigAcceptance_MergeBodyAloneAcceptsNothing(t *testing.T) {
	p := fixture(t, baseFiles())
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	write(t, p, "_apex/config.yaml", "human_review: {}\n")
	commitAll(t, p, "chore: let a skill accept")

	run(t, p.Root, "switch", "-q", "-c", "topic")
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "docs: a note")
	mergeWithBody(t, p, "topic", "apex: config accepted by ann")
	require.Contains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys (human_review)", "a merge body is no acceptance")
}
