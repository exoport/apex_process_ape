package runlog

import "path/filepath"

// The on-disk layout of everything ape writes into a project, in one
// place.
//
// `_output/` is not ape's directory. It is the framework's `output_folder`
// — the skills write handoffs, briefs and verify reports there, and a
// project can point it somewhere else entirely. ape used to scatter run
// artifacts across it as siblings of that content: `_output/pipelines/`,
// `_output/tasks/`, and (inconsistently) `_output/ape/prompts/` and
// `_output/ape/chats/`. ape now owns exactly one subtree, `_output/ape/`,
// and nothing outside it.
//
// The scatter was not just untidy. Every consumer re-derived the root list
// for itself, and three of them got it wrong in the same way:
//
//   - hookdrift swept only `_output/tasks`, so `ape pipeline` — the
//     flagship command — was invisible to the hook-contract check, and a
//     project that only ran pipelines reported "no interactive runs" for
//     ever;
//   - `ape costs reprice` globbed pipelines, tasks and prompts but not
//     chats, so a chat's stored `cost_usd` was never recomputed;
//   - `ape costs run <id>` looked in pipelines and tasks only, so a prompt
//     or chat run id came back not-found.
//
// Meanwhile `cost.ScanProject` did cover all four. The correct list
// existed; it just was not shared. So the fix is not only the move — it is
// that this file is now the single definition, and RunRoots is the only
// way to ask "where are the runs?". A fifth producer is one line here and
// no change anywhere else.
//
// Migration: LegacyRunRoots names the pre-move locations, and Migrate
// relocates them. `ape framework setup|update` runs it.
//
// Known gap, deliberately not addressed here: these helpers hardcode
// `_output` rather than resolving the framework's `output_folder`
// variable, so a project that renames it still gets ape artifacts under
// `_output/ape`. Runlog paths have to resolve in contexts where the
// project config is absent or unparseable, so honouring the variable
// needs a fallback story that is its own change. Because every path now
// comes from this file, that change lands here and nowhere else.

const (
	// OutputDirName is the framework's default output_folder.
	OutputDirName = "_output"
	// ApeDirName is ape's subtree inside it. Everything ape writes lives
	// under <project>/_output/ape/ and nothing outside it.
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
func OutputRoot(projectRoot string) string {
	return filepath.Join(projectRoot, OutputDirName)
}

// ApeRoot is the subtree ape owns.
func ApeRoot(projectRoot string) string {
	return filepath.Join(projectRoot, OutputDirName, ApeDirName)
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
	return []RunRoot{
		{Path: PipelinesRoot(projectRoot), Kind: KindPipeline, Grouped: true},
		{Path: TasksRoot(projectRoot), Kind: KindTask, Grouped: true},
		{Path: PromptsRoot(projectRoot), Kind: KindPrompt},
		{Path: ChatsRoot(projectRoot), Kind: KindChat},
	}
}

// LegacyRunRoots names the pre-move locations of the roots that moved,
// paired with where each now belongs.
//
// Only pipelines and tasks moved: prompts and chats were already under
// `_output/ape/`, which is what made the old layout inconsistent in the
// first place. Returned oldest-first and safe to call on any project — a
// tree that never existed simply is not there.
func LegacyRunRoots(projectRoot string) []Relocation {
	return []Relocation{
		{
			From: filepath.Join(projectRoot, OutputDirName, "pipelines"),
			To:   PipelinesRoot(projectRoot),
			Kind: KindPipeline,
		},
		{
			From: filepath.Join(projectRoot, OutputDirName, "tasks"),
			To:   TasksRoot(projectRoot),
			Kind: KindTask,
		},
	}
}

// Relocation is one legacy tree and the place it now belongs.
type Relocation struct {
	From string
	To   string
	Kind string
}
