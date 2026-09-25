#!/usr/bin/env python3
"""Check the release archives goreleaser built in dist/.

`make snapshot` builds the archives "to verify the archive layout", and until
this script nothing looked at them. So from 2026-07-10 until this check landed,
every release archive carried a FILE named `deploy` next to the `deploy/...`
entries. A goreleaser `files:` entry with a single-file `src` treats `dst` as
the full path, so `src: deploy/policy.yaml, dst: deploy` wrote the policy
under the name `deploy`. A plain `tar x` then failed on every `deploy/...`
entry, and `deploy/policy.yaml` never existed, even though the documented
aped install copies it to /etc/aped/policy.yaml.

Two checks per archive:
  1. it extracts cleanly: no entry is a file where another entry needs a
     directory, and no name appears twice;
  2. it holds what the docs tell an operator to use from it.
"""

import sys
import tarfile
import zipfile
from pathlib import Path

DIST = Path("dist")

# docs/how-to/run-aped.md step 3 installs these from the unpacked archive.
LINUX_REQUIRED = [
    "ape",
    "aped",
    "deploy/policy.yaml",
    "deploy/tmpfiles.d/aped.conf",
    "deploy/systemd/aped-priv.socket",
    "deploy/systemd/aped.service",
    "deploy/systemd/aped-front.service",
]


def entries(path: Path) -> list[tuple[str, bool]]:
    """(name, is_dir) for every member."""
    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as z:
            return [(i.filename.rstrip("/"), i.is_dir()) for i in z.infolist()]
    with tarfile.open(path) as t:
        return [(m.name.rstrip("/"), m.isdir()) for m in t.getmembers()]


def problems(path: Path) -> list[str]:
    members = entries(path)
    out = []
    names = [n for n, _ in members]
    seen = set()
    for n in names:
        if n in seen:
            out.append(f"{n}: listed twice")
        seen.add(n)
    files = {n for n, is_dir in members if not is_dir}
    for n in names:
        parts = n.split("/")
        for i in range(1, len(parts)):
            parent = "/".join(parts[:i])
            if parent in files:
                out.append(f"{n}: its parent {parent!r} is a FILE in the archive, so it cannot extract")
    if "_linux_" in path.name:
        for want in LINUX_REQUIRED:
            if want not in files:
                out.append(f"{want}: missing (docs/how-to/run-aped.md uses it from the archive)")
    elif "ape" not in files and "ape.exe" not in files:
        out.append("ape: binary missing")
    return out


def main() -> int:
    archives = sorted([*DIST.glob("*.tar.gz"), *DIST.glob("*.zip")])
    if not archives:
        print("check-archives: no archives in dist/ — run the snapshot first", file=sys.stderr)
        return 1
    failed = False
    for a in archives:
        found = problems(a)
        if found:
            failed = True
            print(f"check-archives: {a.name}:", file=sys.stderr)
            for p in found:
                print(f"  {p}", file=sys.stderr)
    if failed:
        return 1
    print(f"check-archives: {len(archives)} archive(s) extract cleanly and carry what the docs install")
    return 0


if __name__ == "__main__":
    sys.exit(main())
