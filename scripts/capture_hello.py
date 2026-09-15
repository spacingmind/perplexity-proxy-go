#!/usr/bin/env python3
"""Capture a fresh curl_cffi impersonate=chrome ClientHello.

Writes the raw TLS record (ClientHello incl. record header) to the file
given as argv[1]. Exit 0 on success.

Used by pplx (perplexity-proxy-go) as a runtime self-healing source: the
embedded static ClientHello gets burned by fingerprint replay detection, so
a fresh one is captured when handshakes start failing.
"""
import socket
import sys
import threading


def capture() -> bytes:
    holder = {}
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", 0))
    srv.listen(1)
    port = srv.getsockname()[1]

    def serve():
        conn, _ = srv.accept()
        data = b""
        while len(data) < 5 or len(data) < 5 + int.from_bytes(data[3:5], "big"):
            chunk = conn.recv(65536)
            if not chunk:
                break
            data += chunk
        holder["hello"] = data
        conn.close()

    t = threading.Thread(target=serve)
    t.start()

    from curl_cffi import requests as cr

    try:
        s = cr.Session(impersonate="chrome")
        s.get(f"https://127.0.0.1:{port}/", timeout=5, verify=False)
    except Exception:
        pass
    t.join(5)
    srv.close()
    hello = holder.get("hello", b"")
    if len(hello) < 100:
        raise SystemExit("capture failed: too few bytes")
    return hello


if __name__ == "__main__":
    out = sys.argv[1] if len(sys.argv) > 1 else "/dev/stdout"
    with open(out, "wb") as f:
        f.write(capture())
