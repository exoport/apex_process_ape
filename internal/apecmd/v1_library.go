package apecmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
)

// The v1.1 W7 commands: a library's module fences and the release gate's
// package-contents check (apexv1/fence.go, apexv1/packagecheck.go).

func newProjectFenceCmd() *cobra.Command {
	var f v1Flags
	var check bool
	cmd := &cobra.Command{
		Use:   "fence [--check]",
		Short: "Keep APEX's folders out of a Go library's module zip: one go.mod fence per APEX folder",
		Long: `A Go module's release is a zip of the module root at the tag, and it holds every file
outside a nested module — dot and underscore folders included, and .gitattributes
export-ignore does not apply (cmd/go turns it off). So a library's .claude/, _apex/ and
development folder go to every user who downloads it (pdfa: 1,207 files in its zip, 154
without them). A folder holding a go.mod is another module, which the zip, go build, go
vet and go test ./... leave out.

For a library — _apex/config.yaml has the line ` + "`library: true`" + ` (absent: not a library;
ape project init and the lift suggest it for a Go module with no main package, and never
set it) — with a go.mod at the root, this writes one go.mod in each APEX folder that
exists:

  .claude/go.mod         _apex/go.mod         <development folder>/go.mod
  evidence/go.mod        only when the root has an evidence/ folder (a lifted
                         project's; v1 keeps evidence inside the development folder)

so 3 fences in a new v1 library and 4 in a lifted one with evidence/. Each is:

  // This folder is APEX's, not the library's: this go.mod keeps it out of the module zip.
  // See ` + apexv1.FenceDocsURL + `. Written by ape project fence.
  module apex.invalid/fence/<folder>

  go <the root go.mod's go version>

One commit, "` + apexv1.FenceSubject + `", of the fences it wrote, those whose module or go
line it restored (a fence whose go line no longer matches the root's, after a go bump,
included; every other line is kept: a gate's require and replace lines, see ape project
check --help, gate.root-module-change), and those git did not track (git add -f: the
.claude/ shape in the root .gitignore ignores .claude/go.mod, and the zip is built from
git). Idempotent: with every fence in place it writes nothing and says so (exit 0).
ape project init writes the missing fences of a library into its own commit, and the lift
into the lift commit (when the v0 config says library: true). Otherwise a person runs ape
project fence: after setting library: true, or when ape project check reports
fence.missing or fence.altered. No framework migration runs it (D395): library: is new in
v1.1, so no project is a library when it upgrades.

Nothing to fence (exit 0, nothing written): no library: true, or no go.mod at the root —
a module in a subfolder (the publish-a-library how-to's option A) needs no fence, as the
APEX folders are outside it.

Refused, nothing written: outside a git repository (git.not-a-repo), on a change branch
(branch.change-branch), while a fence it would rewrite has uncommitted changes
(tree.dirty), and for a fence that does not parse (fence.altered: fix or delete it).
fence.unreadable (exit 2): the root go.mod cannot be read or does not parse, or a fence
cannot be read. A command any session may run (not person-only).

--check writes nothing: exit 0 when every present APEX folder's fence is in place and
committed (or there is nothing to fence), 1 when one is not — fence.missing (absent, or
git does not track it) or fence.altered, a finding per fence — and 2 when the check
itself could not judge (fence.unreadable, usage). It judges the folders present, not a fixed
count. ape project check reports the same findings, only for library: true.

JSON: {"library", "go_mod", "go" (the root's go version), "fences": [{"folder", "path",
"state": ok|missing|uncommitted|altered, "detail"}], "written": [<path>], "commit"};
"fences" and "written" are always there, [] when none. Under --check, when something
is not in place (exit 1), the same keys sit at the top level beside the failure's
ok/exit/diagnostics.

Exit codes: 0 fenced, or nothing to do (--check: in place) · 1 refused, or (--check) not
in place · 2 usage, or fence.unreadable.`,
		Example: "  ape project fence --check\n  ape project fence\n  ape project fence --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			if check {
				res, err := p.FenceCheck(cmd.Context())
				if err != nil {
					return v1FailWith(cmd, &f, err, nil, fenceTop(res))
				}
				return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
					fmt.Fprintln(w, fenceNothing(res, "in place"))
				})
			}
			res, err := p.Fence(cmd.Context())
			if err != nil {
				return v1Fail(cmd, &f, err, res)
			}
			return v1Emit(cmd.OutOrStdout(), &f, res, func(w io.Writer) {
				if len(res.Written) == 0 && res.Commit == "" {
					fmt.Fprintln(w, fenceNothing(res, "in place: nothing to do"))
					return
				}
				for _, fe := range res.Fences {
					switch fe.State {
					case apexv1.FenceMissing:
						fmt.Fprintf(w, "wrote     %s\n", fe.Path)
					case apexv1.FenceAltered:
						fmt.Fprintf(w, "restored  %s (%s)\n", fe.Path, fe.Detail)
					case apexv1.FenceUncommitted:
						fmt.Fprintf(w, "committed %s (git did not track it)\n", fe.Path)
					}
				}
				fmt.Fprintf(w, "commit %.7s %s\n", res.Commit, apexv1.FenceSubject)
			})
		},
	}
	addV1Flags(cmd, &f)
	cmd.Flags().BoolVar(&check, "check", false, "write nothing: exit 0 when every present APEX folder's fence is in place, 1 when one is not, 2 when it cannot judge")
	return cmd
}

