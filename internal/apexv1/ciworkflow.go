package apexv1

import (
	"os"
	"path/filepath"
)

// QueueWorkflowRel is the merge queue's workflow in a project integrating
// through GitHub (v1.5 stage 1a, section 5.5; `ape ci workflow --write`).
const QueueWorkflowRel = ".github/workflows/apex-queue.yml"

// QueueWorkflow is that workflow (D702: the queue is main's only). On
// merge_group it runs `ape ci check` on the group, the queue's required
// check, and on a red one publishes ejected; on a push to main it
// publishes close-merged, so the waiting close wakes (OQ10). On a pull
// request it passes at once: the queue runs the checks, on the tree that
// merges. Self-hosted runners (a project's live services, section 5.5);
// the NATS URL and the ensemble credential are CI secrets, never a file
// in the repository.
const QueueWorkflow = `# Written by ape (APEX v1.5, section 5.5): the merge queue's check and its events.
# Required check: "apex-queue / check". Secrets: APE_NATS_URL, APE_NATS_CREDS.
name: apex-queue
on:
  merge_group:
  pull_request:
    branches: [main]
  push:
    branches: [main]
concurrency:
  group: apex-queue-${{ github.ref }}
  cancel-in-progress: false
jobs:
  check:
    if: github.event_name != 'push'
    runs-on: [self-hosted]
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
    runs-on: [self-hosted]
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

// WriteQueueWorkflow writes QueueWorkflow to the project; changed reports
// that the file was absent or differed.
func (p *Project) WriteQueueWorkflow() (changed bool, err error) {
	path := filepath.Join(p.Root, filepath.FromSlash(QueueWorkflowRel))
	if data, err := os.ReadFile(path); err == nil && string(data) == QueueWorkflow {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, writeFileAtomic(path, QueueWorkflow)
}
