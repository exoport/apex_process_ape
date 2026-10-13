package apecmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/exoport/apex_process_ape/internal/pipeline"
)

// resolveRunExtensions resolves the run's extension set once, before any
// UI starts, and reports what it will skip on w.
//
// It runs here rather than only inside pipeline.Run so the operator reads
// the plan — and where `--from` lands when it names a skipped stage —
// before the TUI takes the screen. The runner reuses the same set, so the
// config is read once per run.
func resolveRunExtensions(w io.Writer, spec *pipeline.Spec, projectRoot, fromStage string, ignore, quiet bool) (*pipeline.Extensions, error) {
	ext, err := pipeline.ExtensionsFor(spec, projectRoot, ignore)
	if err != nil || ext == nil || quiet || !spec.HasExtensionGates() {
		return ext, err
	}
	if ext.Ignored {
		fmt.Fprintln(w, "▸ extensions: gates ignored (--ignore-extensions); every stage runs")
		return ext, nil
	}
	enabled := "none"
	if len(ext.Enabled) > 0 {
		enabled = strings.Join(ext.Enabled, ", ")
	}
	fmt.Fprintf(w, "▸ extensions: %s\n", enabled)
	plan := spec.PlanSkips(ext)
	stages := spec.Stages()
	for si := range stages {
		stage := &stages[si]
		p := plan[stage.Name]
		if p.Stage != nil {
			fmt.Fprintf(w, "▸ skip stage %s (needs %s)\n", stage.Name, strings.Join(p.Stage.Missing, ", "))
			continue
		}
		for i, s := range p.Steps {
			if s != nil {
				fmt.Fprintf(w, "▸ skip step %s/%d %s (needs %s)\n",
					stage.Name, i+1, stage.Chain[i].Skill, strings.Join(s.Missing, ", "))
			}
		}
	}
	if fromStage != "" {
		started, skipped := spec.ResolveFromStage(fromStage, plan)
		switch {
		case skipped && started == "":
			fmt.Fprintf(w, "▸ --from %s: that stage and every one after it is skipped; nothing will run\n", fromStage)
		case skipped:
			fmt.Fprintf(w, "▸ --from %s: that stage is skipped; starting at %s\n", fromStage, started)
		}
	}
	return ext, nil
}