// fenceNothing is fence's line when it has nothing to write: why, or the
// fences in place.
func fenceNothing(res *apexv1.FenceResult, inPlace string) string {
	switch {
	case !res.Library:
		return "module fences: not a library (_apex/config.yaml has no library: true) — nothing to fence"
	case !res.GoMod:
		return "module fences: no go.mod at the root — nothing to fence (a module in a subfolder needs none; ape project package-check checks it)"
	}
	paths := make([]string, 0, len(res.Fences))
	for _, fe := range res.Fences {
		paths = append(paths, fe.Path)
	}
	return fmt.Sprintf("module fences: all %d %s (%s)", len(res.Fences), inPlace, strings.Join(paths, ", "))
}

// fenceTop is what --check found, at the top level of the failure's JSON.
func fenceTop(res *apexv1.FenceResult) map[string]any {
	if res == nil {
		return nil
	}
	return map[string]any{"library": res.Library, "go_mod": res.GoMod, "go": res.Go, "fences": res.Fences, "written": res.Written}
}

func newProjectPackageCheckCmd() *cobra.Command {
	var f v1Flags
	cmd := &cobra.Command{
		Use:   "package-check",
		Short: "Check a library's packed files for APEX folders (the release gate apex-release runs)",
		Long: `Pack a library as its users get it and fail on any file under an APEX folder: .claude/,
_apex/, the development folder, or evidence/ (W7, Q160). apex-release runs it before it
releases, and refuses on library.package-leak. Only for a library (_apex/config.yaml
library: true); without the key it packs nothing and passes (exit 0). It writes nothing
in the project.

  Go:     the module zip at HEAD, listed with golang.org/x/mod/zip from the files git holds
          — as a release tag ships it: commit first. The root module, or, with no go.mod at
          the root, each module in a subfolder (the publish-a-library how-to's option A),
          whose zip holds no root folder. .gitattributes export-ignore does not apply (cmd/go
          turns it off), and neither do dot or underscore names: a library fences its APEX
          folders with ape project fence
  npm:    npm pack --dry-run --json --ignore-scripts, in the project root (package.json)
  Python: python3 -m build --no-isolation on a copy of HEAD (pyproject.toml or setup.py): the
          sdist and the wheel, both listed
  Cargo:  cargo package --list --allow-dirty, in the project root (Cargo.toml)

A finding per leaking package, library.package-leak (exit 1, action replan), names the
first 20 paths and counts them, with the fix: for Go, ape project fence and a commit; for
the others, the folders left out in the manifest (package.json files or .npmignore,
MANIFEST.in or the build backend's excludes, Cargo.toml exclude).

Never a silent pass: a package it could not pack is the notice library.package-unchecked
(exit unchanged) naming why — the tool not on PATH (npm, python3 or its build module,
cargo) or failing, HEAD with no commit, or no manifest ape knows. Notices go to stderr as
"notice:" lines, and under "notices" with --json; apex-release states them in its reply.

JSON: {"ok", "library", "head", "packages": [{"ecosystem": go|npm|python|cargo, "dir",
"manifest", "tool", "checked", "files", "leaks": [<path>], "leaks_total", "unchecked",
"zip_error"}], "findings", "notices"}; on a leak the same keys sit at the top level beside
the failure's ok/exit/diagnostics.

Exit codes: 0 no APEX folder in any package checked (or not a library) · 1
library.package-leak · 2 usage.`,
		Example: "  ape project package-check\n  ape project package-check --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := v1Project(&f)
			if err != nil {
				return err
			}
			if err := v1RequireInit(cmd, &f, p); err != nil {
				return err
			}
			res, err := p.PackageCheck(cmd.Context())
			if !f.json {
				printNotices(cmd.ErrOrStderr(), res.Notices)
			}
			if err != nil {
				return v1FailWith(cmd, &f, err, nil, map[string]any{"library": res.Library, "head": res.Head, "packages": res.Packages, "notices": res.Notices})
			}
			out := map[string]any{"ok": true, "library": res.Library, "head": res.Head, "packages": res.Packages, "findings": []apexv1.Finding{}, "notices": res.Notices}
			return v1Emit(cmd.OutOrStdout(), &f, out, func(w io.Writer) {
				if !res.Library {
					fmt.Fprintln(w, "package-check: not a library (_apex/config.yaml has no library: true) — nothing to check")
					return
				}
				for i := range res.Packages {
					fmt.Fprintln(w, packageLine(&res.Packages[i]))
				}
				fmt.Fprintln(w, "package-check: green")
			})
		},
	}
	addV1Flags(cmd, &f)
	return cmd
}

// packageLine is one package's line in package-check's human output.
func packageLine(r *apexv1.PackageReport) string {
	head := "package   " + r.Ecosystem + " " + r.Dir + " (" + r.Manifest + ")"
	if !r.Checked {
		return head + ": not checked — " + r.Unchecked
	}
	line := fmt.Sprintf("%s: %d file(s), none under an APEX folder (%s)", head, r.Files, r.Tool)
	if r.ZipError != "" {
		line += "; the zip itself would fail: " + r.ZipError
	}
	return line
}
