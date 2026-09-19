package apecmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/change"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// changePreflight is everything that must hold before a claude process
// exists. Every failure here is exit 2, and none of them costs a model
// minute to discover.
//
// The four conditions are not hygiene. Each one is a state in which ape
// could not safely hold the pen afterwards:
//
//   - a dirty tree makes the reconciliation meaningless. ape decides what
//     to commit by comparing the contract's claimed paths against what
//     actually changed, and edits that were already there are
//     indistinguishable from the dispatch's;
//   - a detached HEAD gives the commits no branch to land on;
//   - an unignored {output_folder}/ape puts ape's own run artifacts into
//     that same changed set, so every run would refuse itself;
//   - no evidence_folder leaves the goals' evidence with nowhere to go.
func changePreflight(ctx context.Context, cfg *apexcfg.Resolved) error {
	apeRoot := runlog.ApeRoot(cfg.Root)
	rel, err := filepath.Rel(cfg.Root, apeRoot)
	if err != nil {
		return usageErr(fmt.Errorf("resolve %s: %w", apeRoot, err))
	}
	rel = filepath.ToSlash(rel)

	// The trailing slash is the query, not the path. Under a
	// directory-only ignore rule (`_output/ape/` — the line `ape doctor`
	// itself tells projects to add) git answers the bare path "not
	// ignored" until the directory exists, and under a contents rule
	// (`_output/ape/*`) it answers "not ignored" even when it does. Both
	// would refuse a project whose ignore file is exactly right.
	switch gitIgnores(ctx, cfg.Root, rel+"/") {
	case ignoreYes:
		// The condition holds.
	case ignoreNotARepo:
		return usageErr(errors.New("not a git repository: ape composes this change's commits, " +
			"so there has to be somewhere to make them"))
	default:
		return usageErr(fmt.Errorf("%s is not ignored by git: ape writes every manifest, runlog "+
			"and residue there, and an unignored copy would join the changed set this run "+
			"reconciles against — every change would then refuse itself. Add `%s/` to .gitignore",
			rel, rel))
	}

	detached, err := change.DetachedHEAD(ctx, cfg.Root)
	if err != nil {
		return usageErr(err)
	}
	if detached {
		return usageErr(errors.New("HEAD is detached: ape is about to make several commits, and " +
			"on a detached HEAD they would belong to no branch. Check out a branch first"))
	}

	dirty, err := change.Changed(ctx, cfg.Root)
	if err != nil {
		return usageErr(err)
	}
	if len(dirty) > 0 {
		return usageErr(fmt.Errorf("the working tree is not clean, so ape could not tell this "+
			"change's edits from the ones already here:\n  %s",
			strings.Join(dirty, "\n  ")))
	}

	if strings.TrimSpace(cfg.EvidenceFolder) == "" {
		return usageErr(errors.New("evidence_folder is not set in _apex/config.yaml: every goal " +
			"commits its evidence, and ape does not guess the folder the framework resolves. " +
			"Run `ape config pin evidence_folder` to write the resolved value into the config"))
	}
	return nil
}
