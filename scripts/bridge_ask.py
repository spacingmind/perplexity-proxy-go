#!/usr/bin/env python3
"""Bridge ask through the reference Python client (curl_cffi engine).

Reads a JSON request on stdin: {"query": str, "model": str (optional),
"source_focus": str (optional), "backend_uuid": str (optional),
"read_write_token": str (optional)}.
Writes a JSON answer on stdout: {"answer": str, "citations": [{"index": int,
"title": str, "url": str}], "thread_title": str, "backend_uuid": str,
"read_write_token": str} or {"error": str}.

Used by pplx when the Go transport's fingerprint is being blocked — the
reference client passes consistently. State (backend uuid/token) round-trips
so follow-ups keep working across bridge and Go paths.
"""
import json
import sys

sys.path.insert(0, "/home/longnp/Coding/personal/smind/refs/perplexity-web-mcp/src")

from perplexity_web_mcp import Perplexity, ConversationConfig, Models


def main() -> None:
    req = json.loads(sys.stdin.read())
    token = json.load(open("/home/longnp/.pplx/token.json"))["token"]
    try:
        c = Perplexity(session_token=token)
        model = Models.AUTO
        ident = req.get("model") or ""
        for name in dir(Models):
            m = getattr(Models, name)
            if name.startswith("_") or not hasattr(m, "identifier"):
                continue
            if m.identifier == ident:
                model = m
                break
        conv = c.create_conversation(ConversationConfig(model=model))
        if req.get("backend_uuid"):
            conv.restore_session(req["backend_uuid"], req.get("read_write_token") or None)
        conv.ask(req["query"])
        citations = [
            {"index": i + 1, "title": getattr(r, "title", "") or "", "url": getattr(r, "url", "") or ""}
            for i, r in enumerate(conv.search_results or [])
        ]
        print(json.dumps({
            "answer": conv.answer or "",
            "citations": citations,
            "thread_title": conv.title or "",
            "backend_uuid": conv.uuid or "",
            "read_write_token": conv.read_write_token or "",
        }))
    except Exception as e:  # noqa: BLE001 — report any failure as JSON
        print(json.dumps({"error": f"{type(e).__name__}: {e}"}))
        sys.exit(1)


if __name__ == "__main__":
    main()
