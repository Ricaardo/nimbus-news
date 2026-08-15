#!/usr/bin/env python3
import json
import os
import signal
import subprocess
import sys
import time

MAX = 262144


def send(value):
    sys.stdout.write(json.dumps(value, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def result(request_id, value):
    send({"jsonrpc": "2.0", "id": request_id, "result": value})


if os.environ.get("IGNORE_TERM") == "1":
    def ignore_term(_signum, _frame):
        marker = os.environ.get("TERM_FILE")
        if marker:
            with open(marker, "w", encoding="utf-8") as handle:
                handle.write("term")
    signal.signal(signal.SIGTERM, ignore_term)


for raw in sys.stdin:
    message = json.loads(raw)
    method = message.get("method")
    request_id = message.get("id")
    params = message.get("params") or {}

    received_file = os.environ.get("RECEIVED_FILE")
    if received_file and method != "$/cancelRequest":
        with open(received_file, "a", encoding="utf-8") as handle:
            handle.write(str(method) + "\n")

    if method == "$/cancelRequest":
        continue
    if method == "initialize":
        result(request_id, {"version": 1, "capabilities": ["fake"]})
    elif method == "health":
        result(request_id, {"status": "ok"})
    elif method == "shutdown":
        if os.environ.get("SHUTDOWN_GRANDCHILD") == "ignore-term":
            child = subprocess.Popen([
                sys.executable, "-c",
                "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)",
            ])
            marker = os.environ.get("TEST_PID_FILE")
            if marker:
                with open(marker, "w", encoding="utf-8") as handle:
                    handle.write(str(child.pid))
        result(request_id, {"status": "ok"})
        sys.exit(0)
    elif method == "echo":
        result(request_id, params)
    elif method == "bidirectional":
        send({"jsonrpc": "2.0", "id": "child-approval", "method": "tool.approval", "params": {"request_id": request_id, "tool_name": "publish", "input": {"value": 1}}})
        approval = json.loads(sys.stdin.readline())
        result(request_id, {"approved": approval.get("result", {}).get("approved", False)})
    elif method == "bad_binding":
        send({"jsonrpc": "2.0", "id": "child-bad", "method": "tool.approval", "params": {"request_id": "wrong-parent", "tool_name": "publish", "input": {}}})
        approval = json.loads(sys.stdin.readline())
        result(request_id, {"domain": approval.get("error", {}).get("data", {}).get("domain", "")})
    elif method == "reverse_flood":
        for child_id in ["child-one", "child-two"]:
            send({"jsonrpc": "2.0", "id": child_id, "method": "tool.approval", "params": {"request_id": request_id, "tool_name": "publish", "input": {}}})
        responses = [json.loads(sys.stdin.readline()), json.loads(sys.stdin.readline())]
        domains = [item.get("error", {}).get("data", {}).get("domain", "ok") for item in responses]
        result(request_id, {"domains": domains})
    elif method == "reverse_hang":
        send({"jsonrpc": "2.0", "id": "child-hang", "method": "tool.approval", "params": {"request_id": request_id, "tool_name": "publish", "input": {}}})
        while True:
            incoming = json.loads(sys.stdin.readline())
            if incoming.get("id") == "child-hang":
                break
    elif method == "hang":
        continue
    elif method == "malformed":
        sys.stdout.write("not-json\n")
        sys.stdout.flush()
    elif method == "oversize":
        sys.stdout.write("x" * (MAX + 1) + "\n")
        sys.stdout.flush()
    elif method == "crash":
        sys.exit(17)
    elif method == "slow":
        for delta in ["a", "b", "c"]:
            send({"jsonrpc": "2.0", "method": "agent.text_delta", "params": {"delta": delta}})
            time.sleep(0.02)
        result(request_id, {"done": True})
    elif method == "stderr":
        sys.stderr.write("Authorization: Bearer canary-bearer api_key=canary-api known=known-canary\n")
        sys.stderr.flush()
        result(request_id, {"done": True})
    elif method == "stderr_huge":
        sys.stderr.write("x" * (2 * MAX) + "\n")
        sys.stderr.write("after-drain-canary\n")
        sys.stderr.flush()
        result(request_id, {"done": True})
    elif method == "error_canary":
        send({"jsonrpc": "2.0", "id": request_id, "error": {"code": -32000, "message": "Bearer token-canary https://alice:password@private.invalid " + os.path.expanduser("~/sensitive/path"), "data": {"domain": "dependency_down", "retryable": True}}})
    elif method == "env":
        sensitive = [key for key in os.environ if any(term in key.upper() for term in ["TOKEN", "SECRET", "PASSWORD", "API_KEY", "BASE_URL", "CREDENTIAL"])]
        result(request_id, {"sensitive": sensitive, "home_present": bool(os.environ.get("HOME")), "path_present": bool(os.environ.get("PATH")), "safe_value": os.environ.get("SAFE_RUNTIME_MODE", "")})
    elif method == "spawn_grandchild":
        child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
        marker = os.environ.get("TEST_PID_FILE")
        if marker:
            with open(marker, "w", encoding="utf-8") as handle:
                handle.write(str(child.pid))
        result(request_id, {"pid": child.pid})
    elif method == "spawn_grandchild_ignore_term":
        child = subprocess.Popen([
            sys.executable, "-c",
            "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)",
        ])
        marker = os.environ.get("TEST_PID_FILE")
        if marker:
            with open(marker, "w", encoding="utf-8") as handle:
                handle.write(str(child.pid))
        result(request_id, {"pid": child.pid})
    else:
        send({"jsonrpc": "2.0", "id": request_id, "error": {"code": -32000, "message": "unknown", "data": {"domain": "protocol_mismatch", "retryable": False}}})
