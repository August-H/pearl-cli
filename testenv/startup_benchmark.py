#!/usr/bin/env python3
"""Measure compiled CLI startup on macOS/Linux without user accounts or inference.

Build both revisions with the same flags, then pass their executable paths.
Each sample uses a disposable home, config, database, socket, and workspace.
"""

import argparse
import datetime
import fcntl
import json
import math
import os
from pathlib import Path
import platform
import pty
import select
import statistics
import struct
import subprocess
import tempfile
import termios
import time


def command(binary, args, env, cwd):
    started = time.perf_counter_ns()
    result = subprocess.run(
        [binary, *args], env=env, cwd=cwd, capture_output=True, timeout=30
    )
    elapsed = (time.perf_counter_ns() - started) / 1e6
    if result.returncode:
        raise RuntimeError(result.stderr.decode(errors="replace"))
    return elapsed


def dashboard(binary, env, cwd):
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
    process = None
    try:
        started = time.perf_counter_ns()
        process = subprocess.Popen(
            [binary], env=env, cwd=cwd, stdin=slave, stdout=slave, stderr=slave,
            start_new_session=True,
        )
        output = b""
        deadline = time.monotonic() + 30
        while b"Type a Pearl command" not in output:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([master], [], [], remaining)[0]:
                raise RuntimeError("dashboard did not render its command prompt")
            output += os.read(master, 65536)
            if process.poll() is not None:
                raise RuntimeError(output.decode(errors="replace"))
        elapsed = (time.perf_counter_ns() - started) / 1e6
        os.write(master, b"\x03")
        if process.wait(timeout=10):
            raise RuntimeError("dashboard exited unsuccessfully")
        return elapsed
    finally:
        if process is not None and process.poll() is None:
            process.kill()
            process.wait()
        os.close(master)
        os.close(slave)


def stop(binary, env, cwd):
    command(binary, ["daemon", "stop"], env, cwd)
    deadline = time.monotonic() + 10
    while Path(env["PEARL_SOCKET"]).exists():
        if time.monotonic() >= deadline:
            raise RuntimeError("daemon did not stop")
        time.sleep(0.001)


def sample(binary):
    # /tmp keeps Unix socket paths below the OS length limit on macOS.
    with tempfile.TemporaryDirectory(prefix="pearl-startup-", dir="/tmp") as root:
        config = Path(root, "config")
        config.mkdir()
        home = Path(root, "home")
        home.mkdir()
        env = {
            "HOME": str(home), "USERPROFILE": str(home),
            "XDG_CONFIG_HOME": str(home / "config"),
            "PATH": "/usr/bin:/bin", "TERM": "xterm-256color", "NO_COLOR": "1",
            "PEARL_CONFIG_DIR": str(config), "PEARL_SOCKET": str(config / "p.sock"),
            "PEARL_CLAUDE_PATH": str(home / "missing-claude"),
            "PEARL_ANTIGRAVITY_PATH": str(home / "missing-antigravity"),
        }
        # Keep catalog refresh subprocesses out of the measurement. No account
        # is connected and subscription checks never send a model prompt.
        (config / "model_catalogs.json").write_text(json.dumps({
            "fetched_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "catalogs": [{"provider": "openrouter", "models": [{"id": "openrouter/free"}]}],
        }))
        timings = {}
        try:
            timings["help"] = command(binary, ["--help"], env, root)
            timings["version"] = command(binary, ["version"], env, root)
            timings["daemon_start_fresh"] = command(binary, ["daemon", "start"], env, root)
            timings["dashboard_warm"] = dashboard(binary, env, root)
            stop(binary, env, root)
            timings["daemon_start_existing"] = command(binary, ["daemon", "start"], env, root)
            stop(binary, env, root)
            timings["dashboard_cold"] = dashboard(binary, env, root)
        finally:
            stop(binary, env, root)
        return timings


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binaries", nargs="+", type=lambda p: str(Path(p).resolve()))
    parser.add_argument("--samples", type=int, default=30)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.samples < 1:
        parser.error("--samples must be positive")
    # Warm each executable once, then alternate order to reduce ordering bias.
    for binary in args.binaries:
        sample(binary)
    results = {binary: [] for binary in args.binaries}
    for index in range(args.samples):
        order = args.binaries if index % 2 == 0 else list(reversed(args.binaries))
        for binary in order:
            results[binary].append(sample(binary))
    report = {"platform": platform.platform(), "samples": args.samples, "results": {}}
    for binary, samples in results.items():
        summary = {}
        for scenario in samples[0]:
            values = sorted(s[scenario] for s in samples)
            summary[scenario] = {
                "median_ms": round(statistics.median(values), 3),
                "p95_ms": round(values[math.ceil(len(values) * 0.95) - 1], 3),
                "samples_ms": values,
            }
        report["results"][binary] = summary
    encoded = json.dumps(report, indent=2) + "\n"
    if args.output:
        args.output.write_text(encoded)
    print(encoded)


if __name__ == "__main__":
    main()
