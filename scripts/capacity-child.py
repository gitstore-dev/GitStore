#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors
"""Run one owned capacity child with a finite deadline and process-group cleanup."""

import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


def main():
    seconds, log, *command = sys.argv[1:]
    timeout = float(seconds)
    if not math.isfinite(timeout) or not 0 < timeout <= 7200 or not command:
        raise ValueError("a finite deadline up to two hours and a command are required")
    child = None
    stopped = False

    def stop(_signum=None, _frame=None):
        nonlocal stopped
        if _signum is not None:
            stopped = True
        if child is not None:
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    with Path(log).open("wb") as output:
        child = subprocess.Popen(command, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            deadline = time.monotonic() + timeout
            while True:
                if stopped or time.monotonic() >= deadline:
                    result = 143 if stopped else 124
                    break
                try:
                    result = child.wait(timeout=max(0, min(1, deadline - time.monotonic())))
                    break
                except subprocess.TimeoutExpired:
                    continue
        finally:
            stop()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait()
            # The leader can exit before a native Git grandchild. Its group
            # is exclusively owned by this launch and must not outlive it.
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
    return result if result >= 0 else 128 - result


if __name__ == "__main__":
    sys.exit(main())
