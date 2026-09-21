package apecmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// The unknown-verb guard: a command group must not answer a verb it does
// not have with a success code.
//
// # The defect
//
// Cobra's default for an unknown sub-verb of a command that HAS
// subcommands is to print the parent's help on STDOUT and exit 0. Only
// the ROOT command errors, because cobra's legacyArgs special-cases it.
// Measured on 0.0.68, and reproduced independently on 0.0.56 twelve
// releases earlier, so this is cobra's default rather than a regression
// and ape's whole surface has carried it since the first group shipped:
//
//	ape sprint zzunknown   exit 0, 674 bytes of help on stdout, empty stderr
//	ape story  zzunknown   exit 0, 376 bytes
//	ape doc    zzunknown   exit 0, 709 bytes          … and so on
//
// A group carrying its own Run does something strictly worse — it runs
// it:
//
//	$ ape costs zzunknown            # exit 0
//	BUCKET  RUNS  COST  INPUT  OUTPUT  CACHE-R
//
// That is not help text a caller could recognise as wrong. It is a
// plausible table from a real command nobody asked for, at exit 0.
// `ape sessions zzunknown` is the same shape. Any group that later grows
// a Run joins them silently, which is why this guard is installed
// structurally over the tree rather than left to each constructor to
// remember.
//
// # Why it has never bitten anyone
//
// An accident: nearly every scripted call carries `--output-format` or
// `--cwd`, and flag parsing at the bare parent rejects an unknown flag
// with a genuine exit 1 BEFORE the unknown-verb path is reached. So
// `ape sprint zzunknown --output-format json` exits 1 and
// `ape sprint zzunknown` exits 0. The protection is real, undocumented,
// and not uniform: a verb invoked with no flags falls straight through.
//
// # Why RunE is load-bearing and not decoration
//
// Cobra's execute() returns flag.ErrHelp for a command that is not
// Runnable BEFORE it calls ValidateArgs. Measured: a group with
// subcommands, no Run, and this exact Args function still answers an
// unknown verb with exit 0 and help. So Args ALONE is dead code on the
// eighteen groups that have no Run — it would pass review, pass its own
// tests, and guard nothing. The Help closure below is what makes the
// command Runnable so that Args is consulted at all.
//
// # Why the shared flags are NOT hoisted onto the group
//
// Declaring `--output-format` and `--cwd` as persistent flags on each
// group would make the error message name the verb rather than the flag
// in the flag-carrying case above. It was prototyped and rejected:
// measured, a child silently ACCEPTS a persistent flag its own command
// never declared and ignores it, so hoisting them across twenty groups
// would make `ape doc shard --cwd /nowhere` a silent no-op. That is a
// fresh instance of the defect being fixed here, traded for a better
// error string. The flag-carrying case already exits non-zero, which is
// the property that matters; the message naming the flag instead of the
// verb is a cosmetic loss and is the cheaper of the two.
//
// A group whose children ALL take both flags may hoist them safely and
// get the better message — that is a per-group judgement, not a blanket
// one.
//
// # The residual: --help bypasses it
//
// Cobra processes the help flag BEFORE it validates arguments, so
// `ape sprint zzunknown --help` still exits 0 with the group's help. The
// honest statement of what this guard gives is therefore "an unknown verb
// is exit 2 UNLESS --help is passed".
//
// Left open by decision rather than oversight. Nothing downstream reaches
// it: no sanctioned framework call passes --help, and `ape doctor`'s
// framework.command_surface resolves through rootCmd.Find rather than by
// invoking anything. Closing it would mean overriding cobra's built-in
// help handling on every guarded group — more surface than the residual
// warrants.
//
// It is worth knowing about for one reason: `<verb> --help` is a
// TEMPTING existence probe and it lies. Probing a set of declared verbs
// that way once reported every one present against an implementation
// missing three of them — minutes after this guard was written, by the
// person who wrote it. Probe by invoking.
//
// # What is deliberately not guarded
//
// Cobra's own `completion` command, which cobra adds to the root itself
// rather than through rootSubcommands, and the `aboard` SUBTREE. The
// aboard tree comes from a separate module with its own exit table and
// its own argument contracts; ape hosts it rather than owning it, so the
// guard is installed on the `aboard` node and stops there.
//
// guardGroup installs the guard on one command, if it is a group.
//
// A command with no subcommands is left alone: its positional arguments
// are its own contract (`ape task <skill>`, `ape script <file>`), and a
// blanket NoArgs across the surface would break them.
func guardGroup(cmd *cobra.Command) {
	if !cmd.HasSubCommands() {
		return
	}

	// Chained rather than replaced: a group that already validates its
	// own arguments keeps doing so for the zero-argument case.
	prev := cmd.Args
	cmd.Args = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			err := fmt.Errorf(
				"unknown command %q for %q — this ape binary does not provide it; "+
					"run `ape doctor --only framework.command_surface` to see whether the "+
					"installed framework expects commands this binary is missing",
				args[0], c.CommandPath())
			// Printed here with the command's own stderr, and returned as
			// reported so main does not print it a second time. Both
			// silencers above are on, so before main printed unreported
			// errors a guard that only returned one exited 2 with no
			// diagnostic at all — a worse answer than the help text it
			// replaced. Measured that way once.
			fmt.Fprintf(c.ErrOrStderr(), "Error: %s\n", err)
			return reportedErr(ExitUsage, err)
		}
		if prev != nil {
			return prev(c, args)
		}
		return nil
	}

	// Only when the group has no behaviour of its own. `ape costs` and
	// `ape sessions` keep theirs: the guard changes what an unknown VERB
	// does, never what a bare invocation does.
	if !cmd.Runnable() {
		cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	}

	// Without these, cobra prints the usage block on the Args error and
	// the help text lands on stdout again — the thing being fixed.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
}

// guardArgs makes a command's ARGUMENT validation answer with ape's
// usage code rather than the code a gate uses for a finding.
//
// The same collision the flag path had: cobra reports a rejected
// argument as an ordinary error, which the exit table maps to 1, and
// every gate in this binary uses 1 to mean "I looked and found
// something". `ape doctor zzbogus` exited 1 — cobra.NoArgs refusing an
// argument it cannot mean — which reads as a doctor run that found a
// problem.
//
// Distinct from guardGroup above, which replaces a GROUP's Args
// entirely. This wraps whatever validator a command already has,
// including cobra's own NoArgs and ExactArgs, so a command keeps its
// contract and only the exit code moves. Nothing here matches an error
// message: the validator's verdict is ape-side, and its text is not
// read.
func guardArgs(cmd *cobra.Command) {
	prev := cmd.Args
	if prev == nil {
		return // no declared contract: positional args are this command's own
	}
	cmd.Args = func(c *cobra.Command, args []string) error {
		err := prev(c, args)
		if err == nil {
			return nil
		}
		if _, ok := errors.AsType[*exitError](err); ok {
			return err // already carries a code — guardGroup's, typically
		}
		return usageErr(err)
	}
}

// guardArgsDeep applies guardArgs across a tree.
func guardArgsDeep(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		guardArgs(cmd)
		guardArgsDeep(cmd.Commands()...)
	}
}

// guardGroupsDeep installs the guard on every group in each tree.
func guardGroupsDeep(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		guardGroup(cmd)
		guardGroupsDeep(cmd.Commands()...)
	}
}
