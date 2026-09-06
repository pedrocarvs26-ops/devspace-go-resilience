#!/usr/bin/env python3
"""Create an intentionally incompressible Git fixture (>500 MiB by default).
Run only on an isolated test host with at least 3 GiB of free disk space.
This does not start a network listener or change firewall rules.
"""
import argparse
import os
from pathlib import Path
import shutil
import subprocess


def prepare(directory, size_mib=544):
    root = Path(directory).resolve()
    if root.exists():
        raise ValueError("Refusing to overwrite an existing fixture directory")
    if size_mib < 1:
        raise ValueError("size-mib must be positive")
    root.parent.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(root.parent).free < size_mib * 1024 * 1024 * 4:
        raise ValueError("Insufficient disk space (need at least 4x fixture size)")
    root.mkdir()
    source = root / "source"
    source.mkdir()
    def git(*args):
        subprocess.run(["git", "-C", str(source), *args], check=True)
    git("init")
    # Random bytes resist Git/zlib compression; a zero-filled file is not a load test.
    with (source / "payload.bin").open("wb") as output:
        for _ in range(size_mib):
            output.write(os.urandom(1024 * 1024))
    git("add", "payload.bin")
    git("-c", "user.name=Devspace acceptance", "-c", "user.email=acceptance@localhost", "commit", "-m", "Incompressible load fixture")
    subprocess.run(["git", "clone", "--bare", "--no-hardlinks", str(source), str(root / "fixture.git")], check=True)
    subprocess.run(["git", "--git-dir", str(root / "fixture.git"), "update-server-info"], check=True)
    print(f"Fixture ready: {size_mib} MiB in {root}")
    print("Serve from a SEPARATE load-generator host reachable by the devspace machine:")
    print(f"python3 -m http.server 8000 --bind 127.0.0.1 --directory {str(root)!r}")
    print("Change --bind to an appropriate test-network address if needed. Do not expose private repositories.")
    print("Repository URL: http://LOAD_GENERATOR:8000/fixture.git")
    print("Download URL:   http://LOAD_GENERATOR:8000/source/payload.bin")
    return root


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory")
    parser.add_argument("--size-mib", type=int, default=544)
    args = parser.parse_args()
    prepare(args.directory, args.size_mib)
