"""Local harness tests. These are NOT tunnel/Go acceptance results."""
import contextlib
import http.server
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import threading
from types import SimpleNamespace
import unittest

BASE = Path(__file__).parent

def load(name):
    spec = importlib.util.spec_from_file_location(name, BASE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

stress = load("mcp_stress")
fixture = load("prepare_fixture")

@contextlib.contextmanager
def serve(handler):
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield "http://127.0.0.1:" + str(server.server_port)
    finally:
        server.shutdown(); server.server_close(); thread.join(timeout=2)


class HarnessTests(unittest.TestCase):
    def test_fixture_can_be_cloned_over_http(self):
        with tempfile.TemporaryDirectory() as directory:
            with contextlib.redirect_stdout(io.StringIO()):
                root = fixture.prepare(Path(directory) / "fixture", 1)  # SMALL unit fixture, not acceptance
            class Handler(http.server.SimpleHTTPRequestHandler):
                def __init__(self, *args, **kwargs):
                    super().__init__(*args, directory=str(root), **kwargs)
                def log_message(self, *_):
                    pass
            with serve(Handler) as url:
                destination = Path(directory) / "cloned"
                subprocess.run(["git", "clone", "--no-local", url + "/fixture.git", str(destination)], check=True, capture_output=True)
                self.assertEqual((destination / "payload.bin").stat().st_size, 1024 * 1024)
            with self.assertRaises(ValueError):
                fixture.prepare(root, 1)

    def test_driver_requires_explicit_load_confirmation(self):
        with self.assertRaises(ValueError):
            stress.run(SimpleNamespace(confirm_load=False))

    def test_driver_polling_and_failure_cancellation_with_mock_mcp(self):
        for fail in (False, True):
            events = []
            class Handler(http.server.BaseHTTPRequestHandler):
                def log_message(self, *_):
                    pass
                def reply(self, data, code=200):
                    blob = json.dumps(data).encode()
                    self.send_response(code); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(blob))); self.end_headers(); self.wfile.write(blob)
                def do_GET(self):
                    self.reply({"ok": not fail, "name": "devspace-go"})
                def do_POST(self):
                    data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    method = data["method"]
                    if method == "notifications/initialized":
                        self.reply({}); return
                    result = {}
                    if method == "initialize":
                        result = {"protocolVersion": "2025-03-26"}
                    elif method == "tools/list":
                        result = {"tools": [{"name": n} for n in ("bash", "bash_status", "bash_cancel")]}
                    else:
                        tool = data["params"]["name"]; events.append(tool)
                        values = {"workspaceId": "ws"} if tool == "open_default_workspace" else {"job_id": "job", "state": "queued"}
                        if tool == "bash_status":
                            values.update(state="completed", exit_code=0, result="ACCEPTANCE_LOAD_OK MOCK_ONLY", next_offset=28, has_more=False)
                        result = {"structuredContent": values}
                    self.reply({"jsonrpc": "2.0", "id": data["id"], "result": result})
            with tempfile.TemporaryDirectory() as directory, serve(Handler) as url:
                args = SimpleNamespace(confirm_load=True, workers=1, load_seconds=10, deadline=20, max_latency=2,
                    mcp_url=url + "/mcp", repo_url=url + "/fixture.git", download_url=url + "/payload.bin", python="python3", report=str(Path(directory) / "mock-only.json"))
                with contextlib.redirect_stdout(io.StringIO()):
                    if fail:
                        with self.assertRaises(SystemExit):
                            stress.run(args)
                        self.assertIn("bash_cancel", events)
                    else:
                        stress.run(args)
                        self.assertIn("bash_status", events)
                report = json.loads(Path(args.report).read_text())
                self.assertEqual(report["accepted"], not fail)


if __name__ == "__main__":
    unittest.main()
