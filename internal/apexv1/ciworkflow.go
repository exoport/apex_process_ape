package apexv1

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// QueueWorkflowRel is the merge queue's workflow in a project integrating
// through GitHub (v1.5 stage 1a, section 5.5; `ape ci workflow --write`).
const QueueWorkflowRel = ".github/workflows/apex-queue.yml"

// queueWorkflow is that workflow (D702: the queue is main's only). On
// merge_group it runs `ape ci check` on the group, the queue's required
// check, and on a red one publishes ejected; on a push to main it
// publishes close-merged, so the waiting close wakes (OQ10). On a pull
// request it passes at once: the queue runs the checks, on the tree that
// merges. Self-hosted runners (a project's live services, section 5.5),
// and only the project's own: every job runs on [self-hosted, <label>]
// (D871, V12-15), since a bare self-hosted sent the jobs — and the
// credential — to another org runner. The NATS URL and the ensemble
// credential are CI secrets, never a file in the repository. Its token
// reads the repository and nothing more (V12-12): no job of it writes to
// GitHub. runnerLabelSlot stands for the label.
const queueWorkflow = `# Written by ape (APEX v1.5, section 5.5): the merge queue's check and its events.
# Required check: "apex-queue-check". Secrets: APE_NATS_URL, APE_NATS_CREDS.
name: apex-queue
on:
  merge_group:
  pull_request:
    branches: [main]
  push:
    branches: [main]
permissions:
  contents: read
concurrency:
  group: apex-queue-${{ github.ref }}
  cancel-in-progress: false
jobs:
  apex-queue-check:
    if: github.event_name != 'push'
    runs-on: [self-hosted, ` + runnerLabelSlot + `]
    steps:
      - name: the queue runs the checks
        if: github.event_name == 'pull_request'
        run: echo "checked in the merge queue, on the tree that merges"
      - uses: actions/checkout@v4
        if: github.event_name == 'merge_group'
        with:
          fetch-depth: 0
      - name: ape ci check
        if: github.event_name == 'merge_group'
        run: ape ci check --base "${{ github.event.merge_group.base_sha }}" --head "${{ github.event.merge_group.head_sha }}"
      - name: ejected
        if: failure() && github.event_name == 'merge_group'
        env:
          APE_NATS_URL: ${{ secrets.APE_NATS_URL }}
          APE_NATS_CREDS_TEXT: ${{ secrets.APE_NATS_CREDS }}
        run: |
          creds="$RUNNER_TEMP/ape-nats.creds"; umask 077; printf '%s\n' "$APE_NATS_CREDS_TEXT" > "$creds"
          ape ci event ejected --ref "${{ github.event.merge_group.head_ref }}" --sha "${{ github.event.merge_group.head_sha }}" --nats-url "$APE_NATS_URL" --nats-creds "$creds"
  merged:
    if: github.event_name == 'push'
    runs-on: [self-hosted, ` + runnerLabelSlot + `]
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 2
      - name: merged
        env:
          APE_NATS_URL: ${{ secrets.APE_NATS_URL }}
          APE_NATS_CREDS_TEXT: ${{ secrets.APE_NATS_CREDS }}
        run: |
          creds="$RUNNER_TEMP/ape-nats.creds"; umask 077; printf '%s\n' "$APE_NATS_CREDS_TEXT" > "$creds"
          ape ci event close-merged --ref "${{ github.ref_name }}" --sha "${{ github.sha }}" --nats-url "$APE_NATS_URL" --nats-creds "$creds"
`

// runnerLabelSlot is where queueWorkflow names the runner's label.
const runnerLabelSlot = "RUNNER-LABEL"

// CodeCIWorkflowMode: `ape ci workflow` in a project whose mode is not
// queue/runner, the one mode whose events come from the queue's workflow
// (D876, V12-15).
const CodeCIWorkflowMode = "ci.workflow-mode"

// CodeCIRunnerNotInRelease: queue/runner's workflow asked for, in a
// release that does not ship the mode (D884).
const CodeCIRunnerNotInRelease = "ci.runner-not-in-release"

// RunnerModeReleased: whether this release ships queue/runner. 1.5 does
// not (D884): it is the one mode that puts a NATS credential into GitHub,
// and it is not measured live yet. events: runner still resolves as a
// mode, but nothing writes its workflow. Tests of the workflow itself set
// it.
var RunnerModeReleased = false

// RunnerModeAvailable is nil when this release ships queue/runner, else
// CodeCIRunnerNotInRelease.
func RunnerModeAvailable() error {
	if RunnerModeReleased {
		return nil
	}
	return &FailError{Code: CodeCIRunnerNotInRelease, Action: ActionPerson, Msg: "integration mode queue/runner (events: runner) is not available in this release: ape ships solo, trunk, queue/poll and queue/watch, and writes no merge-queue workflow (D884) — use events: watch (`ape ci watch` on the coordination host publishes each merge, with nothing on GitHub), or events: poll"}
}

// QueueWorkflowFor is the workflow for the runner labelled label, every
// job on [self-hosted, label]; refused while this release does not ship
// queue/runner (RunnerModeAvailable), then a label that is not the
// runner's own (CheckRunnerLabel).
func QueueWorkflowFor(label string) (string, error) {
	if err := RunnerModeAvailable(); err != nil {
		return "", err
	}
	if err := CheckRunnerLabel(label); err != nil {
		return "", err
	}
	return strings.ReplaceAll(queueWorkflow, runnerLabelSlot, label), nil
}

// QueueWorkflow is the project's workflow: refused (CodeCIWorkflowMode)
// unless its mode is queue/runner, whose runner_label it names.
func (p *Project) QueueWorkflow() (string, error) {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return "", err
	}
	mode, err := cfg.Coordination.Mode()
	if err != nil {
		return "", err
	}
	if mode != ModeQueueRunner {
		return "", &FailError{Code: CodeCIWorkflowMode, Action: ActionPerson, Msg: fmt.Sprintf(
			"the merge queue's workflow is queue/runner's, and this project's integration mode is %s — queue/poll and queue/watch run nothing on GitHub, and queue/runner is not available in this release (D884)", mode)}
	}
	return QueueWorkflowFor(strings.TrimSpace(cfg.Coordination.RunnerLabel))
}

// WriteQueueWorkflow writes the project's QueueWorkflow; changed reports
// that the file was absent or differed.
func (p *Project) WriteQueueWorkflow() (changed bool, err error) {
	wf, err := p.QueueWorkflow()
	if err != nil {
		return false, err
	}
	path := filepath.Join(p.Root, filepath.FromSlash(QueueWorkflowRel))
	if data, err := os.ReadFile(path); err == nil && string(data) == wf {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, writeFileAtomic(path, wf)
}

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeCIRunnerNotInRelease, 1, ActionPerson, "`ape ci workflow` (print or --write), or the App service's workflow set-up act, for queue/runner, which this release does not ship (D884): ape 1.5 has solo, trunk, queue/poll and queue/watch. events: runner still resolves as a mode, but no merge-queue workflow is written — a person sets events: watch (`ape ci watch` on the coordination host publishes each merge, nothing on GitHub) or events: poll"},
		CodeDoc{CodeCIWorkflowMode, 1, ActionPerson, "`ape ci workflow` (print or --write) in a project whose integration mode is not queue/runner (D876, V12-15): the merge queue's workflow is queue/runner's only. queue/poll and queue/watch run nothing on GitHub and need no workflow; queue/runner is not available in this release (ci.runner-not-in-release, D884)"},
	)
}
