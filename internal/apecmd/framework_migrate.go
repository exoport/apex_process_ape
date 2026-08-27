package apecmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
	"github.com/exoport/apex_process_ape/internal/deferred"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// Project-data migrations run as part of `ape framework update` rather
// than as a separate command a skill has to police, so no skill ever meets
// an un-migrated project and no skill needs a migration failure path.
//
// This is the right transaction boundary: explicitly invoked by the
// operator, at the moment framework expectations change, outside the build
// loop. The alternative — a skill failing mid-review — puts the failure at
// the worst possible time.

// migrationStatus is one pending or completed project-data migration.
type migrationStatus struct {
	Name    string `json:"name"             yaml:"name"`
	Pending bool   `json:"pending"          yaml:"pending"`
	Detail  string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// pendingMigrations reports what a project still owes, detected from disk
// state alone. No version marker is stored anywhere, so there is nothing
// that can drift out of sync with reality.
func pendingMigrations(projectRoot string) []migrationStatus {
	cfg, err := apexcfg.ResolveAt(projectRoot, nil)
	if err != nil {
		return nil
	}
	return []migrationStatus{deferredMigrationStatus(cfg)}
}

func deferredMigrationStatus(cfg *apexcfg.Resolved) migrationStatus {
	st := migrationStatus{Name: "deferred"}
	legacy := cfg.Paths.DeferredLegacy
	if legacy == "" {
		st.Detail = apexcfg.MsgImplementationFolderUnset
		return st
	}
	info, err := os.Stat(legacy)
	if err != nil {
		st.Detail = "no legacy deferred-work.md — nothing to migrate"
		return st
	}
	store := deferred.New(cfg.Paths.Deferred)
	loaded, loadErr := store.Load(deferred.LoadOptions{})
	migrated := loadErr == nil && len(loaded.Records) > 0
	if migrated {
		st.Detail = fmt.Sprintf("%d record(s) already in %s", len(loaded.Records), cfg.Paths.Deferred)
		return st
	}
	st.Pending = true
	st.Detail = fmt.Sprintf("%s is %d bytes and not yet converted to record files",
		legacy, info.Size())
	return st
}

// runProjectMigrations performs every pending project-data migration.
//
// It commits nothing. `ape framework update` has never written a commit
// and this keeps it that way: the whole result sits in the working tree
// for one `git diff`, and the operator groups it into however many commits
// they want. The run prints the paths and the `git add` line instead.
func runProjectMigrations(ctx context.Context, w io.Writer, projectRoot string) error {
	cfg, err := apexcfg.ResolveAt(projectRoot, nil)
	if err != nil {
		// A project with no resolvable config has no project data to
		// migrate. The framework install already succeeded; this is not a
		// reason to fail it.
		fmt.Fprintf(w, "migration: skipped (%v)\n", err)
		return nil
	}
	st := deferredMigrationStatus(cfg)
	if !st.Pending {
		fmt.Fprintf(w, "migration: nothing pending (%s)\n", st.Detail)
		return nil
	}

	// The clean gate is scoped to the paths the migration writes, NOT the
	// whole tree. Those paths are disjoint from what the framework install
	// touched (.claude/skills, _apex/*, CLAUDE.md), which is what makes the
	// install and the migration order-independent — there is no "capture
	// the clean state before installing" constraint to get wrong.
	paths := migrationPaths(cfg.Paths.Deferred, cfg.Paths.DeferredLegacy)
	if dirty := dirtyPaths(ctx, cfg.Root, paths); len(dirty) > 0 {
		fmt.Fprintf(w,
			"migration: SKIPPED — these paths have uncommitted changes, so `git status` could not\n"+
				"           tell ape's work from yours: %v\n"+
				"           Commit or stash them, then run `ape deferred migrate`.\n", dirty)
		return nil
	}

	store := deferred.New(cfg.Paths.Deferred)
	// No DryRun here: `--dry-run` is answered by emitFrameworkDryRun, which
	// reports the framework drift alongside the pending migrations. This
	// path only ever performs one.
	res, err := store.Migrate(ctx, deferred.MigrateOptions{From: cfg.Paths.DeferredLegacy})
	if err != nil {
		return err
	}
	emitMigrateHuman(w, res, cfg.Root)
	return nil
}

// emitFrameworkDryRun reports what an update would do without writing:
// the framework drift and the pending project-data migrations.
//
// A framework side that cannot be read (no metadata, no repo configured)
// is reported and stepped over rather than fatal: the migration half is
// often the reason someone ran --dry-run, and it is knowable either way.
func emitFrameworkDryRun(ctx context.Context, w io.Writer, repo, projectRoot string) error {
	res, err := framework.Status(ctx, framework.StatusOptions{
		ProjectRoot:   projectRoot,
		FrameworkRepo: repo,
	})
	if err != nil {
		fmt.Fprintf(w, "framework: cannot compare — %v\n", err)
	} else {
		emitFrameworkDrift(w, res)
	}
	for _, st := range pendingMigrations(projectRoot) {
		state := "nothing pending"
		if st.Pending {
			state = "PENDING"
		}
		fmt.Fprintf(w, "migration %s: %s — %s\n", st.Name, state, st.Detail)
	}
	if runlog.Pending(projectRoot) {
		root := runlog.ApeRoot(projectRoot)
		if rel, rerr := filepath.Rel(projectRoot, root); rerr == nil {
			root = rel
		}
		fmt.Fprintf(w, "run layout: PENDING — run artifacts still at the legacy _output paths; "+
			"an update relocates them into %s/\n", root)
	} else {
		fmt.Fprintln(w, "run layout: nothing pending — run artifacts are under the current output folder")
	}
	fmt.Fprintln(w, "\n--dry-run: nothing written, nothing committed.")
	return nil
}

func emitFrameworkDrift(w io.Writer, res *framework.StatusResult) {
	fmt.Fprintf(w, "installed: %s @ %s (%s)\n",
		defaultStr(res.Installed.Framework.RepoOrigin, "(no origin)"),
		defaultStr(res.Installed.Framework.VersionTag, "(no tag)"),
		short(res.Installed.Framework.GitHash))
	if res.Current != nil {
		fmt.Fprintf(w, "current:   %s (%s)\n",
			defaultStr(res.Current.VersionTag, "(no tag)"), short(res.Current.GitHash))
	}
	if res.Drift != nil && (res.Drift.HashDrift || res.Drift.TagDrift) {
		fmt.Fprintln(w, "framework: would update —")
		for _, n := range res.Drift.Notes {
			fmt.Fprintln(w, "  - "+n)
		}
		return
	}
	fmt.Fprintln(w, "framework: in sync")
}
