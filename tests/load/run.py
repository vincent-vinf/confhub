#!/usr/bin/env python3
"""Sweep load in an owned, resource-limited Compose cluster; preserve every result."""

import argparse
import contextlib
import datetime
import hashlib
import http.client
import json
import os
import platform
import secrets
import signal
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICES = ("app", "app2", "app3", "db")


def levels(raw):
    values = [int(value) for value in raw.split(",")]
    if (
        not values
        or any(value < 1 for value in values)
        or values != sorted(set(values))
    ):
        raise argparse.ArgumentTypeError(
            "levels must be positive, increasing, comma-separated integers"
        )
    return values


def ready(address):
    try:
        with urllib.request.urlopen(address + "/health/ready", timeout=1) as res:
            return res.status == 200
    except (urllib.error.URLError, TimeoutError, OSError):
        return False


class DockerConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=3)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def docker_get(socket_path, path):
    connection = DockerConnection(socket_path)
    try:
        connection.request("GET", path)
        response = connection.getresponse()
        if response.status != 200:
            raise RuntimeError(f"Docker metrics HTTP {response.status}")
        return json.load(response)
    finally:
        connection.close()


def process_stats(pid):
    """Host Linux proc counters for the exact load process (not the Python runner)."""
    fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
    return {
        "cpu_seconds": (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK"),
        "rss_bytes": int(fields[21]) * os.sysconf("SC_PAGE_SIZE"),
    }


class Environment:
    def __init__(self, report, args):
        self.report, self.args = report, args
        self.project = "confhub-load-" + secrets.token_hex(5)
        self.image = "confhub-load:" + self.project
        self.ids = {}
        self.addresses = []
        self.env = {
            key: value
            for key, value in os.environ.items()
            if not key.startswith("CONFHUB_")
        }
        self.settings = {
            "CONFHUB_APP_CPUS": args.app_cpus,
            "CONFHUB_APP_MEMORY": args.app_memory,
            "CONFHUB_APP_GO_MEMORY": args.go_memory,
            "CONFHUB_DB_CPUS": args.db_cpus,
            "CONFHUB_DB_MEMORY": args.db_memory,
            "CONFHUB_POLL_INTERVAL": args.poll_interval,
            "CONFHUB_LOAD_CONTEXT": str(ROOT),
            "CONFHUB_LOAD_IMAGE": self.image,
            "CONFHUB_DB_PASSWORD": secrets.token_hex(24),
            "CONFHUB_ADMIN_PASSWORD": secrets.token_hex(24),
            "CONFHUB_JWT_SECRET": secrets.token_hex(32),
        }
        self.secret_file = report / ".env"
        self.command = [
            "docker",
            "compose",
            "--env-file",
            str(self.secret_file),
            "-p",
            self.project,
            "-f",
            str(ROOT / "tests/load/compose.yaml"),
        ]
        self.started = False
        self.generator = None
        self.metrics_errors = []
        endpoint = subprocess.check_output(
            ["docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}"],
            text=True,
            env=self.env,
        ).strip()
        endpoint = os.environ.get("DOCKER_HOST", endpoint)
        if not endpoint.startswith("unix://") or platform.system() != "Linux":
            raise RuntimeError(
                "local Linux Docker with a Unix socket is required for RSS/CPU metrics"
            )
        self.socket = endpoint.removeprefix("unix://")
        self.secret_file.write_text(
            "".join(f"{key}={value}\n" for key, value in self.settings.items())
        )
        self.secret_file.chmod(0o600)

    def compose(self, *arguments, log="environment.log"):
        with (self.report / log).open("a") as output:
            subprocess.run(
                self.command + list(arguments),
                cwd=ROOT,
                env=self.env,
                stdout=output,
                stderr=subprocess.STDOUT,
                check=True,
            )

    def output(self, *arguments):
        return subprocess.check_output(
            self.command + list(arguments), cwd=ROOT, env=self.env, text=True
        ).strip()

    def start(self):
        self.compose("build", "app", log="build.log")
        self.started = True
        self.compose("up", "-d", "--wait", "db", "app")
        port = self.output("port", "app", "8080").rsplit(":", 1)[1]
        first = "http://127.0.0.1:" + port
        deadline = time.monotonic() + 90
        while not ready(first):
            if time.monotonic() > deadline:
                raise RuntimeError("first instance initialization timed out")
            time.sleep(0.2)
        self.compose("up", "-d", "app2", "app3")
        for name in SERVICES:
            self.ids[name] = self.output("ps", "-a", "-q", name)
            if not self.ids[name]:
                raise RuntimeError("Compose did not create " + name)
        for name in SERVICES[:3]:
            port = self.output("port", name, "8080").rsplit(":", 1)[1]
            self.addresses.append("http://127.0.0.1:" + port)
        while not all(ready(address) for address in self.addresses):
            if time.monotonic() > deadline:
                raise RuntimeError("cluster readiness timed out")
            time.sleep(0.2)
        metadata = {
            "project": self.project,
            "image": self.image,
            "revision": subprocess.check_output(
                ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True
            ).strip(),
            "dirty": bool(
                subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT)
            ),
            "host": {
                "cpu_count": os.cpu_count(),
                "kernel": platform.release(),
                "machine": platform.machine(),
                "cpu_model": next(
                    (
                        line.split(":", 1)[1].strip()
                        for line in Path("/proc/cpuinfo").read_text().splitlines()
                        if line.startswith("model name")
                    ),
                    platform.processor(),
                ),
            },
            "harness_sha256": {
                name: hashlib.sha256(
                    (ROOT / "tests/load" / name).read_bytes()
                ).hexdigest()
                for name in ("main.go", "run.py", "compose.yaml", "go.mod", "go.sum")
            },
            "generator_procs": self.args.generator_procs,
            "addresses": self.addresses,
            "poll_interval": self.args.poll_interval,
            "go_memory": self.args.go_memory,
            "parameters": vars(self.args),
            "containers": {},
        }
        for name, identifier in self.ids.items():
            data = docker_get(self.socket, f"/containers/{identifier}/json")
            config = data["HostConfig"]
            if config["NanoCpus"] <= 0 or config["Memory"] <= 0:
                raise RuntimeError("resource limits did not take effect on " + name)
            metadata["containers"][name] = {
                "id": identifier,
                "image_id": data["Image"],
                "cpu_limit": config["NanoCpus"] / 1e9,
                "memory_limit_bytes": config["Memory"],
                "swap_limit_bytes": config["MemorySwap"],
            }
        (self.report / "environment.json").write_text(json.dumps(metadata, indent=2))
        return metadata

    def sample(self, pid=None):
        snapshot = {"time": time.time(), "containers": {}}
        for name, identifier in self.ids.items():
            stats = docker_get(
                self.socket,
                f"/containers/{identifier}/stats?stream=false&one-shot=true",
            )
            inspect = docker_get(self.socket, f"/containers/{identifier}/json")
            memory = stats.get("memory_stats", {})
            cache = memory.get("stats", {}).get("inactive_file", 0)
            rss = 0
            host_pid = inspect["State"]["Pid"]
            if name != "db" and host_pid:
                with contextlib.suppress(OSError, ValueError):
                    rss = process_stats(host_pid)["rss_bytes"]
            snapshot["containers"][name] = {
                "cpu_ns": stats.get("cpu_stats", {})
                .get("cpu_usage", {})
                .get("total_usage", 0),
                "throttle_ns": stats.get("cpu_stats", {})
                .get("throttling_data", {})
                .get("throttled_time", 0),
                "working_set_bytes": max(0, memory.get("usage", 0) - cache),
                "rss_bytes": rss,
                "running": inspect["State"]["Running"],
                "oom": inspect["State"]["OOMKilled"],
                "restarts": inspect["RestartCount"],
            }
        if pid:
            with contextlib.suppress(OSError, ValueError):
                snapshot["generator"] = process_stats(pid)
        return snapshot

    def close(self):
        errors = []
        if self.generator and self.generator.poll() is None:
            self.generator.send_signal(signal.SIGTERM)
            try:
                self.generator.wait(timeout=20)
            except subprocess.TimeoutExpired:
                self.generator.kill()
                self.generator.wait(timeout=5)
        if self.started:
            for arguments in [
                ("logs", "--no-color"),
                ("down", "--volumes", "--remove-orphans"),
            ]:
                try:
                    self.compose(
                        *arguments,
                        log="containers.log"
                        if arguments[0] == "logs"
                        else "environment.log",
                    )
                except (OSError, subprocess.SubprocessError):
                    errors.append("compose " + arguments[0])
        # The unique image is owned by this run; production tags are never overwritten.
        try:
            removal = subprocess.run(
                ["docker", "image", "rm", self.image],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                env=self.env,
                check=False,
            )
            if (
                removal.returncode
                and subprocess.run(
                    ["docker", "image", "inspect", self.image],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    env=self.env,
                    check=False,
                ).returncode
                == 0
            ):
                errors.append("image removal")
        except (OSError, subprocess.SubprocessError):
            errors.append("image removal")
        self.secret_file.unlink(missing_ok=True)
        return errors


