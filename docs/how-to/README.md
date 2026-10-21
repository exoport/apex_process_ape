# How-to guides

How-to guides are **recipes** — they answer "how do I X?" for a reader who already knows the basics. Each guide solves a specific problem in a specific context. Unlike [Tutorials](../tutorials/), how-to guides assume competence and skip the hand-holding; unlike [Reference](../reference/), they're goal-oriented rather than exhaustive.

## Available guides

**Install and set up**

- [How to install ape](install.md)
- [How to update ape](update.md)
- [How to verify a release artifact](verify.md)
- [How to pin ape in a project](pin-ape-in-a-project.md)
- [How to install the APEX framework into a project](framework-setup.md)
- [How to start an APEX v1 project](start-a-v1-project.md)
- [How to refresh the framework in a project](framework-update.md)
- [How to lift an APEX v0 project to v1](lift-a-v0-project.md)
- [How to move archived changes to the current archive layout](move-to-the-archive-layout.md)
- [How to pin a config key the framework falls back on](pin-a-config-key.md)

**Run APEX v1 work**

- [How to run a milestone](run-a-milestone.md)
- [How to record a decision](record-a-decision.md)
- [How to run a single skill with `ape task`](run-a-single-skill.md)
- [How to run ape in the background](detach-a-run.md)
- [How to hear from a project: notify recipes and `ape status --wait`](hear-from-a-project.md)
- [How to run an unattended session with `ape prompt`](run-a-prompt-session.md)
- [How to tune long-running sessions](tune-long-running-steps.md)
- [How to write and run an `ape script`](write-ape-scripts.md)
- [How to find and read run artefacts](run-artefacts.md)
- [How to use plugins and tool skills](use-plugins-and-tools.md)

**Integrate**

- [How to use the board (`ape aboard`)](use-the-board.md)
- [How to run ape as a service (NATS job daemon)](run-ape-as-a-service.md)
- [How to publish run progress to NATS](publish-progress-to-nats.md)
- [How to report from a Claude session](report-from-a-session.md)
- [How to upload run transcripts as content-addressed blobs](upload-transcripts.md)

**Sandbox workspaces**

- [How to run a sandboxed Kata VM workspace](sandbox-workspaces.md)
- [How to use a workspace as your dev container](devcontainer-workspaces.md)
- [How to run aped (the VM-management daemon)](run-aped.md)

**Operate**

- [How to keep cost pricing current](keep-cost-pricing-current.md)
- [How to run `ape doctor` in CI](run-doctor-in-ci.md)
- [How to verify a release before tagging](pre-tag-release.md)

## Writing a how-to guide

- Start with the problem (the goal in the reader's words), not the solution.
- Each guide focuses on one outcome. Don't bundle unrelated tasks.
- Show only the path that works for the stated problem. If a problem branches into materially different cases, write separate guides.
- Don't teach concepts in how-to guides — link to [Explanation](../explanation/) instead.

See the [Diátaxis how-to guide rubric](https://diataxis.fr/how-to-guides/).
