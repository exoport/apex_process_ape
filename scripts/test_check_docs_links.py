#!/usr/bin/env python3
"""Tests for check-docs-links.py: which links count, and which are code.

Run by `make docs-check`. A link inside code is an example, not a
reference; reading it as one reported CHANGELOG.md's `` [`main.go`](main.go) ``
example as a dead link. The rules mirror internal/mdscan.MapOutsideCode.
"""

from __future__ import annotations

import importlib.util
import os
import sys
import tempfile
import unittest

# Loading the script would otherwise leave a scripts/__pycache__ in the tree.
sys.dont_write_bytecode = True

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("check_docs_links", os.path.join(HERE, "check-docs-links.py"))
cdl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cdl)


def links_in(text: str) -> list[str]:
    with tempfile.TemporaryDirectory() as d:
        path = os.path.join(d, "doc.md")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
        return cdl.extract_links(path)


class ExtractLinks(unittest.TestCase):
    def test_prose_links_count(self):
        self.assertEqual(links_in("see [a](a.md) and [b](b.md#x)\n"), ["a.md", "b.md"])

    def test_inline_code_is_not_a_link(self):
        self.assertEqual(links_in("an example: `[a](a.md)`, then [b](b.md)\n"), ["b.md"])

    def test_double_backtick_span(self):
        self.assertEqual(links_in("``[a](a.md) has a ` in it`` and [b](b.md)\n"), ["b.md"])

    def test_link_labelled_with_code_is_still_a_link(self):
        self.assertEqual(links_in("[`main.go`](main.go)\n"), ["main.go"])

    def test_unclosed_backtick_is_literal(self):
        self.assertEqual(links_in("a ` alone\n\nthen [b](b.md)\n"), ["b.md"])

    def test_span_does_not_cross_a_blank_line(self):
        self.assertEqual(links_in("a `x\n\n[b](b.md) y`\n"), ["b.md"])

    def test_fenced_blocks(self):
        text = "```md\n[a](a.md)\n```\n~~~\n[b](b.md)\n~~~\n[c](c.md)\n"
        self.assertEqual(links_in(text), ["c.md"])

    def test_shorter_fence_inside_a_longer_one(self):
        # The old toggle closed at the inner ``` and read [b] as prose,
        # then reopened at the outer close and hid [c].
        text = "````\n```\n[b](b.md)\n```\n````\n[c](c.md)\n"
        self.assertEqual(links_in(text), ["c.md"])

    def test_fence_with_info_string_does_not_close(self):
        text = "```\n```go\n[a](a.md)\n```\n[c](c.md)\n"
        self.assertEqual(links_in(text), ["c.md"])

    def test_quoted_fence_ends_with_its_quote(self):
        # An unclosed fence in a block quote must not hide the rest of the
        # document: [c] is outside the quote, so it is a link.
        text = "> ```\n> [a](a.md)\n\n[c](c.md)\n"
        self.assertEqual(links_in(text), ["c.md"])


class WholeCheck(unittest.TestCase):
    def test_dead_link_in_code_passes_and_in_prose_fails(self):
        with tempfile.TemporaryDirectory() as d:
            with open(os.path.join(d, "README.md"), "w", encoding="utf-8") as fh:
                fh.write("example: `[x](missing.md)`\n")
            self.assertEqual(run_main(d), 0)
            with open(os.path.join(d, "README.md"), "a", encoding="utf-8") as fh:
                fh.write("real: [x](missing.md)\n")
            self.assertEqual(run_main(d), 1)


def run_main(root: str) -> int:
    import contextlib
    import io

    argv = sys.argv
    sys.argv = ["check-docs-links.py", root]
    try:
        with contextlib.redirect_stdout(io.StringIO()):
            return cdl.main()
    finally:
        sys.argv = argv


if __name__ == "__main__":
    unittest.main()