def resource_summary(samples):
    out = {"containers": {}, "generator": {}}
    for name in SERVICES:
        rows = [sample for sample in samples if name in sample["containers"]]
        peak_cpu = peak_throttle = 0.0
        for previous, current in zip(rows, rows[1:]):
            seconds = current["time"] - previous["time"]
            if seconds <= 0:
                continue
            a, b = previous["containers"][name], current["containers"][name]
            peak_cpu = max(peak_cpu, (b["cpu_ns"] - a["cpu_ns"]) / 1e9 / seconds * 100)
            peak_throttle = max(
                peak_throttle, (b["throttle_ns"] - a["throttle_ns"]) / 1e9 / seconds
            )
        if rows:
            out["containers"][name] = {
                "peak_cpu_percent": round(peak_cpu, 2),
                "peak_throttle_seconds_per_second": round(peak_throttle, 3),
                "peak_rss_bytes": max(
                    row["containers"][name]["rss_bytes"] for row in rows
                ),
                "peak_working_set_bytes": max(
                    row["containers"][name]["working_set_bytes"] for row in rows
                ),
                "oom_or_restart": any(
                    row["containers"][name]["oom"]
                    or not row["containers"][name]["running"]
                    or row["containers"][name]["restarts"]
                    > rows[0]["containers"][name]["restarts"]
                    for row in rows
                ),
            }
    generators = [sample for sample in samples if "generator" in sample]
    cpus = []
    for previous, current in zip(generators, generators[1:]):
        seconds = current["time"] - previous["time"]
        if seconds > 0:
            cpus.append(
                (
                    current["generator"]["cpu_seconds"]
                    - previous["generator"]["cpu_seconds"]
                )
                / seconds
                * 100
            )
    if generators:
        out["generator"] = {
            "peak_cpu_percent": round(max(cpus, default=0), 2),
            "peak_rss_bytes": max(row["generator"]["rss_bytes"] for row in generators),
        }
    return out


