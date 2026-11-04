#!/usr/bin/env python3
"""Checks that relative links and heading anchors in Markdown files resolve.

Usage:
    python3 -I docs/scripts/check-links.py            # every *.md tracked by git
    python3 -I docs/scripts/check-links.py docs       # only the given files or directories

No dependencies beyond the standard library. Exit status 1 when any link is broken.

What is checked:
  - inline links  [text](target)  and reference definitions  [id]: target
  - relative targets must exist on disk (directories are accepted)
  - "#anchor" targets, with or without a path, must match a heading slug of the target
    Markdown file, using the same slug rule GitHub applies
What is skipped:
  - anything inside fenced code blocks or inline code spans
  - targets with a URL scheme (http:, https:, mailto:, tel:, ...)
"""
from __future__ import annotations

import os
import re
import subprocess
import sys
from urllib.parse import unquote

FENCE_RE = re.compile(r"^(```|~~~)")
INLINE_CODE_RE = re.compile(r"`[^`\n]*`")
INLINE_LINK_RE = re.compile(r"!?\[(?:[^\]]|\\\])*\]\(\s*<?([^)\s>]+)>?(?:\s+\"[^\"]*\")?\s*\)")
REF_DEF_RE = re.compile(r"^\s{0,3}\[[^\]]+\]:\s*<?(\S+?)>?(?:\s+\"[^\"]*\")?\s*$")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
SCHEME_RE = re.compile(r"^[A-Za-z][A-Za-z0-9+.\-]*:")
HTML_ANCHOR_RE = re.compile(r"<a\s+(?:id|name)=\"([^\"]+)\"")


def tracked_markdown_files(root: str) -> list[str]:
    out = subprocess.run(
        ["git", "-C", root, "ls-files", "-z", "--", "*.md", "**/*.md"],
        check=True,
        capture_output=True,
    ).stdout
    return sorted({os.path.join(root, p) for p in out.decode().split("\0") if p})


def collect_files(args: list[str], root: str) -> list[str]:
    if not args:
        return tracked_markdown_files(root)
    files: set[str] = set()
    for arg in args:
        path = os.path.abspath(arg)
        if os.path.isdir(path):
            for dirpath, dirnames, filenames in os.walk(path):
                dirnames[:] = [d for d in dirnames if not d.startswith(".") and d != "node_modules"]
                files.update(os.path.join(dirpath, f) for f in filenames if f.endswith(".md"))
        elif path.endswith(".md"):
            files.add(path)
    return sorted(files)


def strip_code(lines: list[str], inline: bool = True) -> list[str]:
    """Blank out fenced code blocks (and, by default, inline code spans), keeping line numbers.

    Headings keep their inline code because it is part of the slug ("### `maintenance`").
    """
    result: list[str] = []
    in_fence = False
    for line in lines:
        if FENCE_RE.match(line.strip()):
            in_fence = not in_fence
            result.append("")
            continue
        if in_fence:
            result.append("")
        else:
            result.append(INLINE_CODE_RE.sub("", line) if inline else line)
    return result


def slugify(heading: str) -> str:
    """GitHub-style slug: strip markup, lowercase, drop punctuation, spaces become hyphens."""
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", heading)  # [text](url) -> text
    text = re.sub(r"`([^`]*)`", r"\1", text)
    text = re.sub(r"[*_~]+", "", text)
    text = text.strip().lower()
    text = re.sub(r"[^\w\- ]", "", text)
    return text.replace(" ", "-")


def heading_slugs(path: str, cache: dict[str, set[str]]) -> set[str]:
    if path in cache:
        return cache[path]
    slugs: set[str] = set()
    seen: dict[str, int] = {}
    try:
        with open(path, encoding="utf-8") as fh:
            raw = fh.read().splitlines()
    except OSError:
        cache[path] = slugs
        return slugs
    for line in strip_code(raw, inline=False):
        match = HEADING_RE.match(line)
        if match:
            base = slugify(match.group(2))
            count = seen.get(base, 0)
            seen[base] = count + 1
            slugs.add(base if count == 0 else f"{base}-{count}")
    for line in raw:
        for anchor in HTML_ANCHOR_RE.findall(line):
            slugs.add(anchor)
    cache[path] = slugs
    return slugs


def iter_targets(lines: list[str]):
    for number, line in enumerate(strip_code(lines), start=1):
        ref = REF_DEF_RE.match(line)
        if ref:
            yield number, ref.group(1)
            continue
        for match in INLINE_LINK_RE.finditer(line):
            yield number, match.group(1)


def check_file(path: str, root: str, cache: dict[str, set[str]]) -> tuple[int, list[str]]:
    with open(path, encoding="utf-8") as fh:
        lines = fh.read().splitlines()
    checked = 0
    broken: list[str] = []
    rel = os.path.relpath(path, root)
    for number, target in iter_targets(lines):
        if SCHEME_RE.match(target) or target.startswith("//"):
            continue
        checked += 1
        file_part, _, anchor = target.partition("#")
        file_part = unquote(file_part)
        if file_part:
            resolved = os.path.normpath(os.path.join(os.path.dirname(path), file_part))
            if not os.path.exists(resolved):
                broken.append(f"{rel}:{number}: alvo não existe: {target}")
                continue
        else:
            resolved = path
        if anchor:
            if os.path.isdir(resolved):
                broken.append(f"{rel}:{number}: âncora em diretório: {target}")
            elif resolved.endswith(".md") and unquote(anchor).lower() not in heading_slugs(resolved, cache):
                broken.append(f"{rel}:{number}: âncora não encontrada: {target}")
    return checked, broken


def main(argv: list[str]) -> int:
    root = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True
    ).stdout.strip() or os.getcwd()
    files = collect_files(argv, root)
    if not files:
        print("nenhum arquivo .md encontrado", file=sys.stderr)
        return 1
    cache: dict[str, set[str]] = {}
    total_links = 0
    total_broken: list[str] = []
    for path in files:
        checked, broken = check_file(path, root, cache)
        total_links += checked
        total_broken.extend(broken)
    for line in total_broken:
        print(f"quebrado: {line}", file=sys.stderr)
    print(f"{len(files)} arquivos, {total_links} links relativos, {len(total_broken)} quebrados")
    return 1 if total_broken else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
