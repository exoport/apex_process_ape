# How to publish run progress to NATS

`ape pipeline` and `ape task` can stream structured JSON progress events to a
NATS cluster so a remote consumer follows a run live. It is **opt-in per
invocation**, **fire-and-forget** (an unreachable cluster never blocks or fails a
run — it degrades to local-only with one stderr warning), and **off by default**
(no URL configured → nothing is published).

## Turn it on

Point ape at a NATS server. Either flags or env vars work (flags win):

```bash
ape pipeline design --nats-url nats://nats.example:4222 --nats-creds ~/.config/ape/user.creds
# or, for any run in this shell / CI job:
export APE_NATS_URL=nats://nats.example:4222
export APE_NATS_CREDS=~/.config/ape/user.creds
ape task apex-create-prd --agent apex-agent-pm
```

There is **no project-config layer** — NATS settings never live in
`_apex/config.yaml`, so a repo can't turn publishing on for whoever runs in it,
and credential paths never land in committed config.

## Watch the stream

With the [`nats` CLI](https://github.com/nats-io/natscli):

```bash
nats sub 'ape.evt.>'
```

Subjects are `ape.evt.<user>.<project>.<kind>.<id>.<event>` — see
[events.md](../reference/events.md) for the full taxonomy and payload fields. A
run emits `run-start` → per-stage `stage-start`/`stage-end` → per-step
`step-start`/`step-end` (with cost/token telemetry) → `hook` events (live
tool-use) → `commit` → `run-end` (manifest totals + transcript-blob digests).

Filter by subtree — the subject *is* the routing key:

```bash
nats sub 'ape.evt.alice.>'                 # everything user "alice" runs
nats sub 'ape.evt.*.myproject.>'           # one project, any user
nats sub 'ape.evt.*.*.*.*.run-end'         # just run completions
```

## Identity: who published, enforced by the server

The `<user>` token is decoded from the `name` claim of the user JWT inside your
`.creds` file (slugged: lowercased, with `.`/`*`/`>`/whitespace → `-`; it falls
back to the user public key when the name is empty). The full name + public key
are also in every payload's `user` block.

Because the token derives deterministically from the credential, an operator can
issue creds whose **publish permission is scoped to `ape.*.<token>.>`** — then
the identity in the subject is enforced by the NATS server, not merely
self-reported. With `nsc`:

```bash
# user "alice" → token "alice"; scope her publishes to her own subtree.
nsc add user alice \
  --allow-pub 'ape.evt.alice.>' \
  --allow-pub 'ape.log.alice.>' \
  --allow-pub 'ape.metrics.alice.>' \
  --allow-pub '_INBOX.>'
```

A run using alice's creds can only publish under `ape.*.alice.>`; a forged
subject is rejected by the server.

## Notes

- **Nothing hits stdout.** All NATS diagnostics (connect warnings, dropped-event
  counts, upload failures) go to stderr, so `ape task --output-format json`
  stdout stays a clean result envelope.
- **Change the root** with `--events-subject-prefix` if `ape.evt` collides with
  another tenant; everything else in the taxonomy is additive-only.
- **Buffered + drop-on-overflow.** Events ride a bounded in-memory queue drained
  by one goroutine; a burst that outruns the cluster drops events (counted, and
  reported once at run end) rather than slowing the run.

## See also

- [events.md](../reference/events.md) — the frozen subject/payload contract.
- [How to upload transcripts](upload-transcripts.md) — the companion
  content-addressed transcript-blob upload.
