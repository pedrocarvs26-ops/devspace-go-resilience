#!/usr/bin/env python3
"""Opt-in end-to-end test: real MCP tunnel + >500 MiB Git clone + CPU/download load.
Requires Python and Git on the devspace host. Creates a unique checkout inside the
selected workspace and LEAVES it for inspection. Never deletes an existing path.
Run the load generator outside the measured machine for meaningful network load.
"""
import argparse
import base64
import json
from pathlib import Path
import statistics
import time
import urllib.parse
import urllib.request
import uuid

# This program runs as a SINGLE managed shell job on the devspace machine.
WORKLOAD = r'''
import concurrent.futures, hashlib, json, os, pathlib, subprocess, sys, time, urllib.request
cfg = json.loads(CONFIG_JSON)
start = time.monotonic()
children = []
executor = concurrent.futures.ThreadPoolExecutor(max_workers=1)

def download():
    total = 0
    with urllib.request.urlopen(cfg["download_url"], timeout=30) as response:
        while True:
            block = response.read(65536)
            if not block:
                return total
            total += len(block)

try:
    cpu_code = "import hashlib; data=b'x'*1048576\nwhile True: hashlib.sha256(data).digest()"
    for _ in range(cfg["workers"]):
        children.append(subprocess.Popen([sys.executable, "-c", cpu_code], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
    future = executor.submit(download)
    print("ACCEPTANCE_LOAD_STARTED", flush=True)
    subprocess.run(["git", "clone", "--no-local", "--progress", "--", cfg["repo_url"], cfg["destination"]], check=True)
    size = sum(p.stat().st_size for p in pathlib.Path(cfg["destination"], ".git").rglob("*") if p.is_file())
    if size <= 500*1024*1024:
        raise RuntimeError("Git object database did not exceed 500 MiB: " + str(size))
    downloaded = future.result(timeout=cfg["deadline"])
    if downloaded <= 500*1024*1024:
        raise RuntimeError("Concurrent network transfer did not exceed 500 MiB")
    remaining = cfg["load_seconds"] - (time.monotonic()-start)
    if remaining > 0:
        time.sleep(remaining)
    print("ACCEPTANCE_LOAD_OK " + json.dumps({"git_bytes":size,"download_bytes":downloaded}), flush=True)
finally:
    for child in children:
        child.terminate()
    for child in children:
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill(); child.wait()
    executor.shutdown(wait=False, cancel_futures=True)
'''


class MCP:
    def __init__(self, url, timeout):
        self.url, self.timeout, self.counter = url, timeout, 0
        self.latencies = []
        self.protocol = "2025-03-26"

    def rpc(self, method, params):
        self.counter += 1
        payload = json.dumps({"jsonrpc": "2.0", "id": self.counter, "method": method, "params": params}).encode()
        request = urllib.request.Request(self.url, payload, {
            "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": self.protocol,
        })
        start = time.monotonic()
        with urllib.request.urlopen(request, timeout=self.timeout) as response:
            data = json.load(response)
        self.latencies.append(time.monotonic() - start)
        if "error" in data:
            raise RuntimeError(str(data["error"]))
        return data["result"]

    def initialize(self):
        data = self.rpc("initialize", {"protocolVersion": self.protocol, "capabilities": {}, "clientInfo": {"name": "devspace-acceptance", "version": "1"}})
        self.protocol = data.get("protocolVersion", self.protocol)
        request = urllib.request.Request(self.url, json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}).encode(), {
            "Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": self.protocol,
        })
        with urllib.request.urlopen(request, timeout=self.timeout) as response:
            response.read()

    def tool(self, name, arguments):
        data = self.rpc("tools/call", {"name": name, "arguments": arguments})
        if data.get("isError"):
            raise RuntimeError(str(data))
        if "structuredContent" in data:
            return data["structuredContent"]
        return json.loads(next(item["text"] for item in data["content"] if item["type"] == "text"))


