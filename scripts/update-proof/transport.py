"""A loopback HTTPS relay for refusal probes against the SHIPPED updater.

Only github.com's two nightly manifest paths can be replaced. Artifact requests
still go upstream. Positive checks require live production bytes. No signing key,
Go fetcher, window binding or updater is supplied by this relay.
"""

import base64
import http.server
from pathlib import Path
import select
import socket
import socketserver
import ssl
import threading
import urllib.error
import urllib.request

from common import command, require

BASE = "https://github.com/shady2k/nocx/releases/download/nightly/"


class Relay:
    def __init__(self, root, previous, candidate, lower):
        self.mode = "live"
        self.documents = {"stale": previous, "unsigned": candidate, "lower": lower}
        self.requests = []
        self.signature_reached = threading.Event()
        self.signature_continue = threading.Event()
        self.signature_continue.set()
        self.candidate = candidate
        root = Path(root)
        key, cert = root / "relay.key", root / "relay.crt"
        result = command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                          "-keyout", key, "-out", cert, "-days", "1", "-subj", "/CN=github.com",
                          "-addext", "subjectAltName=DNS:github.com"])
        require(result.returncode == 0, "cannot create isolated transport certificate")
        self.cert = cert
        self.tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        self.tls.load_cert_chain(cert, key)
        self.upstream = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        relay = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_args):
                pass

            def do_CONNECT(self):
                host, port = self.path.rsplit(":", 1)
                if host != "github.com":
                    with socket.create_connection((host, int(port)), timeout=30) as upstream:
                        self.send_response(200)
                        self.end_headers()
                        while True:
                            ready, _, _ = select.select([self.connection, upstream], [], [], 60)
                            if not ready:
                                return
                            for source in ready:
                                data = source.recv(65536)
                                if not data:
                                    return
                                (upstream if source is self.connection else self.connection).sendall(data)
                    return
                self.send_response(200)
                self.end_headers()
                self.connection = relay.tls.wrap_socket(self.connection, server_side=True)
                self.rfile = self.connection.makefile("rb")
                self.wfile = self.connection.makefile("wb")
                self.close_connection = False
                while not self.close_connection:
                    self.handle_one_request()

            def do_GET(self):
                suffix = self.path.removeprefix("/shady2k/nocx/releases/download/nightly/")
                intercepted = suffix in ["manifest.json", "manifest.json.sig"]
                mode = relay.mode
                try:
                    if intercepted and suffix.endswith(".sig"):
                        relay.signature_reached.set()
                        require(relay.signature_continue.wait(timeout=120),
                                "desktop never released signature response barrier")
                    if intercepted and mode != "live":
                        pair = relay.documents[mode]
                        data = Path(pair[1 if suffix.endswith(".sig") else 0]).read_bytes()
                        if mode == "unsigned" and suffix.endswith(".sig"):
                            data = base64.b64encode(bytes(64)) + b"\n"
                        self.send_response(200)
                        self.send_header("Content-Length", str(len(data)))
                        self.end_headers()
                        self.wfile.write(data)
                    else:
                        request = urllib.request.Request("https://github.com" + self.path,
                                                         headers={"User-Agent": "nocx-update-proof"})
                        with relay.upstream.open(request, timeout=120) as response:
                            if intercepted:
                                data = response.read()
                                expected = Path(relay.candidate[1 if suffix.endswith(".sig") else 0]).read_bytes()
                                require(data == expected,
                                        "production nightly alias changed or differs from candidate")
                                self.send_response(200)
                                self.send_header("Content-Length", str(len(data)))
                                self.end_headers()
                                self.wfile.write(data)
                            else:
                                self.send_response(response.status)
                                if response.headers.get("Content-Length"):
                                    self.send_header("Content-Length", response.headers["Content-Length"])
                                self.send_header("Connection", "close")
                                self.end_headers()
                                while chunk := response.read(1024 * 1024):
                                    self.wfile.write(chunk)
                    if intercepted:
                        relay.requests.append({"mode": mode, "path": self.path, "ok": True})
                except Exception as error:
                    relay.requests.append({"mode": mode, "path": self.path, "error": str(error)})
                    self.send_error(502, "proof relay refused upstream bytes")
                self.close_connection = True

        class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
            daemon_threads = True

        self.server = Server(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = "http://127.0.0.1:" + str(self.server.server_port)

    def arm_check(self):
        self.signature_reached.clear()
        self.signature_continue.clear()

    def release_check(self):
        self.signature_continue.set()

    def verify_live(self):
        for index, suffix in enumerate(["manifest.json", "manifest.json.sig"]):
            try:
                with self.upstream.open(BASE + suffix, timeout=60) as response:
                    data = response.read()
            except Exception as error:
                raise RuntimeError("nightly producer is not live / no nightly published yet: " + str(error)) from error
            require(data == Path(self.candidate[index]).read_bytes(),
                    "published nightly does not match the candidate: " + suffix)

    def close(self):
        self.release_check()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
