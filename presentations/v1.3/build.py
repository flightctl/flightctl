#!/usr/bin/env python3
"""Inline the CSS, JS, and images of deck.html into one portable HTML file.

Usage:  ./build.py  [output.html]
Default output: flight-control-deck.html
"""

import base64
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent


def data_uri(path: pathlib.Path) -> str:
    mime = {
        ".png": "image/png",
        ".jpg": "image/jpeg",
        ".jpeg": "image/jpeg",
        ".svg": "image/svg+xml",
        ".gif": "image/gif",
        ".webp": "image/webp",
        ".mp4": "video/mp4",
        ".webm": "video/webm",
    }[path.suffix.lower()]
    return f"data:{mime};base64," + base64.b64encode(path.read_bytes()).decode()


def main() -> int:
    out = HERE / (sys.argv[1] if len(sys.argv) > 1 else "flight-control-deck.html")
    html = (HERE / "deck.html").read_text()

    html = html.replace(
        '<link rel="stylesheet" href="deck.css">',
        "<style>\n" + (HERE / "deck.css").read_text() + "\n</style>",
    )
    html = html.replace(
        '<script src="deck.js"></script>',
        "<script>\n" + (HERE / "deck.js").read_text() + "\n</script>",
    )

    # Inline every local src="..." asset (logos, and a demo video if present).
    missing = []

    def sub(match: "re.Match[str]") -> str:
        ref = match.group(2)
        if ref.startswith(("http://", "https://", "data:")):
            return match.group(0)
        path = HERE / ref
        if not path.exists():
            missing.append(ref)
            return match.group(0)
        return f'{match.group(1)}="{data_uri(path)}"'

    html = re.sub(r'(\bsrc)="([^"]+)"', sub, html)

    out.write_text(html)

    for ref in sorted(set(missing)):
        print(f"warning: referenced asset not found, left as-is: {ref}", file=sys.stderr)

    print(f"wrote {out}  ({out.stat().st_size / 1024:.0f} KB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
