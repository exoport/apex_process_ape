---
name: release
description: 'Full release workflow for ape: pre-flight checks (clean tree, CHANGELOG, no duplicate tag) → local CI gate (make ci-local) → push main → poll push CI → final tag → poll release workflow → cosign signature verification. Use when the user says "/release", "cut a release", "tag a release", or "ship vX.Y.Z".'
argument-hint: "Optional: version to release (e.g. v0.0.22) and/or the word \"autonomous\" to skip all confirmation gates. Version is detected from CHANGELOG.md if omitted. Order doesn't matter (e.g. \"v0.0.22 autonomous\" or \"autonomous\")."
---

# Release

## Overview

Walk through the complete release flow: pre-flight → local gate → push main → wait for remote CI on the tagged SHA → final tag → wait for the GitHub Release to publish → verify cosign signature.

The rc-tag pre-release gate that earlier versions of this skill used was dropped after the v0.0.21 incident: rc and final annotated tags landed on the same commit, and goreleaser's `git describe`-based tag resolution misrouted the build artifacts to the rc prerelease. The new flow uses the regular push-to-`main` CI run as the remote gate; the final tag goes on the same SHA, but there's no longer a sibling rc tag to confuse goreleaser.

## CRITICAL RULES

- MANDATORY: Execute ALL steps in the EXECUTION section IN EXACT ORDER
- HALT immediately at any HALT-condition; state the reason and what to fix before the user re-runs — this applies identically in autonomous mode, HALT conditions are never skipped, only confirmation gates are
- ASK the user for confirmation at every Phase boundary where specified — never skip a confirmation gate — UNLESS `{autonomous}` is true (Phase 0), in which case skip every confirmation gate (Phases 1h, 3, 5) and proceed straight through, still announcing each step as you take it
- DO NOT push final tags or create GitHub Releases without confirming with the user first, UNLESS the user's invocation explicitly requested autonomous mode (the literal word "autonomous" in the skill arguments) — that is the standing authorization for this run
- DO NOT amend published commits or tags
- DO NOT create rc/pre-release tags (`vX.Y.Z-rcN`). The rc cycle has been removed
- Only use the Bash tool for shell commands
- When polling remote state, sleep between retries; do not busy-loop
- All paths are relative to the repository root (the current working directory)

---

## EXECUTION

### Phase 0 — Parse arguments and gather constants

1. Read `$ARGUMENTS`. Check case-insensitively for the standalone word `autonomous` (e.g. "autonomous", "release autonomous", "v0.0.22 autonomous"). Set `{autonomous}` = true if present, else false. Remove that word from the string before the next check.
2. In what remains of `$ARGUMENTS`, if it matches `v[0-9]+\.[0-9]+\.[0-9]+`, set `{version}` = that value. Otherwise set `{version}` = "" (to be resolved in Phase 1).
3. If `{autonomous}` is true, tell the user up front: "Running in autonomous mode — no confirmation gates, will push main, tag, and publish the release without stopping." This is not a confirmation ask, just a heads-up before Phase 1 starts.
4. Capture the GitHub repo slug:
   ```bash
   git remote get-url origin
   ```
   Parse `{owner}/{repo}` from the URL (handles both HTTPS and SSH forms). Set `{repo_slug}` = `diegosz/apex_process_ape` (verify against the parsed value; fail loud if they differ).
5. Set `{api_base}` = `https://api.github.com/repos/{repo_slug}`.

---

### Phase 1 — Pre-flight checks

Run each check in order. HALT on the first failure and tell the user exactly what to fix.

#### 1a — Clean working tree

```bash
git status --porcelain
```

HALT if output is non-empty. Message: "Working tree is not clean. Commit or stash all changes before releasing."

#### 1b — On main branch

```bash
git rev-parse --abbrev-ref HEAD
```

HALT if not `main`. Message: "Must be on the main branch to release."

#### 1c — main is up to date with remote

```bash
git fetch origin main 2>&1
git rev-list HEAD..origin/main --count
```

HALT if the count is > 0. Message: "Local main is behind origin/main — pull before releasing."

Also check for unpushed commits:
```bash
git rev-list origin/main..HEAD --count
```

