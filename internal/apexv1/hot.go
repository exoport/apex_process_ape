package apexv1

import (
	"context"
	"path/filepath"
	"strings"
)

// hot/ (D155 Q77): the project's secrets, at its root, read at run time by
// the gates and checks that need one ("$(cat hot/<name>)") and never
// committed. Its .gitignore holds the one line `*`, which ignores the
// .gitignore too, so the folder stays this checkout's (the operator's
// convention). `ape project init` and the lift write it when hot/ has
// none, and never touch an existing one; `project check` reports a hot/
// git tracks anything in or does not ignore (hot.not-ignored).

// HotGitignore is the content init gives hot/.gitignore.
const HotGitignore = "*\n"

// HotDir is hot/ at the project root.
func (p *Project) HotDir() string {
	return filepath.Join(p.Root, "hot")
}

// HotGitignorePath is hot/.gitignore.
func (p *Project) HotGitignorePath() string {
	return filepath.Join(p.HotDir(), ".gitignore")
}

// hotFindings reports a hot/ git does not keep out of history: one it
// tracks a file in, or one a new file in (the probe hot/probe) would not
// be ignored in. No hot/ is no finding, and nor is a project outside a
// repository.
func (p *Project) hotFindings(ctx context.Context) []Finding {
	if !inGitRepo(ctx, p.Root) {
		return nil
	}
	hot := p.Rel(p.HotDir())
	var why []string
	if out, err := git(ctx, p.Root, "ls-files", "--", hot+"/"); err == nil && out != "" {
		files := strings.Split(out, "\n")
		why = append(why, "git tracks "+strings.Join(limit(files, 5), ", "))
	}
	if dirExists(p.HotDir()) && len(gitIgnored(ctx, p.Root, false, hot+"/probe")) == 0 {
		why = append(why, "a file in it is not ignored")
	}
	if len(why) == 0 {
		return nil
	}
	return []Finding{{File: hot + "/", Message: "hot/ is not ignored by git (" + strings.Join(why, "; ") + "), and it holds the project's secrets" +
		" — give it a hot/.gitignore of the one line `*` (`ape project init` writes it when absent) and untrack what git holds there: `git rm -r --cached " + hot + "/`"}}
}
