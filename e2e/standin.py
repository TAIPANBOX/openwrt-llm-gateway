#!/usr/bin/env python3
"""A stand-in model server for the vlab run: answers the OpenAI routes the way a careless home
model server does, on every address and with no key, which is what the gateway is there for.

It streams (server-sent events) when the request asks for it, as real servers do: an agent that
asks for a stream and gets a plain JSON body reports "an empty stream with no finish_reason"
(Hermes did, on flint2, 2026-11-01). Usage is sent in the last chunk.

  standin.py [host] [port] [key]    default: every address, 8080, no key
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ANSWER = "stand-in answer"
USAGE = {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def _json(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def _sse(self, model):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        base = {"id": "chatcmpl-standin", "object": "chat.completion.chunk", "model": model}
        chunks = [
            {**base, "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}]},
            {**base, "choices": [{"index": 0, "delta": {"content": ANSWER}, "finish_reason": None}]},
            {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
            {**base, "choices": [], "usage": USAGE},
        ]
        for c in chunks:
            self.wfile.write(b"data: " + json.dumps(c).encode() + b"\n\n")
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
        self.close_connection = True

    def do_GET(self):
        if self.path == "/health":
            return self._json(200, {"ok": True})
        if self.path == "/v1/models":
            return self._json(200, {"object": "list", "data": [{"id": "stand-in", "object": "model"}]})
        self._json(404, {})

    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        req = json.loads(self.rfile.read(n) or b"{}")
        if self.path != "/v1/chat/completions":
            return self._json(404, {})
        if KEY and self.headers.get("Authorization") != "Bearer " + KEY:
            return self._json(401, {"error": "the backend's key did not arrive"})
        if req.get("stream"):
            return self._sse(req.get("model"))
        self._json(200, {"id": "chatcmpl-standin", "object": "chat.completion", "model": req.get("model"),
                         "choices": [{"index": 0, "message": {"role": "assistant", "content": ANSWER},
                                      "finish_reason": "stop"}],
                         "usage": USAGE})


host = sys.argv[1] if len(sys.argv) > 1 else ""
port = int(sys.argv[2]) if len(sys.argv) > 2 else 8080
KEY = sys.argv[3] if len(sys.argv) > 3 else ""
ThreadingHTTPServer((host, port), H).serve_forever()
