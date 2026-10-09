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

Links inside code are ignored, so usage examples and sample paths don't
register as links: fenced blocks (``` or ~~~), and inline code spans,
including a double-backtick span such as ``[x](y.md)``. The rules match
ape's own internal/mdscan (MapOutsideCode), which `ape doc shard` uses: a
fence closes only on the same character, at least as long, with no info
string; a span is N backticks closed by the next run of exactly N in the
same paragraph. A link whose label is code, [`main.go`](main.go), is still
a link. Not handled: four-space indented code blocks.

Usage: check-docs-links.py <docs-dir>
Exit 0 when clean; exit 1 with a report otherwise.
"""

from __future__ import annotations

import os
import re
import sys

LINK_RE = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
QUOTE_RE = re.compile(r"^ {0,3}> ?")
# Stands in for a masked code span: matches no link pattern, and still sits
# inside a label, so [`x`](y.md) remains a link.
CODE_MARK = "\ue000"


def is_external(target: str) -> bool:
    return (
        target.startswith(("http://", "https://", "mailto:", "tel:"))
        or target.startswith("#")
    )


def fence_marker(line: str) -> tuple[str, int, str] | None:
    """(char, run length, info string) when line opens or closes a fence."""
    stripped = line.lstrip(" ")
    if len(line) - len(stripped) > 3 or len(stripped) < 3:
        return None
    char = stripped[0]
    if char not in "`~":
        return None
    n = len(stripped) - len(stripped.lstrip(char))
    if n < 3:
        return None
    info = stripped[n:].strip()
    if char == "`" and "`" in info:
        return None  # inline code, not a fence
    return char, n, info


def unquote(line: str) -> tuple[int, str]:
    """(depth, rest): strip every leading block-quote marker, so `> ````
    opens a fence."""
    depth = 0
    while True:
        m = QUOTE_RE.match(line)
        if not m:
            return depth, line
        depth, line = depth + 1, line[m.end():]


def prose_runs(text: str) -> list[str]:
    """The text outside fenced code blocks, as runs of consecutive lines.

    A fence in a block quote ends when the quote does: a fence cannot
    outlive its container, and an unclosed one would otherwise hide every
    link after it, letting dead links through unseen."""
    runs: list[str] = []
    current: list[str] = []
    fence: tuple[str, int, int] | None = None  # char, length, quote depth
    for line in text.split("\n"):
        depth, inner = unquote(line)
        if fence is not None:
            if depth >= fence[2]:
                marker = fence_marker(inner)
                if marker and marker[0] == fence[0] and marker[1] >= fence[1] and not marker[2]:
                    fence = None
                continue
            fence = None  # its block quote ended, so the fence did too
        marker = fence_marker(inner)
        if marker:
            fence = (marker[0], marker[1], depth)
            if current:
                runs.append("\n".join(current))
                current = []
            continue
        current.append(line)
    if current:
        runs.append("\n".join(current))
    return runs


def mask_code_spans(prose: str) -> str:
    """Replace each inline code span with CODE_MARK."""
    out: list[str] = []
    i = 0
    while i < len(prose):
        if prose[i] != "`":
            out.append(prose[i])
            i += 1
            continue
        n = len(prose[i:]) - len(prose[i:].lstrip("`"))
        end = closing_run(prose, i + n, n)
        if end < 0:
            out.append("`" * n)  # unclosed: literal backticks
            i += n
            continue
        out.append(CODE_MARK)
        i = end + n
    return "".join(out)


def closing_run(s: str, start: int, n: int) -> int:
    """Index of the next run of exactly n backticks before a blank line."""
    limit = s.find("\n\n", start)
    if limit < 0:
        limit = len(s)
    i = start
    while i < limit:
        if s[i] != "`":
            i += 1
            continue
        m = len(s[i:]) - len(s[i:].lstrip("`"))
        if m == n and i + m <= limit:
            return i
        i += m
    return -1


def extract_links(path: str) -> list[str]:
    """Return the relative link targets in a Markdown file, skipping code
    and external / anchor-only links. Fragments and query strings are
    stripped."""
    links: list[str] = []
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    for run in prose_runs(text):
        for raw in LINK_RE.findall(mask_code_spans(run)):
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
