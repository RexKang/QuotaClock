"""UI 联调用的最小 mock 上游：4 家平台各自的路径返回一份可解析的额度 JSON。

用法：python mock_upstream.py <port>
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8899
RESET = int(time.time()) + 3600


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
        body = json.dumps(payload(self.path)).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        print("[mock]", self.path, flush=True)


print(f"mock upstream on http://127.0.0.1:{PORT}", flush=True)
ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
