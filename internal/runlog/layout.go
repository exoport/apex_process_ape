package runlog

import (
	"path/filepath"

	"github.com/exoport/apex_process_ape/internal/apexcfg"
)

// The on-disk layout of everything ape writes into a project, in one
// place.
//
// The output folder is not ape's directory. It is the framework's
// `output_folder` — the skills write handoffs, briefs and verify reports
// there. ape used to scatter run artifacts across it as siblings of that
// content: `_output/pipelines/`, `_output/tasks/`, and (inconsistently)
// `_output/ape/prompts/` and `_output/ape/chats/`, at two different
// nesting depths. ape now owns exactly one subtree, `{output_folder}/ape/`,
// and nothing outside it.
//
// The scatter was not just untidy. Every consumer re-derived the root list
// for itself, and their coverage varied:
//
//   - hookdrift swept only `_output/tasks`, so `ape pipeline` — the
//     flagship command — was invisible to the hook-contract check, and a
//     project that only ran pipelines reported "no interactive runs" for
//     ever. That one was a real bug;
//   - `ape metrics --run-id` globbed `_output/*/*/<id>/manifest.yaml`,
//     which matched both nesting shapes purely by coincidence of depth;
//   - only `cost.ScanProject` had the full list.
//
// So the fix is not only the move — it is that this file is now the single
// definition, and RunRoots is the only way to ask "where are the runs?".
// A fifth producer is one line here and no change anywhere else.
//
// Two consumers cover a SUBSET on purpose, and that is not the same bug:
// `ape costs reprice` skips chats because `session.yaml` carries no
// per-model token breakdown to recompute a cost from, and
// `cost.FindRunManifest` covers only the manifest-bearing kinds because
// `ape costs chat|prompt` serve the other two. Both now express that as a
// decision taken against RunRoots rather than as a hand-written path list
// that happens to omit them.
//
// Migration: LegacyRunRoots names the pre-move locations, and Migrate
// relocates them. `ape framework setup|update` runs it.
//
// # The output folder is the framework's to name
//
// `output_folder` is a framework config variable. It defaults to
// `_output`, and a project may point it anywhere. ape resolves it, so the
// real root is `{output_folder}/ape` — not a hardcoded `_output/ape`.
//
// Resolution has to degrade, because run paths are needed in places a
// project config is not guaranteed: `ape chat` outside a project, a run
// whose `config.yaml` has a syntax error, a bare directory. An absent or
// unparseable config therefore falls back to the framework's own default,
// `_output`. That is the best available guess, and the failure it can
// produce — writing under `_output/ape` on a project whose config both
// renames output_folder AND does not parse — is already surfaced loudly by
// `ape doctor`'s required config.resolved check.
//
// One asymmetry matters for the migration: ape hardcoded `_output` BEFORE
// this, so legacy artifacts are under a literal `_output` whatever
// output_folder says. LegacyRunRoots reads From literally and To from the
// resolved folder, which is why a project that renamed output_folder has
// four relocations rather than two.
//
// Not migrated, because both regenerate at the new path: `cost-rollup.json`
// (a cache `ape costs` rebuilds) and `service/` (transient job state). On a
// project that renamed output_folder, stale copies may be left under the
// old `_output/ape/` and can be deleted by hand.

const (
	// DefaultOutputDirName is the framework's default output_folder, and
	// the fallback when the project config cannot be read.
	DefaultOutputDirName = "_output"
	// ApeDirName is ape's subtree inside the output folder. Everything ape
	// writes lives under {output_folder}/ape/ and nothing outside it.
	ApeDirName = "ape"
)

// Run-kind directory names, and the kind labels reports use for them.
const (
	KindPipeline = "pipeline"
	KindTask     = "task"
	KindPrompt   = "prompt"
	KindChat     = "chat"
)

// OutputRoot is the project's output folder — framework-owned, shared.
// Resolved from `output_folder`, falling back to the framework default.
func OutputRoot(projectRoot string) string {
	if res, err := apexcfg.ResolveAt(projectRoot, nil); err == nil && res.Paths.Output != "" {
		return res.Paths.Output
	}
	// No config, an unparseable one, or output_folder left blank. The
	// framework's own default is the only honest guess.
	return filepath.Join(projectRoot, DefaultOutputDirName)
}

// ApeRoot is the subtree ape owns: {output_folder}/ape.
func ApeRoot(projectRoot string) string {
	return filepath.Join(OutputRoot(projectRoot), ApeDirName)
}

// PipelinesRoot holds one directory per pipeline, each holding run dirs.
func PipelinesRoot(projectRoot string) string {
	return filepath.Join(ApeRoot(projectRoot), "pipelines")
}

// TasksRoot holds one directory per skill, each holding run dirs.
func TasksRoot(projectRoot string) string {
	return filepath.Join(ApeRoot(projectRoot), "tasks")
}

// PromptsRoot holds one directory per `ape prompt` run.
func PromptsRoot(projectRoot string) string {
	return filepath.Join(ApeRoot(projectRoot), "prompts")
}

