#!/usr/bin/env python3
"""Generate the release manifest with checksums for the built helpers."""
import hashlib
import pathlib
import re
import sys


def package(source, dist, tag):
    text = source.read_text()
    match = re.search(r"^version: (.+)$", text, re.M)
    if match is None or tag != "v" + match[1]:
        raise ValueError("release tag must match provider version")

    def asset(match):
        url = match[1]
        if "/releases/download/" + tag + "/" not in url:
            raise ValueError("binary URL must be pinned to the release tag")
        binary = dist / url.rsplit("/", 1)[1]
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        return "      path: " + url + "\n      checksum: " + digest

    text, count = re.subn(r"^      path: (https://[^\n]+)$", asset, text, flags=re.M)
    if count != 5:
        raise ValueError("expected five platform binaries")
    (dist / "provider.yaml").write_text(text)
    files = sorted(dist.glob("harvester-provider-*")) + [dist / "provider.yaml"]
    (dist / "checksums.txt").write_text("".join(hashlib.sha256(p.read_bytes()).hexdigest() + "  " + p.name + "\n" for p in files))


if __name__ == "__main__":
    package(pathlib.Path("provider.yaml"), pathlib.Path("dist"), sys.argv[1])
