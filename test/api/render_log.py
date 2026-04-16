#!/usr/bin/env python3
"""Render raw terminal output to clean text by simulating a terminal."""
import sys
import pyte


def render(input_path, output_path):
    with open(input_path, "rb") as f:
        data = f.read()

    # Strip script(1) header/footer lines
    lines = data.split(b"\n")
    if lines and lines[0].startswith(b"Script started on"):
        lines = lines[1:]
    while lines and (lines[-1].startswith(b"Script done on") or not lines[-1].strip()):
        lines.pop()
    data = b"\n".join(lines)

    screen = pyte.Screen(200, 5000)
    stream = pyte.Stream(screen)
    stream.feed(data.decode("utf-8", errors="replace"))

    result = [l.rstrip() for l in screen.display]
    while result and not result[-1]:
        result.pop()

    with open(output_path, "w") as f:
        for l in result:
            f.write(l + "\n")


if __name__ == "__main__":
    render(sys.argv[1], sys.argv[2])