// ChatsRoot holds one directory per `ape chat` session.
func ChatsRoot(projectRoot string) string {
	return filepath.Join(ApeRoot(projectRoot), "chats")
}

// ServiceRoot is `ape service`'s state directory.
func ServiceRoot(projectRoot string) string {
	return filepath.Join(ApeRoot(projectRoot), "service")
}

// CostRollupPath is the cached project cost rollup.
func CostRollupPath(projectRoot string) string {
	return filepath.Join(ApeRoot(projectRoot), "cost-rollup.json")
}

// TimestampStatePath holds the last stamp ape issued for this project —
// the persisted floor behind the monotonic clock in internal/stamp.
//
// Not a run artifact and not a cache: deleting it does not lose history,
// but it does drop the floor, which is why the issuer re-seeds from the
// tracker rather than restarting at the wall clock.
func TimestampStatePath(projectRoot string) string {
	return filepath.Join(ApeRoot(projectRoot), "timestamp.state")
}

// PipelineRunDir is where one pipeline run's artifacts live.
func PipelineRunDir(projectRoot, pipelineName, runID string) string {
	return filepath.Join(PipelinesRoot(projectRoot), pipelineName, runID)
}

// TaskRunDir is where one `ape task` run's artifacts live.
func TaskRunDir(projectRoot, skill, runID string) string {
	return filepath.Join(TasksRoot(projectRoot), skill, runID)
}

// PromptRunDir is where one `ape prompt` run's artifacts live.
func PromptRunDir(projectRoot, promptID string) string {
	return filepath.Join(PromptsRoot(projectRoot), promptID)
}

// ChatRunDir is where one `ape chat` session's artifacts live.
func ChatRunDir(projectRoot, chatID string) string {
	return filepath.Join(ChatsRoot(projectRoot), chatID)
}

// RunRoot is one tree of run directories.
type RunRoot struct {
	// Path is the absolute root.
	Path string
	// Kind labels the runs beneath it (KindPipeline, KindTask, …).
	Kind string
	// Grouped reports the nesting: a grouped root holds
	// <group>/<runID>/ (pipelines are grouped by pipeline name, tasks by
	// skill), an ungrouped one holds <runID>/ directly. Consumers that
	// build globs need this; consumers that walk do not.
	Grouped bool
}

// RunRoots is every tree ape writes runs into, in a stable order.
//
// This is the list. A consumer that enumerates roots for itself is a
// consumer that will be missing one — that is the bug this replaces.
func RunRoots(projectRoot string) []RunRoot {
	// Resolved once: every accessor would otherwise re-read the project
	// config, and this is called in loops.
	root := ApeRoot(projectRoot)
	return []RunRoot{
		{Path: filepath.Join(root, "pipelines"), Kind: KindPipeline, Grouped: true},
		{Path: filepath.Join(root, "tasks"), Kind: KindTask, Grouped: true},
		{Path: filepath.Join(root, "prompts"), Kind: KindPrompt},
		{Path: filepath.Join(root, "chats"), Kind: KindChat},
	}
}

// LegacyRunRoots names the pre-move locations of ape's run trees, paired
// with where each now belongs.
//
// `From` is built on a LITERAL `_output`, not on the resolved output
// folder, because that is where the artifacts actually are: ape hardcoded
// `_output` before this change, whatever `output_folder` said.
//
// That asymmetry is why the list is four entries and not two. On a default
// project (`output_folder: _output`) prompts and chats were already at
// their final path, so those two relocations are identities and are
// dropped. On a project that renamed output_folder, all four moved.
//
// Safe to call on any project: a tree that never existed simply is not
// there.
func LegacyRunRoots(projectRoot string) []Relocation {
	root := ApeRoot(projectRoot)
	legacy := filepath.Join(projectRoot, DefaultOutputDirName)
	all := []Relocation{
		{
			From: filepath.Join(legacy, "pipelines"),
			To:   filepath.Join(root, "pipelines"),
			Kind: KindPipeline, Grouped: true,
		},
		{
			From: filepath.Join(legacy, "tasks"),
			To:   filepath.Join(root, "tasks"),
			Kind: KindTask, Grouped: true,
		},
		{
			From: filepath.Join(legacy, ApeDirName, "prompts"),
			To:   filepath.Join(root, "prompts"),
			Kind: KindPrompt,
		},
		{
			From: filepath.Join(legacy, ApeDirName, "chats"),
			To:   filepath.Join(root, "chats"),
			Kind: KindChat,
		},
	}
	out := make([]Relocation, 0, len(all))
	for _, r := range all {
		if filepath.Clean(r.From) == filepath.Clean(r.To) {
			continue // already where it belongs — the default project
		}
		out = append(out, r)
	}
	return out
}

// Relocation is one legacy tree and the place it now belongs. Grouped has
// the same meaning as on RunRoot: a grouped tree holds <group>/<runID>, an
// ungrouped one holds <runID> directly.
type Relocation struct {
	From    string
	To      string
	Kind    string
	Grouped bool
}