If > 0: inform the user there are unpushed commits on main. They will be pushed in Phase 3 (do not push here).

#### 1d — Resolve version from CHANGELOG.md

If `{version}` is still "":
```bash
grep -m1 '^## v[0-9]' CHANGELOG.md
```
Extract the version token (`v0.0.X`) from the first matching line. Set `{version}` = that value.

If no match is found: HALT. Message: "Cannot detect version from CHANGELOG.md. Add a `## vX.Y.Z` entry or pass the version as an argument."

#### 1e — CHANGELOG entry is complete

```bash
grep -m1 "^## ${version}" CHANGELOG.md
```

HALT if not found. Message: "No CHANGELOG.md entry for `{version}`. Add one before releasing."

Check the line does not contain "unreleased" (case-insensitive):
```bash
grep -im1 "^## ${version}" CHANGELOG.md | grep -i unreleased
```

If it matches: HALT. Message: "CHANGELOG.md entry for `{version}` is marked unreleased. Update the date before releasing."

#### 1f — Version not already tagged

```bash
git tag -l "${version}"
```

HALT if output is non-empty. Message: "`{version}` is already tagged. Bump the version in CHANGELOG.md for a new release."

#### 1g — No stale rc/pre-release tags on HEAD

```bash
git tag --points-at HEAD | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-' || true
```

If output is non-empty: HALT. Message: "Pre-release tag(s) point at HEAD: <list>. The rc cycle was dropped — these tags can confuse goreleaser. Delete them (`git tag -d <tag> && git push origin :refs/tags/<tag>`) before releasing."

#### 1h — Report pre-flight summary

Display to the user:

```
Pre-flight checks passed:
  version:   {version}
  repo:      {repo_slug}
  HEAD:      <output of `git rev-parse --short HEAD`>
  CHANGELOG: <first 80 chars of the matching CHANGELOG line>
```

If `{autonomous}` is false: ask "Proceed with `make ci-local`? (this takes ~30–60 s)" — wait for confirmation.

If `{autonomous}` is true: skip the ask, state "Autonomous mode — proceeding with `make ci-local`." and continue immediately.

---

### Phase 2 — Local CI gate

Run the full local gate:

```bash
make ci-local
```

This is a long-running command (30–60 s). Stream output. HALT if the exit code is non-zero. Message: "`make ci-local` failed. Fix the issues and re-run `/release`."

On success inform the user: "Local CI gate passed."

---

### Phase 3 — Push main

If `{autonomous}` is false: ask "Local gate passed. Push main?" — wait for confirmation.

If `{autonomous}` is true: skip the ask, state "Autonomous mode — pushing main." and continue immediately.

```bash
git push origin main
```

HALT on non-zero exit.

Capture the HEAD SHA that will be tagged:
```bash
git rev-parse HEAD
```
Set `{head_sha_full}`. Also record the short form:
```bash
git rev-parse --short HEAD
```
Set `{head_sha}`.

Inform the user: "main pushed to `{head_sha}`. Polling GitHub Actions CI…"

---

### Phase 4 — Poll GitHub Actions CI for the pushed SHA

Poll the GitHub Actions API for the CI workflow run triggered by the push to `main` at `{head_sha_full}`. Retry up to 60 times with 20 s sleep between attempts (20 min total ceiling).

On each iteration:
```bash
curl -sf \
  -H "Accept: application/vnd.github+json" \
  "{api_base}/actions/runs?event=push&branch=main&per_page=10" \
| python3 -c "
import sys, json
runs = json.load(sys.stdin).get('workflow_runs', [])
for r in runs:
    if r.get('name') == 'CI' and r.get('head_sha', '') == '$head_sha_full':
        print(r['status'], r['conclusion'] or '', r['html_url'])
        break
else:
    print('not_found')
"
```

States:
- `not_found` → not yet registered; continue polling
- `queued` or `in_progress` → still running; continue polling
- `completed success` → proceed to Phase 5
- `completed failure` or `completed cancelled` → HALT. Message: "CI failed on main at `{head_sha}`. Fix the issue (push more commits to main), then re-run `/release`."

