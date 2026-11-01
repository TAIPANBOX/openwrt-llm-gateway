#!/usr/bin/env python3
"""A stand-in model server for the vlab run: answers the OpenAI routes the way a careless home
model server does, on every address and with no key, which is what the gateway is there for."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class H(BaseHTTPRequestHandler):
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
        self._json(200, {"object": "chat.completion", "model": req.get("model"),
                         "choices": [{"message": {"role": "assistant", "content": "stand-in answer"}}],
                         "usage": {"prompt_tokens": 12, "completion_tokens": 3}})

ThreadingHTTPServer(("", 8080), H).serve_forever()
