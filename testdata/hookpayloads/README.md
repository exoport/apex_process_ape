# Hook-payload fixtures

Real Claude Code hook envelopes captured by `ape notify` → the bridge, taken from
`<run-dir>/hook-events.jsonl` of actual runs. They exist because the shapes here are
**not reproducible on demand**: the framework's spawn-parameter contract now prevents
the detached-spawn case, so a fresh capture of it cannot be made deliberately.

Each line is one `runlog.HookEntry` — `{event, timestamp, step, session_id, agent_id,
payload}` — where `payload` is Claude Code's verbatim hook envelope.

| File | Source | What it proves |
| --- | --- | --- |
| `stop-clean-with-contract.jsonl` | `apex-story-batch-dev`, 2026-08-08, CC 2.1.226 | The healthy path: `background_tasks: []` **and** a terminal contract (`run_status: partial`) in `last_assistant_message`. Gate A negative + Gate C positive. |
| `stop-detached-subagent.jsonl` | `apex-story-batch-create`, 2026-07-31, CC 2.1.220 | The defect: a `subagent` still `running` in `background_tasks` at the `Stop` that `ape` used to treat as completion. **Gate A positive.** |
| `subagent-pairs.jsonl` | same run as the clean `Stop` | Two well-formed `SubagentStart`/`SubagentStop` pairs — `agent_id` on both, `agent_transcript_path` on `Stop` only. |
| `agent-post-completed.jsonl` | same run as the clean `Stop` | A healthy Agent-tool `PostToolUse`: `tool_response.status == "completed"`. **Gate B negative** — the shape that must NOT trip the teammate check. |

## Which fixture serves which gate

`stop-detached-subagent.jsonl` is a **Gate A fixture only**. Its skill,
`apex-story-batch-create`, emits no machine-readable terminal contract at all, so it
is *not* a valid Gate C negative — an absent `run_status` there is expected, not a
failure. Use `stop-clean-with-contract.jsonl` for Gate C.

## Verbatim-ness

Payloads are byte-verbatim except for two free-text fields that `ape` never parses,
truncated to `… [truncated for fixture]` to keep these files near the repo's ~10 KB
fixture norm (the untrimmed originals were 7.1 MB and 397 KB):

- `subagent-pairs.jsonl` — `last_assistant_message` on each `SubagentStop`.
- `agent-post-completed.jsonl` — `tool_input.prompt`, and `tool_response.prompt` /
  `tool_response.content`.

Every field any gate reads — `background_tasks`, `last_assistant_message` on the main
`Stop`, `tool_response.status`, `agent_id`, `agent_transcript_path` — is untouched.
Keys are sorted for a stable diff; JSON key order is not semantic.
