# The assessor

> APEX v1.5, in development. The reference — subjects, `notice.json`, the flags, the records — is
> [Coordination layout: the assessor](../reference/coordination.md#the-assessor).

Two milestones of one project run at once, each on its own branch, each driven by its own loop. Neither loop sees
the other's work until a take-in or a gate. Most of the time that is fine: two changes to the same file are a merge,
and the take-in handles it. Sometimes it is not: a change that alters an interface the other milestone's plan
consumes, or a file the other milestone's gate runs, means that milestone's next change is built on something that
has already moved. The assessor exists to say so early, as advice.

It answers one question on each batch of coordination events — a plan approved, a change archived, a gate turned
red: **does this affect another open milestone?** The answer is an impact notice to that milestone, delivered to its
conductor, and, at most, a request that a person hold it. The assessor never blocks work. When it is down, over
budget or wrong, the cost is a notice, not a stop.

## A session that can only answer

The assessor is a model session, and its input is text that other milestones' sessions wrote: plans, diffs, change
frontmatter. That is a prompt-injection surface. So the design starts from what the session must not be able to do,
and gives it nothing it does not need:

- **No credential, and no way to publish.** The session never connects to NATS. It writes one file, `notice.json`,
  in its batch folder, and the coordinator process — which holds the ensemble's lease — reads it, judges it and
  publishes it. An earlier plan had the session publish through an `ape` verb, which would have put the
  coordinator's credential within reach of a session reading untrusted text.
- **A snapshot, not a checkout.** The batch folder is a `git archive` of `main`: files only, no `.git`, no `hot/`
  (where the credentials live), and no symbolic links, since a committed link could point a read anywhere on the
  host. A diff comes only from commits on the milestone's own branch, so an event cannot point the session at
  arbitrary content.
- **An allow-list, three times over.** Claude Code's `--tools` limits the session to Read, Grep, Glob and Write. The
  pre-tool hook, registered for every tool, binds those four to the folder (every link resolved, any `..` refused)
  and allows only `notice.json` to be written; it denies anything else, MCP tools included, and records the denial,
  which fails the batch. The inline deny rules name the rest.
- **No code from the snapshot.** `--setting-sources user` keeps the snapshot's own settings hooks from running.
  That also drops project skills, so ape supplies the skill as a plugin — from the coordinator's own checkout, never
  from `main`, and refused if its frontmatter carries hooks (measured: a plugin skill's hooks do run) or its body a
  shell command.

## The coordinator judges, whole

`notice.json` is checked as a whole, and any bad field refuses the whole batch: an unknown key, a target that is
not another open milestone, a reason over 600 characters or matching a secret pattern, evidence that is not a file
of the snapshot or of the input's diffs. Refusing only the bad notice would let a session probe the filter one notice
at a time. A refusal is published as `assessor-failed` with the **rule** broken, never the text: the detail stays in
the coordinator's own record, so the failure is not a way out either.

The reason a session writes is the one piece of free text, and it is typed into a conductor's session as a
delivery. A delivery never grants a person's act, and a notice or a hold request is surfaced to the person and acted
on only by them.

## Fenced, once

Only the coordinator that holds the ensemble's lease may publish, so each publish is fenced with the lease the
coordinator holds at that moment. A token handed out earlier would be stale within seconds, since every renewal moves
the lease's revision; and the coordinator's own renewal between reading its token and checking it is retried once,
so it never fences itself out.

A notice is published at most once. Before publishing, the coordinator reads the last message on that notice's own
subject: a dedupe that never expires, unlike the stream's ten-minute window, which matters because a replaced
coordinator re-runs a batch after the session and the restart have both taken time.

## Advice, not authority

- A **hold request** pauses nothing. Pausing a milestone is a person's act (`ape milestone pause`), taken by the
  person whose checkout holds it.
- Whether a notice was **acted on or dismissed** is recorded by that same person, with `ape coord notice ack`, from
  that checkout and at a terminal. Never the assessor, never a loop, never a standing order. Those counts are what
  decide, later, whether an event type is worth assessing at all.
- The **token line** keeps the assessor from costing more than it is worth: each session is capped, and the
  ensemble has a budget per stage. Past the budget, or when the plan's usage would not allow a launch, a batch is
  skipped and said to be skipped. Nothing waits on the assessor.
- A **replaced coordinator** picks up where its predecessor stopped: the assessor records each batch on the stream
  before running it and marks it done after, so the replacement runs what was left. **A batch whose answer went out
  is not run again**: a second session is a new answer, which could name a target the first did not, so a replayed
  batch with any outcome already published is marked done instead.

## What it does not do yet

- **Answer integrity:** a model session on the coordinator's host can still edit the installed `apex-assess` body.
  That changes what the assessor answers, though it cannot make it run code (the frontmatter and `` !` `` checks).
  The `skill_sha256` in every kept batch record is how a read-out shows which skill ran.
- A notice targets a milestone of the batch's own project: one snapshot is of one project.
- Contracts between milestones (what one consumes of another) come in stage 2; the input says so.
- On a cluster that enforces no permissions (1c's), the coordinator's identity is not checked, so a forged notice
  is not measured there. A coordinator credential with exactly the coord subjects is stage-2 work.
