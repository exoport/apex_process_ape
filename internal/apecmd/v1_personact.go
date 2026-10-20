package apecmd

import (
	"context"
	"sort"
	"strings"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// E2, the command side (D178, D179; apexv1/personact.go has the rules).
// A person act's command line is read the same way twice — by `ape hook
// pre-tool`, from the Bash command's words, and by the command itself, from
// its parsed flags — so both name it by the same normalised argv: the verb,
// the positional arguments, and every flag set but --cwd, --json, --wait
// and --no-detach (how to run it, not what it does), as --name=value,
// sorted. A grant is keyed by that argv's sha256.

// canonSkip are the flags the normalised argv leaves out. A long run's
// worker gets its --cwd from its call, so --cwd must be one of them.
var canonSkip = map[string]bool{"cwd": true, "json": true, "wait": true, "no-detach": true}

// canonicalArgv is cmd's normalised command line (see above).
func canonicalArgv(cmd *cobra.Command, args []string) []string {
	argv := append([]string{cmd.Parent().Name(), cmd.Name()}, args...)
	var flags []string
	cmd.Flags().Visit(func(fl *pflag.Flag) {
		if !canonSkip[fl.Name] {
			flags = append(flags, "--"+fl.Name+"="+fl.Value.String())
		}
	})
	sort.Strings(flags)
	return append(argv, flags...)
}

// personCallOf is the person act cmd, parsed with args, carries out; nil
// when it carries out none (a plain close, a delegate's verdict, …).
func personCallOf(cmd *cobra.Command, args []string) *apexv1.PersonCall {
	if cmd.Parent() == nil || cmd.ValidateArgs(args) != nil {
		return nil
	}
	fl := cmd.Flags()
	str := func(name string) string { v, _ := fl.GetString(name); return v }
	on := func(name string) bool { v, err := fl.GetBool(name); return err == nil && v }
	c := apexv1.PersonCall{By: strings.TrimSpace(str("by"))}
	switch cmd.Parent().Name() + " " + cmd.Name() {
	case "milestone approve":
		c.Act, c.Target = apexv1.ActMilestoneApprove, args[0]
	case "change approve":
		c.Act, c.Target = apexv1.ActTweakApprove, args[0]
	case "change answer":
		c.Act, c.Target, c.Text = apexv1.ActAnswer, args[0], str("text")
	case "project accept-config":
		c.Act, c.Target = apexv1.ActAcceptConfig, "config"
	case "decision accept":
		c.Act, c.Target = apexv1.ActDecisionAccept, args[0]
	case "milestone close":
		if !fl.Changed("accept-red") {
			return nil
		}
		c.Act, c.Target = apexv1.ActAcceptRed, args[0]
	case "change archive":
		if !on("allow-pending-human") {
			return nil
		}
		c.Act, c.Target = apexv1.ActAllowPendingHuman, args[0]
	case "change accept":
		if on("delegate") || str("verdict") != "" {
			return nil
		}
		task := str("task")
		if len(args) == 2 {
			if task != "" && task != args[1] {
				return nil
			}
			task = args[1]
		}
		if task == "" {
			return nil
		}
		c.Act, c.Target, c.Task = apexv1.ActAcceptTask, args[0], task
	default:
		return nil
	}
	argv := canonicalArgv(cmd, args)
	c.ArgvSHA256 = apexv1.ArgvSHA256(argv)
	c.Line = displayLine(argv)
	c.Literal = true
	return &c
}

// displayLine is a normalised argv as a reason shows it.
func displayLine(argv []string) string {
	parts := make([]string, 0, len(argv)+1)
	parts = append(parts, "ape")
	for _, a := range argv {
		if name, val, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") {
			if val == "true" {
				parts = append(parts, name)
				continue
			}
			parts = append(parts, name, shellQuote(val))
			continue
		}
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"$`\\;&|<>(){}*?[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// personActGroups build the command groups a person act lives in.
var personActGroups = map[string]func() *cobra.Command{
	"milestone": newMilestoneCmd, "change": newChangeCmd, "project": newProjectCmd, "decision": newDecisionCmd,
}

// personCallsIn are the person acts a Bash command line runs, each read
// as the command will read itself.
func personCallsIn(command string) []apexv1.PersonCall {
	literal := !strings.ContainsAny(command, "$`")
	var out []apexv1.PersonCall
	for _, words := range apexv1.ApeCommands(command) {
		if len(words) < 2 || personActGroups[words[0]] == nil {
			continue
		}
		group := personActGroups[words[0]]()
		cmd, rest, err := group.Find(words[1:])
		if err != nil || cmd == group || cmd.ParseFlags(rest) != nil {
			continue
		}
		if c := personCallOf(cmd, cmd.Flags().Args()); c != nil {
			c.Literal = literal
			out = append(out, *c)
		}
	}
	return out
}

// takePersonAct is the act a person act's call carries out, before its
// body runs (E2). From a person's terminal: the act as given, by its
// --by. In a person's Claude Code session (CLAUDECODE): the grant
// pre-tool wrote for this command line, consumed, by: git config
// user.name with the prompt that granted it; no grant is
// session.person-only naming the phrase, a --by that is not user.name
// session.by-mismatch. Under APE_SESSION: nil — the body refuses, as
// before (T7).
func takePersonAct(ctx context.Context, p *apexv1.Project, c apexv1.PersonCall) (*apexv1.PersonAct, error) {
	switch apexv1.ModelSession() {
	case "":
		return &apexv1.PersonAct{Act: c.Act, Target: c.Target, ArgvSHA256: c.ArgvSHA256, By: c.By}, nil
	case "CLAUDECODE":
	default:
		return nil, nil //nolint:nilnil // under APE_SESSION: no act, the body refuses (T7)
	}
	// No grant first; then a --by that is not user.name, checked before
	// the grant is spent.
	if !p.HasGrant(c) {
		return nil, apexv1.NoGrant(c)
	}
	name := apexv1.GitUserName(ctx, p.Root)
	if name == "" {
		return nil, apexv1.NoUserName()
	}
	if c.By != "" && c.By != name {
		return nil, apexv1.ByMismatch(c.By, name)
	}
	a := p.ConsumeGrant(ctx, c)
	if a == nil {
		return nil, apexv1.NoGrant(c)
	}
	a.By = apexv1.PersonBy(name, a.PromptTS, a.Quote)
	return a, nil
}

// v1PersonAct takes the person act of a call that runs in this process
// (every person act but the long ones, which runLong takes under the run
// lock), and hands it to the body through cmd's context.
func v1PersonAct(cmd *cobra.Command, p *apexv1.Project, c *apexv1.PersonCall) error {
	if c == nil {
		return nil
	}
	a, err := takePersonAct(cmd.Context(), p, *c)
	if err != nil {
		return err
	}
	if a != nil {
		cmd.SetContext(apexv1.WithPersonAct(cmd.Context(), a))
	}
	return nil
}

// personBy is the by a person act records: the granted act's (the
// person's user.name and prompt) in a model session, else --by as given.
func personBy(ctx context.Context, by string) string {
	if a := apexv1.PersonActFrom(ctx); a != nil && a.By != "" {
		return a.By
	}
	return by
}
