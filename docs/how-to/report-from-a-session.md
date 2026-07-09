# How to report from a Claude session

Four commands let an agent — or a human, or a script — report from **any**
Claude Code session over NATS, holding nothing but a `.creds` file:

```bash
ape event status --payload '{"phase":"implement","pct":60}'  # a progress event
ape log info "migration step 3 complete"                     # a structured log line
ape metrics                                                  # scan + publish this session's usage
ape transcript upload                                        # blob-upload this session's transcript set
```

Every message carries the **user identity decoded from your NATS credential**
(baked into the subject, server-enforceable) and the **Claude Code session id**
(auto-resolved or passed explicitly). This is the same machinery the `ape
pipeline` / `ape task` runners use at finalize, so a supervised run and a
self-reporting agent are indistinguishable to a consumer.

Unlike the fire-and-forget runner taps, these commands are *for* reporting, so
**failure is visible**: exit `0` published/uploaded · `1` NATS publish/upload
failed (connection was fine) · `2` usage error, no NATS configured, or the
session couldn't be resolved.

## Configure NATS

Flags or env vars (flags win):

```bash
export APE_NATS_URL=nats://nats.example:4222
export APE_NATS_CREDS=~/.config/ape/user.creds
```

With no URL configured, the commands exit `2` — there is no silent no-op (they
exist to report). There is **no project-config layer**; credential paths never
land in committed config.

## The four commands

```bash
# A caller-named progress event. <event> is validated [a-z0-9-]+.
# --payload is JSON inline, @file, or "-" for stdin.
ape event build-green
ape event status --payload '{"phase":"implement","pct":60}'
echo '{"pr":42}' | ape event pr-opened --payload -

# A structured log record. Level ∈ debug|info|warn|error; repeatable --field.
ape log warn "retrying upstream" --field attempt=2 --field endpoint=api

# Scan the session set (main + sub-agents) and publish a usage snapshot.
# The payload carries per-model tokens + timestamps, so a consumer can reprice
# against Claude Code API rates at any moment (per_model tokens × table = cost).
ape metrics
ape metrics --output-format json          # result object on stdout, diagnostics on stderr

# Upload the transcript set as content-addressed, zstd blobs (idempotent), then
# publish a companion transcript-uploaded event with the digest map.
ape transcript upload
ape transcript upload --store uri-offload
```

Subjects (see [events.md](../reference/events.md) for the full contract):

| Command | Subject |
| --- | --- |
| `ape event <e>` | `ape.evt.<user>.<project>.session.<session-id>.<e>` |
| `ape log <lvl>` | `ape.log.<user>.<project>.<session-id>.<lvl>` |
| `ape metrics` | `ape.metrics.<user>.<project>.<session-id>` |
| `ape transcript upload` | blobs + `ape.evt.<user>.<project>.session.<session-id>.transcript-uploaded` |

## Which session? (resolution order)

Each command resolves the target session in this order, first match wins:

1. `--session-id <uuid>` — explicit.
2. `--transcript <path>` — explicit transcript file; the id is parsed from its name.
3. `APE_SESSION_ID` (env) — set by ape's own runners inside a supervised run, or
   by a hook/wrapper in a plain session.
4. **Auto-detect** — the newest transcript for the current project
   (`~/.claude/projects/<cwd-slug>/`, falling back to matching the recorded `cwd`).

`ape metrics` and `ape transcript` need the transcript on disk; `ape event` and
`ape log` need only the id.

### Recommended agent setup

Auto-detect is heuristic (newest wins) and can misattribute when two sessions
run concurrently in one project. For reliable self-reporting, export the id from
a Claude Code **SessionStart hook** so every `ape` call in that session resolves
deterministically:

```bash
# .claude/settings.json SessionStart hook → writes APE_SESSION_ID for the session.
export APE_SESSION_ID="$CLAUDE_SESSION_ID"
```

Inside an ape-supervised run this is unnecessary — the runner already exports the
NATS config into the Claude child's environment, and the child's own transcript
is the newest, so a nested `ape event` resolves correctly with no flags.

## Server-enforced identity

The `<user>` token is decoded from the `name` claim of the user JWT in your
`.creds` file (slugged: lowercased, `.`/`*`/`>`/whitespace → `-`; it falls back
to the user public key). The full name + public key are also in every payload's
`user` block.

Because the token derives deterministically from the credential, scope a user's
**publish permission to `ape.*.<token>.>`** and the identity in every subject is
enforced by the server, not merely self-reported. With
[`nsc`](https://github.com/nats-io/nsc):

```bash
# user "alice" → token "alice"; scope her to her own subtree across all roots.
nsc add user alice \
  --allow-pub 'ape.evt.alice.>' \
  --allow-pub 'ape.log.alice.>' \
  --allow-pub 'ape.metrics.alice.>' \
  --allow-pub 'ape.blob.uri-request' \
  --allow-pub '_INBOX.>' --allow-sub '_INBOX.>'
```

Any attempt to publish under another token (a forged subject) is rejected by the
server, and the command exits `1`. Issue **unique** `name` claims per credential
— two creds with the same name map to the same token.

## Notes

- **stdout is the result object only.** In `--output-format json`, stdout carries
  a clean result envelope; all NATS diagnostics go to stderr.
- **`ape metrics` republishing is idempotent** — consumers key on
  `(session_id, ts)`. `ape transcript upload` is content-addressed, so a re-run
  is a cheap dedup no-op (each result entry is marked `existed`).
- **`ape metrics --run-id <id>`** publishes a completed run's manifest totals
  instead of a live session scan.

## See also

- [events.md](../reference/events.md) — the frozen subject/payload contract.
- [How to publish run progress to NATS](publish-progress-to-nats.md) — the
  supervised-run event stream.
- [How to upload transcripts](upload-transcripts.md) — transcript-blob details.
