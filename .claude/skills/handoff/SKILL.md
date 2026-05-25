---
name: handoff
description: 'Compact the current conversation into a self-resumable handoff document so a fresh agent can continue the work, or produce a context block for injection into another session. Use when the user says "/handoff", "write a handoff", "prepare a handoff doc", or "save context for the next session".'
argument-hint: "What will the next session focus on? Append --context to produce a context-injection variant."
---

# Handoff

## Overview

Compact the current conversation into a Markdown handoff document saved under `_output/handoffs/`. The document is either **self-resumable** (default — a fresh agent reading the file is instructed to continue the work) or a **context block** (compact summary intended to be attached/pasted as context into another session).

## On Activation

> **Path resolution:** All paths in this skill (e.g., `_output/handoffs/`) are relative to the **project working directory** (repository root), NOT relative to `${CLAUDE_SKILL_DIR}`. Never read from, write to, or create files inside the skill directory.

## Commit Policy

This skill is **not** responsible for committing its own output. The caller (the user running this skill solo) owns all commits.
Do not commit on your own initiative. Commit only if the user explicitly asks (e.g. "commit the handoff"). Leaving the handoff file uncommitted is the default — handoffs are session artifacts and are typically left out of source control.

## CRITICAL RULES

- MANDATORY: Execute ALL steps in the EXECUTION section IN EXACT ORDER
- DO NOT skip steps or change the sequence
- HALT immediately when halt-conditions are met
- Each action within a step is a REQUIRED action to complete that step
- DO NOT pre-scan the repository tree. Only cite artifacts that were actually surfaced in the current conversation (read, edited, or returned by tool results).
- DO NOT restate the content of any cited artifact. Reference it by path only.
- DO NOT fabricate `suggested_skills`. Omit the section entirely when nothing concrete points to a next skill.
- General rule: only use the Bash tool to execute shell commands. Do not use any other tool for command execution.

## EXECUTION

### Step 1: Parse Arguments and Determine Mode

- Read `$ARGUMENTS`.
- Detect the `--context` flag (also accept `--as-context`). Set `{mode}`:
  - `--context` present → `{mode}` = `context`
  - otherwise → `{mode}` = `resume`
- Remove the flag from `$ARGUMENTS`; the remainder is `{next_focus}` (free-form description of what the next session will focus on).
- If `{next_focus}` is empty: do not halt. Ask the user one line: "What will the next session focus on? (one sentence)" and capture the answer as `{next_focus}`.

### Step 2: Derive Topic, Slug, Title, and Timestamp

- Identify `{session_topic}` — 2-5 content words describing what THIS conversation actually worked on (the build / fix / investigation / decision that happened). Source from session memory only: files touched, decisions made, work produced. NOT from `$ARGUMENTS`. Examples: `handoff-skill-build`, `sprint-planning-lint-fix`, `validator-cli-investigation`.
- Classify `{next_focus}` as **meta-only** or **topical**:
  - **Meta-only** = reduces to nothing but stop words and these meta markers (case-insensitive): `continue, save, prepare, write, capture, handoff, doc, document, session, context, next, clear, after, before, for, the, to`. Examples: "to continue working after context clear", "for the next session", "save context before /clear", "prepare handoff doc".
  - **Topical** = carries at least one content word tied to the work. Example: "validate the redaction logic next" → topical ("validate", "redaction logic").
- Compose `{slug}` (kebab-case, lowercase, content words only, no stop words, ≤60 chars, truncate by trailing words — never mid-word):
  - `{next_focus}` meta-only → `{slug}` = `{session_topic}`.
  - `{next_focus}` topical → `{slug}` = `{session_topic}` joined with the topical content words of `{next_focus}` (dedup repeated words, cap at 60 chars).
  - Both vacuous (no clear session topic AND meta-only arg) → `{slug}` = `session-{timestamp}` and inform the user the slug fell back because no topical content was detected.
- Stop words to strip before composition: `a, an, the, to, for, of, in, on, at, by, as, after, before, with, from, and, or, but`.
- Set `{title}` = Title Case of `{slug}` with hyphens replaced by spaces (e.g., `handoff-skill-build` → `Handoff Skill Build`). Used in the H1 and Resume Protocol echo for readable prose.
- Set `{timestamp}` = current local time formatted as `YYYYMMDDHHMMSS` (no separators, second precision).
- Set `{filename}`:
  - `{mode}` = `resume` → `{timestamp}-{slug}-handoff.md`
  - `{mode}` = `context` → `{timestamp}-{slug}-context.md`
- Set `{handoff_path}` = `_output/handoffs/{filename}`.

### Step 3: Gather In-Context References

- From the current conversation context only (NOT by globbing the filesystem), enumerate paths that were read, edited, or returned by tool results during this session. Apply these filters:
  - Paths under the repository's source tree, docs, development, planning, or output folders — list each with a one-line role tag (`read`, `edited`, `produced`).
  - Commit SHAs, PR numbers, issue numbers mentioned in the session.
  - Output documents created or modified during the session.
