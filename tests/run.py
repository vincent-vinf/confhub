#!/usr/bin/env python3
"""Run layered tests in an owned PostgreSQL/three-replica environment."""

import argparse
import contextlib
import http.server
import json
import os
import secrets
import select
import socket
import socketserver
import subprocess
import threading
import time
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
IMAGE = "registry.cn-hangzhou.aliyuncs.com/bodesi/postgres:17"


def free_port():
    with socket.socket() as connection:
        connection.bind(("127.0.0.1", 0))
        return connection.getsockname()[1]


def wait_for(check, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.05)
    raise RuntimeError("environment readiness timed out")


def ready(address):
    try:
        with urllib.request.urlopen(address + "/health/ready", timeout=0.2) as response:
            return response.status == 200
    except (urllib.error.URLError, TimeoutError):
        return False


class Proxy(socketserver.ThreadingTCPServer):
    """Owned TCP database boundary, including existing pooled connections."""

    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, port):
        self.target = ("127.0.0.1", port)
        self.enabled = True
        self.connections = set()
        self.lock = threading.Lock()
        super().__init__(("127.0.0.1", 0), ProxyConnection)
        self.thread = threading.Thread(target=self.serve_forever, daemon=True)
        self.thread.start()

    def enable(self, value):
        with self.lock:
            self.enabled = value
            if not value:
                for connection in tuple(self.connections):
                    with contextlib.suppress(OSError):
                        connection.shutdown(socket.SHUT_RDWR)

    def close(self):
        self.enable(False)
        self.shutdown()
        self.server_close()
        self.thread.join(timeout=3)


class ProxyConnection(socketserver.BaseRequestHandler):
    def handle(self):
        proxy = self.server
        upstream = None
        try:
            with proxy.lock:
                if not proxy.enabled:
                    return
                upstream = socket.create_connection(proxy.target, timeout=2)
                upstream.settimeout(None)
                proxy.connections.update((self.request, upstream))
            peers = {self.request: upstream, upstream: self.request}
            while True:
                readable, _, _ = select.select(list(peers), [], [], 1)
                for source in readable:
                    data = source.recv(65536)
                    if not data:
                        return
                    peers[source].sendall(data)
        except OSError:
            pass
        finally:
            with proxy.lock:
                proxy.connections.discard(self.request)
                if upstream:
                    proxy.connections.discard(upstream)
            if upstream:
                upstream.close()


