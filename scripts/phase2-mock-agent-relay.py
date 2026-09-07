#!/usr/bin/env python3
"""Phase 2 local agent stand-in: same event protocol as the real routine.

Listens on 127.0.0.1:18765, accepts POST /agent-job (legacy alias: /valentine-job),
responds 200 immediately, then in a background thread posts
assistant_start → assistant_delta* → assistant_done to events_url and POST
complete_url with Authorization: Bearer <GROK_BRIDGE_SECRET>.
"""
from __future__ import annotations

import json
import os
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HOST = os.environ.get("PHASE2_RELAY_HOST", "127.0.0.1")
PORT = int(os.environ.get("PHASE2_RELAY_PORT", "18765"))
SECRET = os.environ.get("GROK_BRIDGE_SECRET", "").strip()
JOB_PATHS = ("/agent-job", "/valentine-job")


def post_json(url: str, body: dict, secret: str) -> tuple[int, str]:
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {secret}",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", errors="replace")


def handle_job(payload: dict) -> None:
    secret = (
        (payload.get("bridge_secret") or payload.get("secret") or "").strip()
        or SECRET
        or os.environ.get("GROK_BRIDGE_SECRET", "").strip()
    )
    if not secret:
        print("[relay] ERROR: no GROK_BRIDGE_SECRET available", flush=True)
        return

    events_url = payload.get("events_url") or ""
    complete_url = payload.get("complete_url") or ""
    session_id = payload.get("session_id") or ""
    user_message = payload.get("user_message") or ""
    job_id = payload.get("job_id") or ""

    if not events_url or not complete_url:
        print(f"[relay] ERROR: missing events_url/complete_url job={job_id}", flush=True)
        return

    # Small delay so hub has registered the wait loop
    time.sleep(0.15)

    reply = f"Phase2Relay: got your message — {user_message}"
    chunks = [
        "Phase2Relay: ",
        "got your message",
        " — ",
        str(user_message),
    ]

    print(f"[relay] job={job_id} session={session_id} posting events → {events_url}", flush=True)

    code, body = post_json(
        events_url,
        {"type": "assistant_start", "session_id": session_id},
        secret,
    )
    print(f"[relay] assistant_start → {code} {body[:200]}", flush=True)

    for chunk in chunks:
        time.sleep(0.05)
        code, body = post_json(
            events_url,
            {"type": "assistant_delta", "session_id": session_id, "delta": chunk},
            secret,
        )
        print(f"[relay] assistant_delta → {code}", flush=True)

    code, body = post_json(
        events_url,
        {"type": "assistant_done", "session_id": session_id, "content": reply},
        secret,
    )
    print(f"[relay] assistant_done → {code} {body[:200]}", flush=True)

    code, body = post_json(
        complete_url,
        {"content": reply},
        secret,
    )
    print(f"[relay] complete → {code} {body[:300]}", flush=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt: str, *args) -> None:
        print(f"[relay-http] {self.address_string()} {fmt % args}", flush=True)

    def _read_json(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            return json.loads(raw.decode("utf-8") or "{}")
        except json.JSONDecodeError:
            return {}

    def _write(self, status: int, obj: dict) -> None:
        data = json.dumps(obj).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:
        if self.path in ("/", "/health"):
            self._write(200, {"ok": True, "service": "phase2-mock-agent-relay", "port": PORT})
            return
        self._write(404, {"error": "not found"})

    def do_POST(self) -> None:
        path = self.path.split("?", 1)[0]
        if path not in JOB_PATHS:
            self._write(404, {"error": "not found"})
            return
        payload = self._read_json()
        job_id = payload.get("job_id", "?")
        print(f"[relay] accepted job={job_id} path={path} keys={list(payload.keys())}", flush=True)
        # Respond 200 immediately; work in background (same as real agent routine)
        self._write(200, {"ok": True, "accepted": True, "job_id": job_id})
        threading.Thread(target=handle_job, args=(payload,), daemon=True).start()


def main() -> None:
    if not SECRET:
        print("[relay] WARNING: GROK_BRIDGE_SECRET unset at start; will try payload/env later", flush=True)
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    print(f"[relay] listening on http://{HOST}:{PORT}/agent-job (alias /valentine-job)", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
