#!/usr/bin/env python3
"""Run govulncheck and gate on the result, allow-listing known-unfixable advisories.

Usage: govulncheck-gate.py <govulncheck-binary> [packages...]

Runs `<govulncheck> -json <packages>` and fails (exit 1) if any CALLED
vulnerability is found whose OSV id is not in ALLOW. Allow-listed ids are
reported but do not fail the build, so the scan stays active — any NEW or
non-allow-listed vulnerability still breaks CI — while tolerating advisories
that have no upstream fix.

In `-json` mode govulncheck exits 0 whether or not vulns are found (non-zero
means the tool itself errored); this gate decides pass/fail from the findings.
"""

import json
import subprocess
import sys

# OSV ids that are known, reachable, and have no upstream fix. Every entry
# MUST carry a justification and be re-reviewed on each dependency bump.
#
# Empty: the sole former entry (GO-2026-5932, golang.org/x/crypto/openpgp) was
# retired once `ape update` dropped github.com/creativeprojects/go-selfupdate
# for a cosign-verifying updater (github.com/sigstore/sigstore-go +
# github.com/minio/selfupdate). openpgp is no longer in ape's compiled build
# graph, so govulncheck no longer flags it.
ALLOW = set()


def called_vulns(stream):
    """Return {osv_id: example "pkg.func" trace} for vulns reachable in the call graph."""
    dec = json.JSONDecoder()
    i, n = 0, len(stream)
    summaries, called = {}, {}
    while i < n:
        while i < n and stream[i].isspace():
            i += 1
        if i >= n:
            break
        obj, i = dec.raw_decode(stream, i)
        osv = obj.get("osv")
        if isinstance(osv, dict) and osv.get("id"):
            summaries[osv["id"]] = (osv.get("summary") or "").strip()
        finding = obj.get("finding")
        if isinstance(finding, dict):
            trace = finding.get("trace") or []
            # A finding is "called" when its most-specific frame names a
            # function (module/package-only findings mean imported-not-called).
            if trace and trace[0].get("function"):
                fid = finding.get("osv", "")
                if fid and fid not in called:
                    fr = trace[0]
                    called[fid] = f'{fr.get("package", "?")}.{fr.get("function", "?")}'
    return called, summaries


def main():
    if len(sys.argv) < 2:
        print("usage: govulncheck-gate.py <govulncheck-binary> [packages...]", file=sys.stderr)
        return 2
    binary = sys.argv[1]
    pkgs = sys.argv[2:] or ["./..."]

    proc = subprocess.run([binary, "-json", *pkgs], capture_output=True, text=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        print(f"govulncheck-gate: govulncheck exited {proc.returncode} (tool error)", file=sys.stderr)
        return proc.returncode or 1

    called, summaries = called_vulns(proc.stdout)
    offending = sorted(k for k in called if k not in ALLOW)
    allowed = sorted(k for k in called if k in ALLOW)

    for k in allowed:
        print(f"ALLOW  {k}: {summaries.get(k, '')}  (called via {called[k]})")
    for k in offending:
        print(f"FAIL   {k}: {summaries.get(k, '')}  (called via {called[k]})")

    if offending:
        print(
            f"\ngovulncheck-gate: {len(offending)} non-allow-listed vulnerability(ies) found — "
            "fix them or, if genuinely unfixable, add to ALLOW with a justification.",
            file=sys.stderr,
        )
        return 1
    print(f"\ngovulncheck-gate: OK — {len(allowed)} allow-listed, 0 offending called vulnerabilities")
    return 0


if __name__ == "__main__":
    sys.exit(main())
