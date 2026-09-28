#!/usr/bin/env python3
"""Fail if Go comments contain plan/design artifact markers.

Comments should explain behavior. Track acceptance criteria and design
decisions in Jira, design docs, and PRs — not in source comments.

Forbidden examples: "D4:", "per D12", "AC5", "per design §4.2".
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

SCAN_ROOTS = ("api", "cmd", "internal", "pkg", "test")
SKIP_NAME_SUFFIX = ".gen.go"

# Lines that look like Go comments (line comments or block-comment body).
COMMENT_LINE = re.compile(r"^\s*(?://|/\*|\*)")

# Forbidden markers inside those comment lines.
FORBIDDEN = re.compile(
    r"(?:"
    r"D\d+:"  # // D4: ...
    r"|per D\d+"
    r"|decision D\d+"
    r"|Locked: D"
    r"|\bAC\d+\b"  # AC4, AC5, ...
    r"|design §"
    r"|per design"
    r"|Required by design"
    r")"
)


def iter_go_files(root: Path):
    for name in SCAN_ROOTS:
        base = root / name
        if not base.is_dir():
            continue
        for path in base.rglob("*.go"):
            if path.name.endswith(SKIP_NAME_SUFFIX):
                continue
            yield path


def main() -> int:
    hits: list[str] = []
    for path in iter_go_files(ROOT):
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            text = path.read_text(encoding="utf-8", errors="replace")
        rel = path.relative_to(ROOT)
        for lineno, line in enumerate(text.splitlines(), start=1):
            if not COMMENT_LINE.search(line):
                continue
            if FORBIDDEN.search(line):
                hits.append(f"{rel}:{lineno}:{line.rstrip()}")

    if hits:
        print(
            "error: Go comments must not cite plan/decision tags or design-doc section IDs.",
            file=sys.stderr,
        )
        print(
            "Explain behavior in comments; keep AC/D/design-§ references in Jira, design docs, or PRs.",
            file=sys.stderr,
        )
        print(file=sys.stderr)
        for hit in hits:
            print(hit, file=sys.stderr)
        print(file=sys.stderr)
        print("See hack/check_comment_artifacts.py", file=sys.stderr)
        return 1

    print("OK: no plan/design artifact markers in Go comments.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