If the ceiling is hit without completion: HALT. Message: "CI poll timed out after 20 minutes. Check https://github.com/{repo_slug}/actions manually and re-run once it finishes."

On CI success inform the user: "Remote CI passed for `{head_sha}` on main."

---

### Phase 5 — Final tag

If `{autonomous}` is false: ask "CI is green. Create and push the final release tag `{version}`?" — wait for confirmation.

If `{autonomous}` is true: skip the ask, state "Autonomous mode — creating and pushing the final tag `{version}`." and continue immediately.

#### 5a — Extract release notes headline from CHANGELOG.md

```bash
grep -A2 "^## ${version}" CHANGELOG.md | head -3
```

Use the first non-empty line after the heading as the tag message suffix (keep it to one line, ≤80 chars). If nothing found, use `{version}`.

Set `{tag_message}` = `"{version} — {headline}"`.

#### 5b — Create and push final tag

```bash
git tag -a "{version}" -m "{tag_message}"
git push origin "{version}"
```

HALT on non-zero exit.

Inform the user: "Final tag `{version}` pushed at `{head_sha}`. Release workflow is running…"

---

### Phase 6 — Poll for GitHub Release publication

Poll `{api_base}/releases/tags/{version}` every 30 s up to 40 iterations (20 min ceiling).

```bash
curl -sf \
  -H "Accept: application/vnd.github+json" \
  "{api_base}/releases/tags/{version}" \
| python3 -c "
import sys, json
r = json.load(sys.stdin)
print('published', r.get('tag_name',''), r.get('html_url',''))
" 2>/dev/null || echo "not_yet"
```

- `not_yet` → keep polling
- `published <tag> <url>` → if `{tag}` ≠ `{version}` HALT (release landed on the wrong tag — same bug class as v0.0.21). Otherwise proceed to Phase 7.

If ceiling hit: HALT. Message: "Release poll timed out. Check https://github.com/{repo_slug}/releases manually. Run Phase 7 manually once the release is published."

---

### Phase 7 — Cosign signature verification

#### 7a — Check cosign is available

```bash
cosign version 2>/dev/null | head -1
```

If cosign is not found: warn the user with the manual verification command (see below) and skip to Phase 8. Do NOT HALT.

#### 7b — Download release artifacts

```bash
tmp_dir=$(mktemp -d)
base_url="https://github.com/{repo_slug}/releases/download/{version}"
curl -sfL "${base_url}/ape_checksums.txt"     -o "${tmp_dir}/ape_checksums.txt"
curl -sfL "${base_url}/ape_checksums.txt.sig" -o "${tmp_dir}/ape_checksums.txt.sig"
curl -sfL "${base_url}/ape_checksums.txt.pem" -o "${tmp_dir}/ape_checksums.txt.pem"
```

HALT if any curl fails (non-zero exit or empty file). Message: "Failed to download release artifacts. The release may still be uploading — try again in a minute."

#### 7c — Verify signature

```bash
cosign verify-blob \
  --certificate "${tmp_dir}/ape_checksums.txt.pem" \
  --signature   "${tmp_dir}/ape_checksums.txt.sig" \
  --certificate-identity "https://github.com/{repo_slug}/.github/workflows/release.yml@refs/tags/{version}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${tmp_dir}/ape_checksums.txt"
```

HALT on non-zero exit. Message: "cosign verification FAILED. The checksums file signature does not match the expected identity. Do NOT distribute these artifacts."

On success: inform the user "Cosign signature verified OK."

Clean up temp dir:
```bash
rm -rf "${tmp_dir}"
```

---

### Phase 8 — Report completion

Display a final summary:

```
Release complete:
  version:       {version}
  tag:           {version} (commit {head_sha})
  release URL:   https://github.com/{repo_slug}/releases/tag/{version}
  cosign:        verified / skipped (cosign not installed)
```

If cosign was skipped, also display the manual verification command ready to copy-paste:

```bash
cosign verify-blob \
  --certificate ape_checksums.txt.pem \
  --signature   ape_checksums.txt.sig \
  --certificate-identity "https://github.com/{repo_slug}/.github/workflows/release.yml@refs/tags/{version}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  ape_checksums.txt
```
