"""Records every POST as one JSON line (headers + base64 body) for the system test."""
import base64, http.server, json, sys

OUT = sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        with open(OUT, "a") as f:
            f.write(json.dumps({"path": self.path, "headers": dict(self.headers), "body": base64.b64encode(body).decode()}) + "\n")
        self.send_response(204)
        self.end_headers()

    def log_message(self, *args):
        pass

http.server.ThreadingHTTPServer(("0.0.0.0", int(sys.argv[1])), Handler).serve_forever()
