#!/usr/bin/env python3
"""Docs link + reachability checker (PLAN-9 F4).

Two gates over a docs/ tree of Markdown:

  1. No dead relative links — every `[text](target)` that points at a
     local path (not http(s)://, mailto:, or a bare #anchor) must resolve
     to a file that exists. A link to a directory (or a path ending in
     `/`) resolves to that directory's README.md.
  2. No orphans — every .md file under the root must be reachable from
     the root README.md by following relative links (directory links
     count as links to that directory's README.md).

Links inside fenced code blocks (``` or ~~~) are ignored, so usage
examples and sample paths don't register as links.

Usage: check-docs-links.py <docs-dir>
Exit 0 when clean; exit 1 with a report otherwise.
"""

from __future__ import annotations

import os
import re
import sys

LINK_RE = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
FENCE_RE = re.compile(r"^\s*(```|~~~)")


def is_external(target: str) -> bool:
    return (
        target.startswith(("http://", "https://", "mailto:", "tel:"))
        or target.startswith("#")
    )


def extract_links(path: str) -> list[str]:
    """Return the relative link targets in a Markdown file, skipping
    fenced code blocks and external / anchor-only links. Fragments and
    query strings are stripped."""
    links: list[str] = []
    in_fence = False
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            if FENCE_RE.match(line):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            for raw in LINK_RE.findall(line):
                target = raw.strip().split()[0]  # drop optional "title"
                if is_external(target):
                    continue
                target = target.split("#", 1)[0].split("?", 1)[0]
                if target:
                    links.append(target)
    return links


def resolve(src_file: str, target: str) -> str:
    """Resolve a link target to an absolute filesystem path, mapping a
    directory (or trailing-slash) link to its README.md."""
    base = os.path.dirname(src_file)
    dest = os.path.normpath(os.path.join(base, target))
    if os.path.isdir(dest) or target.endswith("/"):
        dest = os.path.join(dest, "README.md")
    return dest


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-docs-links.py <docs-dir>", file=sys.stderr)
        return 2
    root = os.path.normpath(sys.argv[1])
    root_readme = os.path.join(root, "README.md")
    if not os.path.isfile(root_readme):
        print(f"error: {root_readme} not found", file=sys.stderr)
        return 2

    all_md = set()
    for dirpath, _dirs, files in os.walk(root):
        for f in files:
            if f.endswith(".md"):
                all_md.add(os.path.normpath(os.path.join(dirpath, f)))

    dead: list[tuple[str, str]] = []
    # Reachability BFS from the root README, following links as edges.
    reachable = {root_readme}
    queue = [root_readme]
    while queue:
        cur = queue.pop()
        for target in extract_links(cur):
            dest = resolve(cur, target)
            if not os.path.isfile(dest):
                dead.append((cur, target))
                continue
            if dest.endswith(".md") and dest not in reachable:
                reachable.add(dest)
                queue.append(dest)

    # Dead links can also live in files not on the reachable path; scan
    # every doc so a broken link in an orphan is still reported.
    seen_pairs = set(dead)
    for md in sorted(all_md):
        for target in extract_links(md):
            dest = resolve(md, target)
            if not os.path.isfile(dest) and (md, target) not in seen_pairs:
                dead.append((md, target))
                seen_pairs.add((md, target))

    orphans = sorted(all_md - reachable)

    ok = True
    if dead:
        ok = False
        print("Dead relative links:")
        for src, target in sorted(dead):
            print(f"  {src} -> {target}")
    if orphans:
        ok = False
        print("Orphaned docs (not reachable from docs/README.md):")
        for o in orphans:
            print(f"  {o}")

    if ok:
        print(f"docs link-check OK: {len(all_md)} files, all reachable, no dead links.")
        return 0
    return 1


if __name__ == "__main__":
    sys.exit(main())
