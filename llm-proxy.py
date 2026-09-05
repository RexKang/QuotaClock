# -*- coding: utf-8 -*-
"""
LLM 用量采集本机代理（解决 CORS）
openai 系网关 CORS 策略不一：opencode.ai 的 server function 与 api.kimi.com 都不放行跨域，
浏览器里的 dashboard 直连会被 CORS 拦截。本代理在 127.0.0.1:8787 转发并注入 CORS 头。

用法:  python llm-proxy.py
dashboard 对应平台 baseURL 填 http://127.0.0.1:8787/<prefix>：
  /kimi/*      → https://api.kimi.com/coding/v1/*
  /opencode/*  → https://opencode.ai/*
"""
import http.server
import urllib.request
import urllib.parse
import sys

PORT = 8787
ROUTES = {
    "/kimi": "https://api.kimi.com/coding/v1",
    "/opencode": "https://opencode.ai",
}


class Handler(http.server.BaseHTTPRequestHandler):
    def _cors(self):
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "X-Cookie, Cookie, Authorization, x-server-id, x-server-instance, Content-Type")
        self.send_header("Access-Control-Max-Age", "3600")

    def do_OPTIONS(self):
        self.send_response(204)
        self._cors()
        self.end_headers()

    def _forward(self, method):
        path = self.path
        target = None
        for prefix, upstream in ROUTES.items():
            if path.startswith(prefix + "/") or path == prefix:
                target = upstream + path[len(prefix):]
                break
        # 默认路由：无前缀的 /_server 请求兼容旧配置 → opencode.ai
        if not target and path.startswith("/_server"):
            target = "https://opencode.ai" + path
        if not target:
            self.send_response(404)
            self._cors()
            self.end_headers()
            self.wfile.write(b"unknown route")
            return

        # 透传鉴权相关头（X-Cookie 由 dashboard 传入 → 转标准 Cookie / Authorization / SolidStart 协议头）
        headers = {
            "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
            "Accept": "*/*",
        }
        cookie = self.headers.get("X-Cookie") or self.headers.get("Cookie")
        if cookie:
            headers["Cookie"] = cookie
        for h in ("Authorization", "x-server-id", "x-server-instance", "Content-Type"):
            v = self.headers.get(h)
            if v:
                headers[h] = v

        data = None
        if method != "GET":
            length = int(self.headers.get("Content-Length") or 0)
            data = self.rfile.read(length) if length else None

        req = urllib.request.Request(target, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=25) as resp:
                body = resp.read()
                self.send_response(200)
                self.send_header("Content-Type", resp.headers.get("Content-Type", "text/plain"))
                self._cors()
                self.end_headers()
                self.wfile.write(body)
        except urllib.error.HTTPError as e:
            body = e.read()
            self.send_response(e.code)
            self.send_header("Content-Type", e.headers.get("Content-Type", "text/plain"))
            self._cors()
            self.end_headers()
            self.wfile.write(body)
        except Exception as e:
            self.send_response(502)
            self._cors()
            self.end_headers()
            self.wfile.write(("proxy error: " + str(e)).encode("utf-8"))

    def do_GET(self):
        self._forward("GET")

    def do_POST(self):
        self._forward("POST")

    def log_message(self, fmt, *args):
        # 调试：确认 X-Cookie 是否送达（浏览器 fetch 会丢弃 Cookie 头，必须走 X-Cookie）
        try:
            xc = self.headers.get("X-Cookie") or ""
            ck = self.headers.get("Cookie") or ""
            if xc:
                print(f"[proxy] X-Cookie received ({len(xc)} chars) -> {self.path[:60]}", flush=True)
            elif ck:
                print(f"[proxy] Cookie received ({len(ck)} chars) -> {self.path[:60]}", flush=True)
        except Exception:
            pass


if __name__ == "__main__":
    print(f"LLM proxy listening on http://127.0.0.1:{PORT}")
    for p, u in ROUTES.items():
        print(f"  {p}/*  ->  {u}/*")
    http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
