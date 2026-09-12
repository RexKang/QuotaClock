"""UI 联调用的最小 mock 上游：4 家平台各自的路径返回一份可解析的额度 JSON。

用法：python mock_upstream.py <port>
     MOCK_STATUS="opencode=429,kimi-code=500" 可让指定平台返回指定状态码
     （用来在页面上验证「采集异常」(429) 与「采集失败」(5xx) 的展示）
"""
import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8899
RESET = int(time.time()) + 3600

# 平台标识 → 路径片段（与 config/platform.go 的预设对应）
PATHS = {
    "zhipu-glm": "/api/monitor/usage/quota/limit",
    "deepseek": "/user/balance",
    "kimi-code": "/usages",
    "opencode": "/usage",
}
STATUS = {}
for pair in (os.environ.get("MOCK_STATUS") or "").split(","):
    if "=" in pair:
        k, v = pair.split("=", 1)
        STATUS[k.strip()] = int(v.strip())

# MOCK_DELAY=8：上游延迟应答（用来观察「页面首次打开、首轮采集还没回来」的等待态）
DELAY = float(os.environ.get("MOCK_DELAY") or 0)


def platform_of(path):
    for pid, frag in PATHS.items():
        if frag in path:
            return pid
    return ""


def payload(path):
    if "/api/monitor/usage/quota/limit" in path:
        return {
            "code": 200,
            "success": True,
            "data": {
                "limits": [
                    {
                        "type": "TOKENS_LIMIT",
                        "percentage": 42,
                        "currentValue": 4200,
                        "usage": 4200,
                        "remaining": 5800,
                        "number": 10000,
                        "nextResetTime": RESET * 1000,
                    }
                ]
            },
        }
    if "/user/balance" in path:
        return {"is_available": True, "balance_infos": [{"currency": "CNY", "total_balance": "88.50", "granted_balance": "8.50", "topped_up_balance": "80.00"}]}
    if "/usages" in path:
        return {
            "success": True,
            "data": {
                "usage": {"limit": 2000, "used": 500, "remaining": 1500, "resetTime": RESET * 1000},
                "limits": [{"window": "5h", "percentage": 25, "used": 500, "limit": 2000, "reset_at": RESET}],
            },
        }
    if "/usage" in path:
        return {"success": True, "data": {"percentage": 61, "used": 6100, "limit": 10000, "reset_at": RESET, "plan": "Go"}}
    return {"success": True, "data": {"percentage": 7}}


class H(BaseHTTPRequestHandler):
    def do_GET(self):
        if DELAY:
            time.sleep(DELAY)
        pid = platform_of(self.path)
        code = STATUS.get(pid, 200)
        if code != 200:
            body = json.dumps({"error": {"message": f"mock {code} for {pid}"}}).encode()
            self.send_response(code)
            if code == 429:
                self.send_header("Retry-After", "120")
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            print(f"[mock] {self.path} -> {code}", flush=True)
            return
        body = json.dumps(payload(self.path)).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        print("[mock]", self.path, flush=True)


print(f"mock upstream on http://127.0.0.1:{PORT} status={STATUS or 'all 200'}", flush=True)
ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