def plans(args):
    for scenario in args.scenarios.split(","):
        points = (
            args.read_rates
            if scenario == "get"
            else args.write_rates
            if scenario == "write"
            else args.connections
        )
        for point in points:
            yield {
                "scenario": scenario,
                "clients": 0 if scenario in {"get", "write"} else point,
                "configs": 20
                if scenario == "write"
                else 10
                if scenario == "mixed"
                else 1,
                "size": 10240 if scenario == "mixed" else args.size,
                "get_rate": point
                if scenario == "get"
                else args.mixed_get_rate
                if scenario == "mixed"
                else 0,
                "publish_rate": point
                if scenario == "write"
                else 10
                if scenario == "mixed"
                else 1
                if scenario == "fanout"
                else 0,
            }


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenarios", default="get,idle,fanout,mixed,write")
    parser.add_argument(
        "--read-rates", type=levels, default=levels("1000,5000,10000,20000")
    )
    parser.add_argument(
        "--write-rates", type=levels, default=levels("10,50,100,200,500")
    )
    parser.add_argument("--connections", type=levels, default=levels("1000,5000,10000"))
    parser.add_argument("--duration", type=int, default=30)
    parser.add_argument("--warmup", type=int, default=5)
    parser.add_argument("--workers", type=int, default=256)
    parser.add_argument("--connect-workers", type=int, default=64)
    parser.add_argument("--size", type=int, default=1024)
    parser.add_argument("--mixed-get-rate", type=int, default=500)
    parser.add_argument("--app-cpus", default="1.0")
    parser.add_argument("--app-memory", default="1g")
    parser.add_argument("--go-memory", default="768MiB")
    parser.add_argument("--db-cpus", default="1.0")
    parser.add_argument("--db-memory", default="2g")
    parser.add_argument("--poll-interval", default="100ms")
    parser.add_argument("--generator-procs", type=int, default=2)
    parser.add_argument("--report-dir")
    parser.add_argument("--require-all-pass", action="store_true")
    parser.add_argument(
        "--race-generator",
        action="store_true",
        help="smoke validation only; not capacity measurement",
    )
    args = parser.parse_args()
    if any(
        name not in {"get", "idle", "fanout", "mixed", "write"}
        for name in args.scenarios.split(",")
    ):
        parser.error("unknown scenario")
    if (
        min(args.duration, args.workers, args.connect_workers, args.generator_procs) < 1
        or args.warmup < 0
        or not 32 <= args.size <= 1 << 20
        or args.mixed_get_rate < 0
    ):
        parser.error("invalid workload parameters")
    stamp = datetime.datetime.now(
        datetime.timezone(datetime.timedelta(hours=8))
    ).strftime("%Y%m%d-%H%M%S")
    report = (
        Path(args.report_dir).resolve()
        if args.report_dir
        else ROOT / "test-results" / ("load-" + stamp)
    )
    if report.exists() and any(report.iterdir()):
        parser.error("report directory must be empty; preserve earlier evidence")
    report.mkdir(parents=True, exist_ok=True)
    env = Environment(report, args)
    interrupted = False

    def interrupt(_signum, _frame):
        nonlocal interrupted
        interrupted = True
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupt)
    signal.signal(signal.SIGINT, interrupt)
    results = []
    failed = set()
    summary = {"results": results, "boundary": {}, "cleanup": False}
    try:
        metadata = env.start()
        summary["environment"] = metadata
        binary = report / "loadgen"
        subprocess.run(
            ["go", "build"]
            + (["-race"] if args.race_generator else [])
            + ["-trimpath", "-o", str(binary), "."],
            cwd=ROOT / "tests/load",
            env=env.env,
            check=True,
        )
        baseline = [env.sample()]
        time.sleep(2)
        baseline.append(env.sample())
        summary["empty_cluster"] = resource_summary(baseline)
        for index, plan in enumerate(plans(args)):
            scenario = plan["scenario"]
            if scenario in failed:
                continue
            label = f"{index:02d}-{scenario}-{plan['clients'] or plan['get_rate'] or plan['publish_rate']}"
            directory = report / label
            directory.mkdir()
            print("RUN", label, flush=True)
            command = [
                str(binary),
                "--scenario",
                scenario,
                "--clients",
                str(plan["clients"]),
                "--configs",
                str(plan["configs"]),
                "--size",
                str(plan["size"]),
                "--get-rate",
                str(plan["get_rate"]),
                "--publish-rate",
                str(plan["publish_rate"]),
                "--duration",
                str(args.duration) + "s",
                "--warmup",
                str(args.warmup) + "s",
                "--workers",
                str(args.workers),
                "--connect-workers",
                str(args.connect_workers),
                "--output",
                str(directory / "load.json"),
            ]
            generator_env = dict(env.env)
            generator_env.update(
                {
                    "CONFHUB_LOAD_ADDRESSES": ",".join(env.addresses),
                    "CONFHUB_LOAD_PASSWORD": env.settings["CONFHUB_ADMIN_PASSWORD"],
                    "GOMAXPROCS": str(args.generator_procs),
                    "GOMEMLIMIT": "2GiB",
                }
            )
            samples = [env.sample()]
            errors_before = len(env.metrics_errors)
            start = time.monotonic()
            deadline = (
                start + args.warmup + args.duration + max(180, plan["clients"] / 10)
            )
            with (
                (directory / "generator.log").open("w") as log,
                (directory / "resources.jsonl").open("w") as stream,
            ):
                env.generator = subprocess.Popen(
                    command, env=generator_env, stdout=log, stderr=subprocess.STDOUT
                )
                while env.generator.poll() is None:
                    try:
                        sample = env.sample(env.generator.pid)
                        phase_file = directory / "load.json.phase"
                        sample["phase"] = (
                            phase_file.read_text() if phase_file.exists() else "setup"
                        )
                        samples.append(sample)
                        stream.write(json.dumps(sample) + "\n")
                        stream.flush()
                        if any(
                            not data["running"]
                            for data in sample["containers"].values()
                        ):
                            env.generator.send_signal(signal.SIGTERM)
                    except (
                        OSError,
                        ValueError,
                        RuntimeError,
                        http.client.HTTPException,
                    ) as error:
                        env.metrics_errors.append(type(error).__name__)
                    if time.monotonic() > deadline:
                        env.generator.send_signal(signal.SIGTERM)
                        try:
                            env.generator.wait(timeout=20)
                        except subprocess.TimeoutExpired:
                            env.generator.kill()
                            env.generator.wait(timeout=5)
                        break
                    time.sleep(1)
            samples.append(env.sample())
            resources = resource_summary(samples)
            measured_samples = [
                sample for sample in samples if sample.get("phase") == "measure"
            ]
            resources["measurement"] = resource_summary(measured_samples)
            raw = directory / "load.json"
            measured = (
                json.loads(raw.read_text())
                if raw.exists()
                else {
                    "pass": False,
                    "reasons": ["load process did not produce a report"],
                }
            )
            valid_metrics = (
                len(measured_samples) >= 2 and len(env.metrics_errors) == errors_before
            )
            valid_metrics = (
                valid_metrics
                and measured.get("notifications", {}).get("recorder_dropped", 0) == 0
            )
            stable = (
                measured["pass"]
                and valid_metrics
                and not any(
                    data["oom_or_restart"] for data in resources["containers"].values()
                )
            )
            generator_limited = (
                resources["measurement"]["generator"].get("peak_cpu_percent", 0)
                >= args.generator_procs * 90
            )
            result = {
                "label": label,
                "plan": plan,
                "load": measured,
                "resources": resources,
                "stable": stable,
                "valid_metrics": valid_metrics,
                "generator_near_cpu_limit": generator_limited,
                "exit_code": env.generator.returncode,
                "wall_seconds": round(time.monotonic() - start, 2),
            }
            results.append(result)
            if stable:
                summary["boundary"].setdefault(scenario, {})[
                    "highest_observed_pass"
                ] = plan
            else:
                summary["boundary"].setdefault(scenario, {})[
                    "first_observed_failure"
                ] = plan
                failed.add(scenario)
            (report / "summary.json").write_text(json.dumps(summary, indent=2))
            print(
                "PASS" if stable else "BOUNDARY",
                label,
                measured.get("reasons", []),
                flush=True,
            )
            if any(data["oom_or_restart"] for data in resources["containers"].values()):
                print("Stopping: owned cluster became unavailable.", flush=True)
                break
    finally:
        cleanup_errors = env.close()
        summary["cleanup"] = not cleanup_errors
        summary["cleanup_errors"] = cleanup_errors
        summary["interrupted"] = interrupted
        summary["metrics_errors"] = env.metrics_errors
        (report / "summary.json").write_text(json.dumps(summary, indent=2))
        print("Reports:", report, flush=True)
    harness_failed = any(
        not result["valid_metrics"]
        or result["load"].get("cleanup_errors")
        or result["exit_code"] == 2
        for result in results
    )
    if (
        cleanup_errors
        or env.metrics_errors
        or harness_failed
        or (args.require_all_pass and failed)
    ):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
