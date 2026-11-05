#!/usr/bin/env python3
"""Fails when the harness includes a repository file that its Docker build context leaves out.

The harness image (deploy/local/harness.Dockerfile) copies only what its .dockerignore lets in.
A `#[path = "..."]` module or an `include_str!`/`include_bytes!` that reaches outside
services/harness compiles on a checkout and breaks only inside `docker build`, so this check
resolves every such reference and requires it to be allowed by a `!` line of the ignore file.

Usage: tools/ci/check-docker-context.py
"""

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
HARNESS = ROOT / "services" / "harness"
IGNORE = ROOT / "deploy" / "local" / "harness.Dockerfile.dockerignore"
REFERENCE = re.compile(r'(?:#\[path\s*=\s*"|include_(?:str|bytes)!\(\s*")([^"]+)"')


def allowed_paths() -> list[str]:
    return [line[1:].strip().rstrip("/") for line in IGNORE.read_text().splitlines() if line.startswith("!")]


def references() -> list[tuple[pathlib.Path, str]]:
    out = []
    for src in sorted(HARNESS.rglob("*.rs")):
        if "target" in src.parts:
            continue
        for match in REFERENCE.finditer(src.read_text(errors="ignore")):
            target = (src.parent / match.group(1)).resolve()
            try:
                rel = target.relative_to(ROOT).as_posix()
            except ValueError:
                continue
            if not rel.startswith("services/harness/"):
                out.append((src.relative_to(ROOT), rel))
    return out


def main() -> int:
    allowed = allowed_paths()
    missing = [
        f"{src}: {rel}"
        for src, rel in references()
        if not any(rel == a or rel.startswith(a + "/") for a in allowed)
    ]
    if missing:
        print(f"docker-context: fora do contexto de {IGNORE.relative_to(ROOT)}:", file=sys.stderr)
        for line in missing:
            print(f"  {line}", file=sys.stderr)
        print("docker-context: acrescente um '!<caminho>' no .dockerignore e um COPY no Dockerfile", file=sys.stderr)
        return 1
    print(f"docker-context: ok ({len(references())} referência(s) fora do harness, todas no contexto)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