- If no such references exist, the References section will be omitted.

### Step 4: Capture Git Context

- Use the Bash tool to capture: current branch name, short HEAD SHA, `git status -s` short output, last 5 commit subjects (`git log -n 5 --oneline`).
- Capture each command's output into a variable. Do not chain commands; run each as a separate Bash call.
- If the working directory is not a git repository, record `git_context = none` and proceed.

### Step 5: Identify Suggested Skills (Conditional)

- Determine whether the session ended with a clear, named follow-up action that maps to a registered skill (e.g., the user said "next we should run /foo", or the conversation explicitly deferred work to a specific skill).
- For each candidate, verify the directory exists at `.claude/skills/{skill-name}/` (project-level) or `~/.claude/skills/{skill-name}/` (user-level) before including it.
- If zero candidates pass the bar, set `{suggested_skills}` = empty and OMIT the Suggested Skills section AND the `suggested_skills` frontmatter key from the handoff document.
- If at least one candidate qualifies, build a list of `{name, why}` entries where `why` is one short sentence tied to this session's content.

### Step 6: Compose the Handoff Document

Build the document in memory with the structure below. Section selection depends on `{mode}`.

Frontmatter (always):

```yaml
---
created_at: {ISO-8601 local timestamp}
slug: {slug}
title: "{title}"
mode: {mode}
next_focus: "{next_focus}"
branch: {branch or "none"}
head_sha: {short SHA or "none"}
suggested_skills: # ONLY if Step 5 produced entries
  - name: {skill-name}
    why: {one sentence}
---
```

Document body sections, in order:

1. H1: `# Handoff: {title}`
2. **Resume Protocol** (only when `{mode}` = `resume`) — a blockquote that instructs the reading agent how to behave when it opens this file:

   ```text
   > **Resume Protocol** — You are reading a handoff document. Treat it as restored session context.
   >
   > 1. Read the entire document before responding.
   > 2. Acknowledge the restored context in one sentence ("Resuming session: {title}").
   > 3. If `suggested_skills` is non-empty, ask whether to start with the first listed skill.
   > 4. Otherwise, present the open threads below and ask which to pick up.
   > 5. Do NOT re-summarise the document back to the user — they wrote it. Just confirm and proceed.
   ```

3. **Next Focus** — one paragraph expanding `{next_focus}`.
4. **Current State** — 1-5 bullets capturing where the session left off (in-flight vs done). High signal only.
5. **Open Threads** — bulleted list of decisions deferred, unresolved questions, known blockers.
6. **Git Context** — branch, short SHA, working tree status, last 5 commit subjects (from Step 4). Omit if `git_context = none`.
7. **References** (only if Step 3 produced entries) — bulleted list of paths and commit/PR identifiers with the role tag from Step 3. Do NOT restate their content.
8. **Suggested Skills** (only if Step 5 produced entries) — ordered list of `N. {skill-name} — {why}`.
9. **Out of Scope** — what the next session should NOT pick up.

Do NOT include any other sections. Keep the document under ~300 lines.

### Step 7: Redact Sensitive Information

Scan the assembled document for the following classes of content and redact each match by replacing the value with `[REDACTED]`:

- API keys, OAuth tokens, bearer tokens, JWT strings
- Passwords, secrets, private keys, SSH key material
- Contents of `.env` values when literal
- Email addresses (replace with `[REDACTED-EMAIL]`)
- Customer or end-user personally identifiable information

Do NOT redact: public file paths, commit SHAs, branch names, framework convention names, configuration keys.

### Step 8: Write the Handoff File

- Use the Bash tool to ensure `_output/handoffs/` exists (`mkdir -p _output/handoffs`).
- Use the Write tool to write `{handoff_path}` with the assembled document.

### Step 9: Report Completion

Resolve `{handoff_abs_path}` = absolute path of `{handoff_path}` using a single Bash call (`realpath {handoff_path}`). Capture the output.

Display a completion summary to the user with two sections.

**Summary:**

- Handoff file: `{handoff_path}` (CWD-relative)
- Mode: `{mode}`
- Title: `{title}`
- Next focus: `{next_focus}`
- Suggested skills: count, or "none"

**Continuation Prompt:** render a ready-to-copy fenced code block containing a SINGLE line. After clearing the context (`/clear`), the user pastes this line and the fresh agent will pick up where this session left off.

When `{mode}` = `resume`, render exactly:

```text
Read {handoff_abs_path} and follow the Resume Protocol inside it.
```

When `{mode}` = `context`, render exactly:

```text
Read {handoff_abs_path} and add it to the context of this session. Do not re-summarize it; wait for my next instruction.
```

Precede the code block with a one-line instruction such as: "Copy-paste this after `/clear` to continue:". Do not add any other prose inside or after the code block — the line must be paste-ready as-is.

Return to the calling process after presenting the summary.
