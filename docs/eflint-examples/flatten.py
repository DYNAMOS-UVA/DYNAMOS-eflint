#!/usr/bin/env python3
"""Flatten multi-line eFLINT files into one-phrase-per-line for eflint-repl."""

import sys

def flatten(path: str) -> list[str]:
    phrases: list[str] = []
    current = ""

    with open(path) as f:
        for raw in f:
            line = raw.rstrip("\n")
            stripped = line.lstrip()

            if not stripped or stripped.startswith("//"):
                continue

            is_continuation = line[0] in (" ", "\t")

            if is_continuation and current:
                current += " " + stripped
            else:
                if current:
                    phrases.append(current)
                current = stripped

    if current:
        phrases.append(current)

    return phrases


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(f"usage: {sys.argv[0]} FILE [FILE ...]", file=sys.stderr)
        sys.exit(1)

    for path in sys.argv[1:]:
        for phrase in flatten(path):
            print(phrase)
