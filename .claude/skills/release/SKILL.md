---
name: release
description: 'Full release workflow for ape: pre-flight checks (clean tree, CHANGELOG, no duplicate tag) → local CI gate (make ci-local) → rc tag → poll GitHub CI → final tag → poll release workflow → cosign signature verification. Use when the user says "/release", "cut a release", "tag a release", or "ship vX.Y.Z".'
argument-hint: "Optional: version to release (e.g. v0.0.21). Detected from CHANGELOG.md if omitted."
---

# Release

## Overview

Walk through the complete release flow: pre-flight → local gate → rc tag → wait for remote CI → final tag → wait for GitHub Release to publish → verify cosign signature.

## CRITICAL RULES

- MANDATORY: Execute ALL steps in the EXECUTION section IN EXACT ORDER
- HALT immediately at any HALT-condition; state the reason and what to fix before the user re-runs
- ASK the user for confirmation at every Phase boundary where specified — never skip a confirmation gate
- DO NOT push final tags or create GitHub Releases autonomously; always confirm with the user first
- DO NOT amend published commits or tags
- Only use the Bash tool for shell commands
- When polling remote state, sleep between retries; do not busy-loop
- All paths are relative to the repository root (the current working directory)

---

## EXECUTION

### Phase 0 — Parse arguments and gather constants

1. Read `$ARGUMENTS`. If it matches `v[0-9]+\.[0-9]+\.[0-9]+`, set `{version}` = that value. Otherwise set `{version}` = "" (to be resolved in Phase 1).
2. Capture the GitHub repo slug:
   ```bash
   git remote get-url origin
   ```
   Parse `{owner}/{repo}` from the URL (handles both HTTPS and SSH forms). Set `{repo_slug}` = `diegosz/apex_process_ape` (verify against the parsed value; fail loud if they differ).
3. Set `{api_base}` = `https://api.github.com/repos/{repo_slug}`.

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
git fetch origin main --dry-run 2>&1
git rev-list HEAD..origin/main --count
```

HALT if the count is > 0. Message: "Local main is behind origin/main — pull before releasing."

Also check for unpushed commits:
```bash
git rev-list origin/main..HEAD --count
```

If > 0: inform the user there are unpushed commits on main. Ask: "Push these commits to main before tagging? (recommended — the rc tag must point to the same SHA that CI will verify)". Wait for confirmation; do not push autonomously.

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

#### 1g — Determine rc number

```bash
git tag -l "${version}-rc*" | sort -V | tail -1
```

If empty: `{rc_num}` = 1. Otherwise parse the trailing digit(s) from the last rc tag and increment by 1.

Set `{rc_tag}` = `{version}-rc{rc_num}`.

#### 1h — Report pre-flight summary

Display to the user:

```
Pre-flight checks passed:
  version:   {version}
  rc tag:    {rc_tag}
  repo:      {repo_slug}
  HEAD:      <output of `git rev-parse --short HEAD`>
  CHANGELOG: <first 80 chars of the matching CHANGELOG line>
```

Ask: "Proceed with `make ci-local`? (this takes ~30–60 s)" — wait for confirmation.

---

### Phase 2 — Local CI gate

Run the full local gate:

```bash
make ci-local
```

This is a long-running command (30–60 s). Stream output. HALT if the exit code is non-zero. Message: "`make ci-local` failed. Fix the issues and re-run `/release`."

On success inform the user: "Local CI gate passed."

---

### Phase 3 — Push main and rc tag

Ask: "Local gate passed. Push main and create rc tag `{rc_tag}`?" — wait for confirmation.

#### 3a — Push main

```bash
git push origin main
```

HALT on non-zero exit.

#### 3b — Create and push rc tag

```bash
git tag -a "{rc_tag}" -m "{rc_tag} release candidate"
git push origin "{rc_tag}"
```

HALT on non-zero exit.

Capture the SHA the tag points to:
```bash
git rev-parse --short "{rc_tag}^{}"
```
Set `{rc_sha}`.

Inform the user: "rc tag `{rc_tag}` pushed at `{rc_sha}`. Polling GitHub Actions CI…"

---

### Phase 4 — Poll GitHub Actions CI for the rc tag

Poll the GitHub Actions API for the workflow run triggered by `{rc_tag}`. Retry up to 60 times with 20 s sleep between attempts (20 min total ceiling).

On each iteration:
```bash
curl -sf \
  -H "Accept: application/vnd.github+json" \
  "{api_base}/actions/runs?event=push&per_page=10" \
| python3 -c "
import sys, json
runs = json.load(sys.stdin).get('workflow_runs', [])
for r in runs:
    if r.get('head_sha', '').startswith('$rc_sha_full') or r.get('head_branch') == '{rc_tag}':
        print(r['status'], r['conclusion'] or '', r['html_url'])
        break
else:
    print('not_found')
"
```

Where `{rc_sha_full}` is from `git rev-parse "{rc_tag}^{}"`.

States:
- `not_found` → not yet registered; continue polling
- `queued` or `in_progress` → still running; continue polling
- `completed success` → proceed to Phase 5
- `completed failure` or `completed cancelled` → HALT. Message: "CI failed for `{rc_tag}`. Fix the issue, then re-run `/release` (the skill will auto-increment to `{version}-rc{rc_num+1}`)."

If the ceiling is hit without completion: HALT. Message: "CI poll timed out after 20 minutes. Check https://github.com/{repo_slug}/actions manually and re-run once it finishes."

On CI success inform the user: "Remote CI passed for `{rc_tag}`."

---

### Phase 5 — Final tag

Ask: "CI is green. Create and push the final release tag `{version}`?" — wait for confirmation.

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

Inform the user: "Final tag `{version}` pushed. Release workflow is running…"

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
print('published', r.get('html_url',''))
" 2>/dev/null || echo "not_yet"
```

- `not_yet` → keep polling
- `published <url>` → proceed to Phase 7

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
  rc tag:        {rc_tag}
  final tag:     {version}
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
