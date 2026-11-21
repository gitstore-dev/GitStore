#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

"""Build/verify owned capacity artifacts using the repository's pinned toolchains."""

import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys


MODULES = {
    "api": ("gitstore-api", "./cmd/server"),
    "controller": ("gitstore-controller-manager", "./cmd/controller"),
    "git": ("gitstore-git-service", "git-service"),
}


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args])


def clean_source(root):
    if git(root, "status", "--porcelain", "--untracked-files=normal").strip():
        raise ValueError("capacity builds/verification require a clean source checkout")
    return git(root, "rev-parse", "HEAD").decode().strip()


def source_digest(root, role):
    module, _ = MODULES[role]
    paths = sorted(
        p.decode()
        for p in git(
            root, "ls-files", "-z", "--", module, "shared", "go.work", "go.work.sum",
            "Cargo.toml", "Cargo.lock", "rust-toolchain.toml", ".cargo", "schemas",
        ).split(b"\0") if p
    )
    manifest = f"{module}/" + ("Cargo.toml" if role == "git" else "go.mod")
    if manifest not in paths:
        raise ValueError("tracked component build inputs are missing")
    digest = hashlib.sha256()
    for relative in paths:
        path = root / relative
        info = path.lstat()
        if stat.S_ISLNK(info.st_mode):
            mode, content = "120000", os.readlink(path).encode()
        elif stat.S_ISREG(info.st_mode):
            mode = "100755" if info.st_mode & 0o111 else "100644"
            content = path.read_bytes()
        else:
            raise ValueError("build inputs must be tracked regular files or symlinks")
        entry = hashlib.sha256(content).hexdigest()
        digest.update(f"{len(relative.encode())}:{relative}:{mode}:{entry}\n".encode())
    return digest.hexdigest()


def artifact_digest(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= 256 << 20:
        raise ValueError("artifact must be a nonempty bounded regular file")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest(), info.st_size


def private_parent(path):
    if not path.is_absolute() or path.parent.resolve() != path.parent:
        raise ValueError("capacity artifacts/receipts require canonical absolute paths")
    info = path.parent.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
        raise ValueError("capacity artifact/receipt directory must already be private")


def verify(root, role, artifact, receipt):
    clean_source(root)
    private_parent(artifact)
    private_parent(receipt)
    info = receipt.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or not 0 < info.st_size <= 1 << 20:
        raise ValueError("capacity build receipt must be a private bounded regular file")
    record = json.loads(receipt.read_bytes())
    command = record.get("command", [])
    _, entry = MODULES[role]
    command_matches = command == ["cargo", "build", "--release", "--locked"] if role == "git" else (
        len(command) == 6 and command[:4] == ["go", "build", "-trimpath", "-o"] and command[5] == entry
    )
    kind_matches = record.get("buildKind") == "repository-release"
    if role in ("api", "controller") and record.get("buildKind") == "repository-owned-rbac":
        kind_matches = True
        command_matches = (
            len(command) == 7 and command[:5] == ["go", "test", "-c", "-trimpath", "-o"]
            and command[6] == ("./internal/app" if role == "api" else entry)
        )
    digest, size = artifact_digest(artifact)
    if (
        record.get("schemaVersion") != 1 or record.get("role") != role
        or record.get("artifactSha256") != digest or record.get("artifactBytes") != size
        or record.get("sourceInputsSha256") != source_digest(root, role)
        or not record.get("sourceRevision") or not record.get("toolchain")
        or record.get("profiled") is not False
        or not kind_matches
        or not command_matches
    ):
        raise ValueError("artifact or current component/shared source inputs differ from their build receipt")
    return record


def build(root, role, artifact, receipt, kind="release"):
    if kind not in ("release", "owned-rbac") or kind == "owned-rbac" and role not in ("api", "controller"):
        raise ValueError("owned-rbac only builds the existing explicitly owned API and controller test hosts")
    revision = clean_source(root)
    private_parent(artifact)
    private_parent(receipt)
    if receipt.exists():
        # A previous build may be reused across test/doc-only revisions.
        record = verify(root, role, artifact, receipt)
        if record["buildKind"] != "repository-" + kind:
            raise ValueError("existing artifact uses a different build recipe")
        return
    if artifact.exists() or artifact.is_symlink() or receipt.is_symlink():
        raise ValueError("never adopt or replace an unreceipted or live artifact")
    before = source_digest(root, role)
    module, entry = MODULES[role]
    environment = os.environ.copy()
    if any(environment.get(name) for name in ("GOFLAGS", "GOEXPERIMENT", "RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS")):
        raise ValueError("capacity release builds refuse ambient instrumentation/compiler overrides")
    if role == "git":
        environment["CARGO_TARGET_DIR"] = str(root / module / "target")
        command = ["cargo", "build", "--release", "--locked"]
        toolchain = subprocess.check_output(["rustc", "--version"], cwd=root / module).decode().strip()
    else:
        environment["GOWORK"] = "off"
        command = (
            ["go", "test", "-c", "-trimpath", "-o", str(artifact), "./internal/app" if role == "api" else entry]
            if kind == "owned-rbac" else ["go", "build", "-trimpath", "-o", str(artifact), entry]
        )
        toolchain = subprocess.check_output(["go", "version"], cwd=root / module, env=environment).decode().strip()
    # Reserve exactly the caller-selected, previously absent output path.
    with artifact.open("xb"):
        pass
    os.chmod(artifact, 0o700)
    try:
        subprocess.run(command, cwd=root / module, env=environment, check=True)
        if role == "git":
            with (root / module / "target/release" / entry).open("rb") as source, artifact.open("wb") as target:
                shutil.copyfileobj(source, target)
        os.chmod(artifact, 0o700)
        if clean_source(root) != revision or source_digest(root, role) != before:
            raise ValueError("source changed during capacity build")
        digest, size = artifact_digest(artifact)
        record = {
            "schemaVersion": 1, "role": role, "sourceRevision": revision,
            "sourceInputsSha256": before, "artifactSha256": digest, "artifactBytes": size,
            "toolchain": toolchain, "command": command, "profiled": False,
            "buildKind": "repository-" + kind,
            "builtAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        }
        descriptor = os.open(receipt, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(descriptor, "w") as stream:
            json.dump(record, stream, sort_keys=True)
            stream.flush()
            os.fsync(stream.fileno())
    except BaseException:
        artifact.unlink()
        raise


def main():
    if len(sys.argv) not in (5, 6) or sys.argv[1] not in ("build", "verify") or sys.argv[2] not in MODULES:
        raise ValueError("usage: capacity-build-receipt.py <build|verify> <api|controller|git> <artifact> <receipt> [release|owned-rbac]")
    operation, role, artifact, receipt = sys.argv[1:5]
    root = Path(__file__).resolve().parent.parent
    if operation == "build":
        build(root, role, Path(artifact), Path(receipt), sys.argv[5] if len(sys.argv) == 6 else "release")
    elif len(sys.argv) == 5:
        verify(root, role, Path(artifact), Path(receipt))
    else:
        raise ValueError("verification derives the build recipe from the immutable receipt")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"capacity build receipt: {error}", file=sys.stderr)
        sys.exit(2)