class Environment:
    def __init__(self, report, env):
        self.report = report
        self.env = env
        self.name = "confhub-system-" + secrets.token_hex(6)
        self.binary = report / "confhub"
        self.processes = []
        self.logs = []
        self.proxies = []
        self.ports = []
        self.database_created = False
        self.controller = None
        self.lock = threading.Lock()
        self.paused = False

    def start(self, index):
        process = self.processes[index]
        if process is not None and process.poll() is None:
            return
        app_env = dict(self.env)
        proxy_port = self.proxies[index].server_address[1]
        app_env["CONFHUB_DSN"] = self.dsn(proxy_port)
        app_env["CONFHUB_LISTEN"] = f"127.0.0.1:{self.ports[index]}"
        self.processes[index] = subprocess.Popen(
            [str(self.binary)],
            cwd=ROOT,
            env=app_env,
            stdout=self.logs[index],
            stderr=self.logs[index],
        )
        wait_for(lambda: self.processes[index].poll() is not None or ready(self.addresses[index]))
        if self.processes[index].poll() is not None:
            raise RuntimeError(f"application {index} exited; inspect owned app log")

    def stop(self, index):
        process = self.processes[index]
        if process is not None and process.poll() is None:
            # An abnormal exit keeps the lease, unlike graceful termination.
            process.kill()
            process.wait(timeout=5)

    @property
    def addresses(self):
        return [f"http://127.0.0.1:{port}" for port in self.ports]

    def dsn(self, port):
        return f"postgres://confhub:{self.env['CONFHUB_ADMIN_PASSWORD']}@127.0.0.1:{port}/confhub?sslmode=disable"

    def provision(self):
        subprocess.run(
            [
                "docker",
                "run",
                "-d",
                "--name",
                self.name,
                "-p",
                "127.0.0.1::5432",
                "-e",
                "POSTGRES_DB=confhub",
                "-e",
                "POSTGRES_USER=confhub",
                "-e",
                "POSTGRES_PASSWORD=" + self.env["CONFHUB_ADMIN_PASSWORD"],
                IMAGE,
            ],
            check=True,
            stdout=subprocess.DEVNULL,
        )
        self.database_created = True
        self.port = int(
            subprocess.check_output(["docker", "port", self.name, "5432/tcp"], text=True)
            .strip()
            .rsplit(":", 1)[1]
        )
        wait_for(
            lambda: (
                subprocess.run(
                    ["docker", "exec", self.name, "pg_isready", "-U", "confhub", "-d", "confhub"],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                ).returncode
                == 0
            )
        )
        self.env["CONFHUB_TEST_POSTGRES_DSN"] = self.dsn(self.port)
        self.env["CONFHUB_DSN"] = self.dsn(self.port)
        subprocess.run(
            ["go", "build", "-race", "-cover", "-o", str(self.binary), "./cmd/main"],
            cwd=ROOT,
            env=self.env,
            check=True,
        )
        # One explicit initializer, then concurrent real startup on the same schema.
        subprocess.run(
            [str(self.binary), "migrate"],
            cwd=ROOT,
            env=self.env,
            check=True,
            stdout=subprocess.DEVNULL,
        )
        for index in range(3):
            self.proxies.append(Proxy(self.port))
            self.ports.append(free_port())
            self.processes.append(None)
            self.logs.append(open(self.report / f"app-{index}.log", "a", buffering=1))
        errors = []

        def start(index):
            try:
                self.start(index)
            except Exception as error:
                errors.append(error)

        threads = [threading.Thread(target=start, args=(i,)) for i in range(3)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        if errors:
            raise errors[0]
        token = secrets.token_hex(32)
        environment = self

        class Controller(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                from urllib.parse import parse_qs, urlsplit

                if self.headers.get("Authorization") != "Bearer " + token:
                    self.send_error(403)
                    return
                try:
                    parsed = urlsplit(self.path)
                    index = int(parse_qs(parsed.query).get("index", ["0"])[0])
                    if index not in range(3):
                        raise ValueError("invalid instance index")
                    with environment.lock:
                        action = parsed.path.strip("/")
                        if action == "stop":
                            environment.stop(index)
                        elif action == "start":
                            environment.start(index)
                        elif action == "isolate":
                            environment.proxies[index].enable(False)
                        elif action == "heal":
                            environment.proxies[index].enable(True)
                        elif action == "database-down":
                            for proxy in environment.proxies:
                                proxy.enable(False)
                            if not environment.paused:
                                subprocess.run(
                                    ["docker", "pause", environment.name],
                                    check=True,
                                    stdout=subprocess.DEVNULL,
                                )
                                environment.paused = True
                        elif action == "database-up":
                            if environment.paused:
                                subprocess.run(
                                    ["docker", "unpause", environment.name],
                                    check=True,
                                    stdout=subprocess.DEVNULL,
                                )
                                environment.paused = False
                            for proxy in environment.proxies:
                                proxy.enable(True)
                        elif action == "wait-retention":
                            time.sleep(2.1)  # let the owned database's retention clock expire

                            def purged():
                                value = subprocess.check_output(
                                    [
                                        "docker",
                                        "exec",
                                        environment.name,
                                        "psql",
                                        "-U",
                                        "confhub",
                                        "-d",
                                        "confhub",
                                        "-Atc",
                                        "SELECT COUNT(*) FROM change_events",
                                    ],
                                    text=True,
                                ).strip()
                                return value == "0"

                            wait_for(purged, seconds=12)
                        else:
                            raise ValueError("unknown fault action")
                    self.send_response(200)
                    self.end_headers()
                except Exception:
                    self.send_error(500, "isolated fault operation failed")

        self.controller = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Controller)
        threading.Thread(target=self.controller.serve_forever, daemon=True).start()
        self.env["CONFHUB_SYSTEM_CONTROL"] = f"http://127.0.0.1:{self.controller.server_port}"
        self.env["CONFHUB_SYSTEM_CONTROL_TOKEN"] = token
        self.env["CONFHUB_SYSTEM_PASSWORD"] = self.env["CONFHUB_ADMIN_PASSWORD"]
        self.env["CONFHUB_SDK_TEST_PASSWORD"] = self.env["CONFHUB_ADMIN_PASSWORD"]
        self.env["CONFHUB_SDK_TEST_URL"] = self.addresses[0]
        self.env["CONFHUB_SDK_TEST_URL2"] = self.addresses[1]

    def close(self):
        if self.controller:
            self.controller.shutdown()
            self.controller.server_close()
        if self.paused:
            subprocess.run(
                ["docker", "unpause", self.name],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                check=True,
            )
        for process in self.processes:
            if process is not None and process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        for proxy in self.proxies:
            proxy.close()
        for log in self.logs:
            log.close()
        if self.database_created:
            subprocess.run(["docker", "rm", "-f", self.name], check=True, stdout=subprocess.DEVNULL)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--layers", default="unit,integration,system,frontend")
    parser.add_argument(
        "--scenarios", default="business,protocol,concurrency,random,presence,faults"
    )
    parser.add_argument("--writers", type=int, default=32)
    parser.add_argument("--clients", type=int, default=100)
    parser.add_argument("--configs", type=int, default=10)
    parser.add_argument("--rounds", type=int, default=10)
    parser.add_argument("--seed", type=int, default=20261010)
    parser.add_argument(
        "--coverage-gates",
        action="store_true",
        help="enforce all backend/SDK coverage goals; requires integration and system",
    )
    parser.add_argument("--python", default=str(ROOT / "sdk/python/.venv/bin/python"))
    parser.add_argument(
        "--report-dir",
        default=str(ROOT / "test-results" / ("run-" + time.strftime("%Y%m%d-%H%M%S"))),
    )
    args = parser.parse_args()
    layers = args.layers.split(",")
    if not layers or any(
        layer not in {"unit", "integration", "system", "frontend"} for layer in layers
    ):
        parser.error("unknown layer")
    if (
        min(args.writers, args.clients, args.configs, args.rounds) < 1
        or args.writers < 2
        or args.configs > 10
    ):
        parser.error("invalid test sizes")
    if (
        any(layer in {"unit", "integration", "system"} for layer in layers)
        and not Path(args.python).is_file()
    ):
        parser.error("install sdk/python[dev] in its venv or pass --python")
    if args.coverage_gates and not {"integration", "system"}.issubset(layers):
        parser.error("coverage gates require integration and system layers")
    report = Path(args.report_dir).resolve()
    report.mkdir(parents=True, exist_ok=True)
    env = {key: value for key, value in os.environ.items() if not key.startswith("CONFHUB_")}
    env.update(
        {
            "CGO_ENABLED": "1",
            "CONFHUB_ADMIN_PASSWORD": secrets.token_hex(16),
            "CONFHUB_JWT_SECRET": secrets.token_hex(32),
            "CONFHUB_CLEANUP_INTERVAL": "200ms",
            "CONFHUB_EVENT_RETENTION": "2s",
            "CONFHUB_STATIC_DIR": str(ROOT / "frontend/dist"),
            "PYTHONASYNCIODEBUG": "1",
        }
    )
    binary_coverage = report / "binary-coverage"
    binary_coverage.mkdir(exist_ok=True)
    env["GOCOVERDIR"] = str(binary_coverage)
    environment = Environment(report, env)
    results = []

    def execute(name, command, cwd=ROOT, command_env=None):
        start = time.monotonic()
        print("RUN", name, flush=True)
        with open(report / (name + ".log"), "w") as log:
            process = subprocess.run(
                command,
                cwd=cwd,
                env=env if command_env is None else command_env,
                stdout=log,
                stderr=subprocess.STDOUT,
            )
        result = {
            "name": name,
            "seconds": time.monotonic() - start,
            "exit_code": process.returncode,
        }
        results.append(result)
        (report / "layers.json").write_text(json.dumps(results, indent=2))
        suite = ET.Element(
            "testsuite",
            name="ConfHub layers",
            tests=str(len(results)),
            failures=str(sum(item["exit_code"] != 0 for item in results)),
        )
        for item in results:
            case = ET.SubElement(suite, "testcase", name=item["name"], time=str(item["seconds"]))
            if item["exit_code"]:
                ET.SubElement(case, "failure", message="inspect corresponding owned layer log")
        ET.ElementTree(suite).write(report / "layers.xml", encoding="utf-8", xml_declaration=True)
        print("PASS" if process.returncode == 0 else "FAIL", name, flush=True)
        if process.returncode:
            raise RuntimeError(f"{name} failed; inspect {report / (name + '.log')}")

    try:
        if "unit" in layers:
            execute(
                "unit",
                [
                    "go",
                    "test",
                    "-race",
                    "-count=1",
                    "-coverprofile=" + str(report / "unit.out"),
                    "./internal/config",
                    "./internal/settings",
                ],
            )
            execute("sdk-unit", ["go", "test", "-race", "-count=1", "./..."], ROOT / "sdk/go")
            execute(
                "python-unit",
                [
                    args.python,
                    "-m",
                    "unittest",
                    "discover",
                    "-s",
                    "tests",
                    "-p",
                    "test_client.py",
                    "-v",
                ],
                ROOT / "sdk/python",
            )
        if "integration" in layers or "system" in layers:
            environment.provision()
            if "integration" in layers:
                execute(
                    "backend",
                    [
                        "go",
                        "test",
                        "-race",
                        "-count=1",
                        "-coverprofile=" + str(report / "backend.out"),
                        "./cmd/...",
                        "./internal/config",
                        "./internal/settings",
                        "./internal/storage",
                        "./internal/server",
                        "./internal/syncer",
                    ],
                )
                execute("vet", ["go", "vet", "./cmd/...", "./internal/..."])
                execute(
                    "sdk",
                    [
                        "go",
                        "test",
                        "-race",
                        "-count=1",
                        "-coverprofile=" + str(report / "sdk.out"),
                        "./...",
                    ],
                    ROOT / "sdk/go",
                )
                env["COVERAGE_FILE"] = str(report / ".coverage")
                execute(
                    "python",
                    [
                        args.python,
                        "-m",
                        "coverage",
                        "run",
                        "--source=confhub",
                        "-m",
                        "unittest",
                        "discover",
                        "-s",
                        "tests",
                        "-v",
                    ],
                    ROOT / "sdk/python",
                )
                execute(
                    "python-coverage",
                    [
                        args.python,
                        "-m",
                        "coverage",
                        "json",
                        "-o",
                        str(report / "python-coverage.json"),
                    ],
                    ROOT / "sdk/python",
                )
                execute("sdk-check", ["make", "sdk-check", "SDK_PYTHON=" + args.python])
            if "system" in layers:
                execute(
                    "system",
                    [
                        "go",
                        "run",
                        "-race",
                        ".",
                        "--addresses",
                        ",".join(environment.addresses),
                        "--scenarios",
                        args.scenarios,
                        "--python",
                        args.python,
                        "--python-worker",
                        str(ROOT / "tests/python_worker.py"),
                        "--report-dir",
                        str(report / "system"),
                        "--timeout",
                        "35s",
                        "--writers",
                        str(args.writers),
                        "--clients",
                        str(args.clients),
                        "--configs",
                        str(args.configs),
                        "--rounds",
                        str(args.rounds),
                        "--seed",
                        str(args.seed),
                    ],
                    ROOT / "tests/system",
                )
            if "system" in layers:
                connected_env = {
                    key: value
                    for key, value in env.items()
                    if key not in {"CONFHUB_SYSTEM_CONTROL", "CONFHUB_SYSTEM_CONTROL_TOKEN"}
                }
                connected_env["CONFHUB_SYSTEM_ADDRESSES"] = ",".join(environment.addresses)
                execute(
                    "connected-system",
                    [
                        "go",
                        "run",
                        "-race",
                        ".",
                        "--scenarios",
                        "business,protocol",
                        "--python",
                        args.python,
                        "--python-worker",
                        str(ROOT / "tests/python_worker.py"),
                        "--report-dir",
                        str(report / "connected-system"),
                    ],
                    ROOT / "tests/system",
                    connected_env,
                )
            for process in environment.processes:
                if process is not None and process.poll() is None:
                    process.terminate()
                    process.wait(timeout=10)
            execute(
                "process-coverage",
                [
                    "go",
                    "tool",
                    "covdata",
                    "textfmt",
                    "-i=" + str(binary_coverage),
                    "-o=" + str(report / "process.out"),
                ],
            )
            if "integration" in layers:
                execute(
                    "coverage-summary",
                    ["python3", "tests/coverage.py", str(report)]
                    + (["--gate"] if args.coverage_gates else []),
                )
            race_found = any(
                "DATA RACE" in (report / f"app-{index}.log").read_text() for index in range(3)
            )
            if race_found:
                raise RuntimeError("application race detector found a data race; inspect app logs")
        if "frontend" in layers:
            execute("frontend-typecheck", ["npm", "--prefix", "frontend", "run", "typecheck"])
            execute(
                "frontend-unit",
                [
                    "npm",
                    "--prefix",
                    "frontend",
                    "run",
                    "test:coverage",
                    "--",
                    "--coverage.reportsDirectory=" + str(report / "frontend-coverage"),
                ],
            )
            execute("frontend-format", ["npm", "--prefix", "frontend", "run", "format:check"])
            execute("frontend-e2e", ["npm", "--prefix", "frontend", "run", "test:e2e"])
    finally:
        environment.close()
    print("Reports:", report, flush=True)


if __name__ == "__main__":
    main()