def run(args):
    if not args.confirm_load:
        raise ValueError("Use --confirm-load only on an isolated test host: this consumes CPU, network and several GiB of disk")
    if not 1 <= args.workers <= 64 or args.load_seconds < 10 or args.deadline < args.load_seconds or args.max_latency <= 0:
        raise ValueError("Require workers 1..64, load-seconds >=10, deadline >=load-seconds, max-latency >0")
    for url in (args.mcp_url, args.repo_url, args.download_url):
        if urllib.parse.urlsplit(url).scheme not in ("http", "https"):
            raise ValueError("Use actual HTTP(S) endpoints, not file:// paths")
    client = MCP(args.mcp_url, args.max_latency)
    client.initialize()
    tools = client.rpc("tools/list", {})
    names = {item["name"] for item in tools["tools"]}
    shell = "bash" if "bash" in names else "run_shell"
    if not {shell, "bash_status", "bash_cancel"} <= names:
        raise ValueError("Required asynchronous tools were not advertised")
    opened = client.tool("open_default_workspace", {})
    ws = opened["workspaceId"]
    destination = "devspace-acceptance-" + uuid.uuid4().hex
    config = {"repo_url": args.repo_url, "download_url": args.download_url, "destination": destination,
              "workers": args.workers, "load_seconds": args.load_seconds, "deadline": args.deadline}
    code = WORKLOAD.replace("CONFIG_JSON", repr(json.dumps(config)))
    encoded = base64.b64encode(code.encode()).decode()
    # The payload contains no single quotes; this quoting works in bash and PowerShell.
    if args.python not in ("python", "python3", "python.exe", "python3.exe"):
        raise ValueError("--python must name python/python3 (optionally .exe)")
    command = f'''{args.python} -c 'import base64;exec(base64.b64decode("{encoded}").decode())' '''
    job = None
    finished = False
    report = {"accepted": False, "fixture_directory": destination, "checks": 0, "errors": []}
    log_path = Path(args.report).with_suffix(".output.log")
    started = time.monotonic()
    try:
        job = client.tool(shell, {"workspaceId": ws, "command": command, "timeout": args.deadline})
        cursor = 0
        success_marker = False
        health_url = args.mcp_url.rsplit("/", 1)[0] + "/healthz"
        with log_path.open("w", encoding="utf-8") as output:
            while time.monotonic() - started < args.deadline:
                tick = time.monotonic()
                with urllib.request.urlopen(health_url, timeout=args.max_latency) as response:
                    health = json.load(response)
                latency = time.monotonic() - tick
                if not health.get("ok") or health.get("name") != "devspace-go" or latency > args.max_latency:
                    raise RuntimeError("Health check failed or exceeded latency threshold")
                client.rpc("tools/list", {})
                state = client.tool("bash_status", {"workspaceId": ws, "job_id": job["job_id"], "offset": cursor})
                report["checks"] += 1
                text = state.get("result", "")
                output.write(text); output.flush()
                # Markers may be split across polls: inspect a bounded rolling tail.
                report["tail"] = (report.get("tail", "") + text)[-4096:]
                success_marker |= "ACCEPTANCE_LOAD_OK" in report["tail"]
                if state.get("dropped_bytes", 0):
                    raise RuntimeError("Polling lost output; increase retention or polling frequency")
                cursor = state["next_offset"]
                if state["state"] not in ("queued", "running") and not state["has_more"]:
                    finished = True
                    if state["state"] != "completed" or state.get("exit_code") != 0 or not success_marker:
                        raise RuntimeError(f"Workload failed: {state['state']}: {state.get('error')}; inspect {log_path}")
                    report["accepted"] = True
                    break
                time.sleep(0.25 if state["has_more"] else 0.5)
            if not finished:
                raise TimeoutError("Acceptance deadline expired")
        if max(client.latencies) > args.max_latency:
            raise RuntimeError("An MCP request exceeded the latency threshold")
    except Exception as error:
        report["accepted"] = False
        report["errors"].append(str(error))
    finally:
        if job and not finished:
            try:
                client.tool("bash_cancel", {"workspaceId": ws, "job_id": job["job_id"]})
            except Exception as error:
                report["errors"].append("Could not cancel job; it remains bounded by its deadline: " + str(error))
        report.pop("tail", None)
        report["elapsed_seconds"] = time.monotonic() - started
        report["max_mcp_latency_seconds"] = max(client.latencies, default=0)
        report["median_mcp_latency_seconds"] = statistics.median(client.latencies) if client.latencies else 0
        Path(args.report).write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
    if not report["accepted"]:
        raise SystemExit(1)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mcp-url", required=True, help="Actual public tunnel URL ending in /mcp")
    parser.add_argument("--repo-url", required=True)
    parser.add_argument("--download-url", required=True)
    parser.add_argument("--python", default="python3")
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--load-seconds", type=int, default=120)
    parser.add_argument("--deadline", type=int, default=3600)
    parser.add_argument("--max-latency", type=float, default=5)
    parser.add_argument("--report", default="acceptance-result.json")
    parser.add_argument("--confirm-load", action="store_true")
    run(parser.parse_args())
