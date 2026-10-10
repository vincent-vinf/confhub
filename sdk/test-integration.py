#!/usr/bin/env python3
"""Run SDK and server suites against a disposable database and two instances."""

import argparse
import contextlib
import os
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def free_port():
    with socket.socket() as connection:
        connection.bind(("127.0.0.1", 0))
        return connection.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--full", action="store_true", help="also run full server tests, race checks and vet"
    )
    parser.add_argument("--python", default=str(ROOT / "sdk/python/.venv/bin/python"))
    options = parser.parse_args()
    if not Path(options.python).is_file():
        parser.error("install sdk/python[dev] in sdk/python/.venv first, or pass --python")
    name = "confhub-sdk-test-" + secrets.token_hex(6)
    password = secrets.token_hex(16)
    env = dict(os.environ)
    for key in list(env):
        if key.startswith("CONFHUB_"):
            del env[key]
    processes = []
    try:
        subprocess.run(
            [
                "docker",
                "run",
                "-d",
                "--name",
                name,
                "-p",
                "127.0.0.1::5432",
                "-e",
                "POSTGRES_DB=confhub",
                "-e",
                "POSTGRES_USER=confhub",
                "-e",
                "POSTGRES_PASSWORD=" + password,
                "registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17",
            ],
            check=True,
            stdout=subprocess.DEVNULL,
        )
        port = (
            subprocess.check_output(["docker", "port", name, "5432/tcp"], text=True)
            .strip()
            .rsplit(":", 1)[1]
        )
        for _ in range(100):
            result = subprocess.run(
                ["docker", "exec", name, "pg_isready", "-U", "confhub", "-d", "confhub"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            if result.returncode == 0:
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("temporary database failed to start")
        env["CONFHUB_DSN"] = (
            f"postgres://confhub:{password}@127.0.0.1:{port}/confhub?sslmode=disable"
        )
        env["CONFHUB_TEST_POSTGRES_DSN"] = env["CONFHUB_DSN"]
        env["CONFHUB_ADMIN_PASSWORD"] = password
        env["CONFHUB_SDK_TEST_PASSWORD"] = password
        env["CONFHUB_JWT_SECRET"] = secrets.token_hex(32)
        with tempfile.TemporaryDirectory(prefix="confhub-sdk-") as directory:
            binary = Path(directory) / "confhub"
            subprocess.run(
                ["go", "build", "-o", str(binary), "./cmd/main"], cwd=ROOT, env=env, check=True
            )
            with contextlib.ExitStack() as stack:
                for number in (1, 2):
                    app_port = free_port()
                    address = f"http://127.0.0.1:{app_port}"
                    log = stack.enter_context(open(Path(directory) / f"app{number}.log", "w"))
                    process = subprocess.Popen(
                        [str(binary), "--listen", f"127.0.0.1:{app_port}"],
                        cwd=ROOT,
                        env=env,
                        stdout=log,
                        stderr=log,
                    )
                    processes.append(process)
                    for _ in range(100):
                        if process.poll() is not None:
                            raise RuntimeError("temporary application exited; logs withheld")
                        try:
                            with urllib.request.urlopen(
                                address + "/health/ready", timeout=0.2
                            ) as response:
                                if response.status == 200:
                                    break
                        except (urllib.error.URLError, TimeoutError):
                            pass
                        time.sleep(0.1)
                    else:
                        raise RuntimeError("temporary application not ready")
                    env["CONFHUB_SDK_TEST_URL" + ("2" if number == 2 else "")] = address
                print("Two isolated ConfHub instances are ready", flush=True)
                env["CGO_ENABLED"] = "1"
                subprocess.run(
                    ["go", "test", "-race", "-count=1", "./..."],
                    cwd=ROOT / "sdk/go",
                    env=env,
                    check=True,
                )
                subprocess.run(
                    [options.python, "-m", "unittest", "discover", "-s", "tests", "-v"],
                    cwd=ROOT / "sdk/python",
                    env=env,
                    check=True,
                )
                if options.full:
                    # PostgreSQL bind mounts under data/ may be unreadable.
                    packages = ["./cmd/...", "./internal/..."]
                    subprocess.run(
                        ["go", "test", "-race", "-count=1", *packages],
                        cwd=ROOT,
                        env=env,
                        check=True,
                    )
                    subprocess.run(["go", "vet", *packages], cwd=ROOT, env=env, check=True)
    finally:
        for process in processes:
            if process.poll() is None:
                process.terminate()
        for process in processes:
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        subprocess.run(
            ["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
        )


if __name__ == "__main__":
    main()
