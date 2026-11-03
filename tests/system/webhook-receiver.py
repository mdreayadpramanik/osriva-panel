"""Records every POST as one JSON line (headers + base64 body) for the system test.

Usage: webhook-receiver.py PORT OUT_FILE [FAIL_FIRST]: the first FAIL_FIRST requests
are answered with HTTP 503 (and recorded) to exercise the panel's retries.
"""
import base64, http.server, json, sys, threading

OUT = sys.argv[2]
fail_left = int(sys.argv[3]) if len(sys.argv) > 3 else 0
lock = threading.Lock()

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        global fail_left
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        with lock:
            status = 204
            if fail_left > 0:
                fail_left -= 1
                status = 503
            with open(OUT, "a") as f:
                f.write(json.dumps({"path": self.path, "status": status, "headers": dict(self.headers), "body": base64.b64encode(body).decode()}) + "\n")
        self.send_response(status)
        self.end_headers()

    def log_message(self, *args):
        pass

http.server.ThreadingHTTPServer(("0.0.0.0", int(sys.argv[1])), Handler).serve_forever()
