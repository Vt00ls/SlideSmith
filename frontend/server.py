#!/usr/bin/env python3
"""Small static/proxy server for network-limited prebuilt deployments."""

from __future__ import annotations

import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen


DIST_DIR = Path(os.environ.get("SLIDESMITH_FRONTEND_DIST", "/app/frontend/dist")).resolve()
API_UPSTREAM = os.environ.get("SLIDESMITH_API_UPSTREAM", "http://api:18080").rstrip("/")
ADDR = os.environ.get("SLIDESMITH_FRONTEND_ADDR", "0.0.0.0:8080")

HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}

MIME_TYPES = {
    ".css": "text/css; charset=utf-8",
    ".html": "text/html; charset=utf-8",
    ".js": "text/javascript; charset=utf-8",
    ".json": "application/json; charset=utf-8",
    ".map": "application/json; charset=utf-8",
    ".svg": "image/svg+xml",
    ".txt": "text/plain; charset=utf-8",
    ".ico": "image/x-icon",
    ".png": "image/png",
    ".jpg": "image/jpeg",
    ".jpeg": "image/jpeg",
    ".webp": "image/webp",
}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self) -> None:
        if self.is_proxy_path():
            self.proxy()
            return
        self.serve_static()

    def do_HEAD(self) -> None:
        if self.is_proxy_path():
            self.proxy(head_only=True)
            return
        self.serve_static(head_only=True)

    def do_POST(self) -> None:
        self.proxy()

    def do_PUT(self) -> None:
        self.proxy()

    def do_DELETE(self) -> None:
        self.proxy()

    def is_proxy_path(self) -> bool:
        return self.path == "/healthz" or self.path.startswith("/api/")

    def serve_static(self, head_only: bool = False) -> None:
        target = self.static_target()
        if target is None:
            self.send_error(404)
            return
        payload = b"" if head_only else target.read_bytes()
        content_type = MIME_TYPES.get(target.suffix.lower(), "application/octet-stream")
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(target.stat().st_size))
        self.send_header("Cache-Control", "no-cache" if target.name == "index.html" else "public, max-age=3600")
        self.end_headers()
        if not head_only:
            self.wfile.write(payload)

    def static_target(self) -> Path | None:
        path = urlsplit(self.path).path
        rel = path.lstrip("/")
        candidate = (DIST_DIR / rel).resolve()
        if candidate.is_file() and DIST_DIR in candidate.parents:
            return candidate
        index = DIST_DIR / "index.html"
        if index.is_file():
            return index
        return None

    def proxy(self, head_only: bool = False) -> None:
        body = self.read_body()
        target = API_UPSTREAM + self.path
        headers = {
            key: value
            for key, value in self.headers.items()
            if key.lower() not in HOP_BY_HOP_HEADERS and key.lower() != "host"
        }
        req = Request(target, data=body, method=self.command, headers=headers)
        try:
            with urlopen(req, timeout=3600) as response:
                self.send_response(response.status)
                self.copy_response_headers(response.headers.items())
                if head_only:
                    payload = b""
                else:
                    payload = response.read()
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                if payload:
                    self.wfile.write(payload)
        except HTTPError as exc:
            payload = b"" if head_only else exc.read()
            self.send_response(exc.code)
            self.copy_response_headers(exc.headers.items())
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            if payload:
                self.wfile.write(payload)
        except URLError as exc:
            payload = f"upstream error: {exc.reason}".encode("utf-8")
            self.send_response(502)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            if not head_only:
                self.wfile.write(payload)

    def read_body(self) -> bytes | None:
        length = int(self.headers.get("Content-Length", "0") or "0")
        if length <= 0:
            return None
        return self.rfile.read(length)

    def copy_response_headers(self, headers: list[tuple[str, str]]) -> None:
        for key, value in headers:
            if key.lower() in HOP_BY_HOP_HEADERS or key.lower() == "content-length":
                continue
            self.send_header(key, value)

    def log_message(self, fmt: str, *args: object) -> None:
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)


def main() -> None:
    host, raw_port = ADDR.rsplit(":", 1)
    server = ThreadingHTTPServer((host, int(raw_port)), Handler)
    print(f"slidesmith frontend serving {DIST_DIR} on {ADDR}, api={API_UPSTREAM}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
