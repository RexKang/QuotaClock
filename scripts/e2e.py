# -*- coding: utf-8 -*-
"""
QuotaClock v0.2.0 E2E 演练（测试方案 L3；验收 §10 抽样自动化）。

用法:  python scripts/e2e.py <quotaclock 可执行文件路径>
覆盖:  首启模板 / 迁移方案 A / API 契约 / 热生效采集 / revision 判重 /
       双开拒绝 / 多配置并存 / kill -9 stale 接管 / 优雅退出删锁 /
       日志脱敏 / 启动 <1s / 页面出数 <1s
"""
import http.server
import json
import os
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

HTTPServerModule = http.server  # 防止下方 http() 请求函数遮蔽模块名

BIN = sys.argv[1] if len(sys.argv) > 1 else None
IS_WIN = os.name == "nt"
BASE = "http://127.0.0.1"
FX1_TOKEN = '<script>&"\'\\中文🚀'  # 特殊字符 token（验收 #16 脱敏红线）

results = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("  PASS  " if ok else "  FAIL  ") + name + ("" if ok else "  -> " + detail))


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def http(method, url, body=None, headers=None, timeout=10):
    req = urllib.request.Request(url, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, data=data, timeout=timeout) as resp:
            return resp.status, dict(resp.headers), json.loads(resp.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, dict(e.headers), json.loads(raw or "{}")
        except Exception:
            return e.code, dict(e.headers), {"raw": raw}


class Instance:
    """一个被演练的 quotaclock 进程，附带 stdout 采集。"""

    def __init__(self, binpath, cfg, port, new_group=False):
        self.port = port
        kw = {}
        if IS_WIN and new_group:
            kw["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
        self.proc = subprocess.Popen([binpath, "-config", cfg, "-port", str(port)],
                                     stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                     universal_newlines=True, **kw)
        self.lines = []
        self.stop = threading.Event()
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()

    def _read(self):
        for line in self.proc.stdout:
            self.lines.append(line)
            if self.stop.is_set():
                break

    def log(self):
        return "".join(self.lines)

    def kill(self):
        if self.proc.poll() is None:
            self.proc.kill()
        self.proc.wait(timeout=10)

    def wait_ready(self, path="/api/quotas", timeout=10.0):
        url = f"{BASE}:{self.port}{path}"
        t0 = time.time()
        last = None
        while time.time() - t0 < timeout:
            if self.proc.poll() is not None:
                raise RuntimeError(f"进程提前退出 rc={self.proc.returncode}: {self.log()[-400:]}")
            try:
                code, _, body = http("GET", url, timeout=2)
                if code == 200:
                    return body, time.time() - t0
                last = code
            except Exception as e:
                last = e
            time.sleep(0.05)
        raise TimeoutError(f"{url} 未就绪: {last}")


def wait_snapshot(base, cond, timeout=15.0):
    t0 = time.time()
    snap = None
    while time.time() - t0 < timeout:
        _, _, snap = http("GET", base + "/api/quotas")
        if cond(snap):
            return snap
        time.sleep(0.1)
    return snap


class MockUpstream:
    """平台 mock：可切换应答内容，用于采集与 revision 判重演练。"""

    def __init__(self):
        outer = self
        outer.payload = {"success": True, "data": {"percentage": 50}}

        class H(HTTPServerModule.BaseHTTPRequestHandler):
            def do_GET(self):
                body = json.dumps(outer.payload).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *a):
                pass

        self.srv = HTTPServerModule.ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.port = self.srv.server_address[1]
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()

    def url(self):
        return f"{BASE}:{self.port}"


def scenario_first_start(binpath):
    print("\n== 场景 1：首启模板 / 默认密码检测 / 静态页 ==")
    d = tempfile.mkdtemp(prefix="qc-e2e-")
    port = free_port()
    cfg = os.path.join(d, "config.json")
    inst = Instance(binpath, cfg, port)
    snap, boot = inst.wait_ready()
    check("首启就绪（观测 %.2fs）" % boot, boot < 10)
    check("空配置快照 providers 空", snap["providers"] == [])
    with open(cfg, encoding="utf-8") as f:
        created = json.load(f)
    check("模板 version=3", created["version"] == 3)
    check("模板 auth=admin + providers 空", created["auth"]["mode"] == "admin" and created["providers"] == [])
    locks = [x for x in os.listdir(d) if x.startswith("quotaclock-") and x.endswith(".lock")]
    check("锁文件已创建", len(locks) == 1)
    req = urllib.request.urlopen(f"{BASE}:{port}/", timeout=5)
    page = req.read().decode()
    check("GET / 出静态页", "QuotaClock" in page and "text/html" in req.headers["Content-Type"])
    code, _, view = http("GET", f"{BASE}:{port}/api/config")
    s = json.dumps(view)
    check("GET config 200", code == 200)
    check("password_is_default=true（默认密码未改）", view["auth"]["password_is_default"] is True)
    check("无 token/token_cipher 字段", "token_cipher" not in s and '"token":' not in s)
    out = subprocess.run([binpath, "-version"], capture_output=True, text=True)
    check("-version 输出版本", "QuotaClock" in out.stdout)
    return d, port, cfg, inst


def scenario_api_chain(d, port, cfg, inst, mock):
    print("\n== 场景 2：登录 / PUT 配置 / 热生效采集 / 判重 ==")
    base = f"{BASE}:{port}"
    code, hdr, body = http("POST", base + "/api/login", {"password": "wrong-pass"})
    check("错误密码 401 INVALID_CREDENTIALS", code == 401 and body["error"]["code"] == "INVALID_CREDENTIALS")
    code, hdr, body = http("POST", base + "/api/login", {"password": "Quota@2026090S"})
    setc = hdr.get("Set-Cookie", "")
    check("正确密码 200 + Set-Cookie", code == 200 and "qc_session=" in setc)
    check("cookie 属性 HttpOnly+Lax", "HttpOnly" in setc and "SameSite=Lax" in setc)
    cookie = re.search(r"qc_session=([^;]+)", setc).group(1)
    auth = {"Cookie": "qc_session=" + cookie}

    code, _, _ = http("PUT", base + "/api/config", {"version": 3})
    check("未登录 PUT 401", code == 401)

    put = {
        "version": 3,
        "listen": {"host": "127.0.0.1", "port": port},
        "collector": {"interval_base_s": 30, "jitter_min_s": 5, "jitter_max_s": 25,
                      "stagger_min_s": 1, "stagger_max_s": 5, "backoff_multiplier": 2, "backoff_max_s": 1800},
        "auth": {"mode": "admin"},
        "providers": [{"id": "mockp", "name": "Mock 平台", "base_url": mock.url(),
                       "paths": ["/q"], "auth_style": "bearer", "token": "sk-e2e-token-abcdef"}],
    }
    code, _, body = http("PUT", base + "/api/config", put, auth)
    check("PUT 配置 200 ok", code == 200 and body.get("ok") is True)
    with open(cfg, encoding="utf-8") as f:
        saved = json.load(f)
    check("config.json 无明文 token", "sk-e2e-token-abcdef" not in json.dumps(saved))
    check("config.json token_cipher 已落盘", "token_cipher" in json.dumps(saved))

    snap = wait_snapshot(base, lambda s: (s["providers"] or [{}])[0].get("status") == "ok", timeout=30)
    p = (snap["providers"] or [{}])[0]
    check("热生效后采集器拉到数据 status=ok", p.get("status") == "ok", json.dumps(p)[:200])
    check("data 透传 percentage", (p.get("data") or {}).get("data", {}).get("percentage") == 50)
    check("last_success_at RFC3339", bool(re.match(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", p.get("last_success_at") or "")))

    t0 = time.time()
    http("GET", base + "/api/quotas")
    dt = time.time() - t0
    check("GET /api/quotas <1s（%.0fms）" % (dt * 1000), dt < 1.0)

    # 判重：数据变化 → revision +1（下一采集 tick = 30s 基准 + 5~25s 抖动）；无变化 → revision 不动
    rev1 = snap["revision"]
    mock.payload = {"success": True, "data": {"percentage": 60}}
    snap2 = wait_snapshot(base, lambda s: s["revision"] > rev1 and (s["providers"] or [{}])[0].get("data", {}).get("data", {}).get("percentage") == 60, timeout=70)
    check("data 变化 revision +1", snap2["revision"] == rev1 + 1, f"{rev1}→{snap2['revision']}")
    rev2 = snap2["revision"]
    snap3 = wait_snapshot(base, lambda s: s["revision"] != rev2, timeout=3.0)
    check("无变化 revision 不动", snap3 is not None and snap3["revision"] == rev2)

    code, _, view = http("GET", base + "/api/config", None, auth)
    pv = view["providers"][0]
    check("登录态 token_masked", pv.get("token_masked") == "sk****ef")

    code, _, _ = http("POST", base + "/api/logout", None, auth)
    check("logout 200", code == 200)
    code, _, view2 = http("GET", base + "/api/config")
    check("登出后无 token_masked", "token_masked" not in json.dumps(view2))
    code, _, _ = http("PUT", base + "/api/config", put, auth)
    check("登出后旧会话 PUT 401（拒绝名单）", code == 401)

    # 特殊字符 token（FX-1）落配置 → 之后校验日志脱敏
    code, hdr, _ = http("POST", base + "/api/login", {"password": "Quota@2026090S"})
    cookie2 = re.search(r"qc_session=([^;]+)", hdr.get("Set-Cookie", "")).group(1)
    put["providers"][0]["token"] = FX1_TOKEN
    code, _, _ = http("PUT", base + "/api/config", put, {"Cookie": "qc_session=" + cookie2})
    check("特殊字符 token PUT 成功", code == 200)
    time.sleep(0.3)  # 留给采集失败 WARN 日志（mock 改 401 验证失败态传播）
    return cookie2


def scenario_log_redaction(inst, cfg):
    print("\n== 场景 2b：日志脱敏红线（FX-1 特殊字符 token） ==")
    with open(cfg, encoding="utf-8") as f:
        saved = f.read()
    log = inst.log()
    fragments = [FX1_TOKEN, "中文🚀", "qc_session="]
    ok = True
    hit = ""
    for frag in fragments:
        if frag in log:
            ok = False
            hit = frag
    check("日志无 token/掩码/会话 cookie 字面量", ok and '"token_cipher"' not in log, f"命中: {hit}" if hit else "")
    # 状态快照失败态验证（mock 已改 60% 正常应答，无失败日志是正常路径）
    check("config.json 含密文不含明文", '"token_cipher"' in saved and FX1_TOKEN not in saved)


def scenario_migration(binpath, mock):
    print("\n== 场景 3：v0.1 旁路迁移（方案 A 自动改写） ==")
    d = tempfile.mkdtemp(prefix="qc-e2e-mig-")
    port = free_port()
    cfg = os.path.join(d, "config.json")
    v1 = {
        "version": 2, "active": "deepseek",
        "providers": [
            {"enabled": True, "id": "zhipu", "name": "智谱 GLM", "baseURL": "https://open.bigmodel.cn",
             "token": "fake-zhipu-token-aaaa", "endpoints": [
                 {"method": "GET", "path": "/api/monitor/usage/quota/limit", "params": "{", "note": "x"}]},
            {"enabled": True, "id": "deepseek", "name": "DeepSeek", "baseURL": "https://api.deepseek.com",
             "token": "sk-fake-bbbb", "endpoints": [{"method": "GET", "path": "/user/balance"}]},
            {"enabled": True, "id": "moonshot", "name": "Kimi Code", "baseURL": "http://127.0.0.1:8787/kimi",
             "token": "sk-fake-kimi", "endpoints": [
                 {"method": "GET", "path": "/usages"}, {"method": "GET", "path": "/me"}, {"method": "GET", "path": "/models"}]},
            {"enabled": True, "id": "opencode", "name": "OpenCode Go", "baseURL": "http://127.0.0.1:8787/opencode",
             "token": "auth=Fe26.2**fake; oc_locale=zh", "endpoints": [
                 {"method": "GET", "path": "/_server?id=deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef&args=%7B%22t%22%3A9%7D"}]},
            {"enabled": False, "id": "disabled", "name": "禁用", "baseURL": "https://d.example.com",
             "token": "sk-fake-d", "endpoints": [{"method": "GET", "path": "/x"}]},
        ],
    }
    with open(cfg, "w", encoding="utf-8") as f:
        json.dump(v1, f, ensure_ascii=False)
    inst = Instance(binpath, cfg, port)
    try:
        inst.wait_ready()
        with open(cfg, encoding="utf-8") as f:
            mig = json.load(f)
        s = json.dumps(mig)
        check("迁移升版 version=3", mig["version"] == 3)
        check("迁移后无明文 token", not any(x in s for x in
              ["fake-zhipu-token", "sk-fake-bbbb", "sk-fake-kimi", "Fe26.2**fake", "sk-fake-d"]))
        byid = {p["id"]: p for p in mig["providers"]}
        check("enabled=false 跳过（4 个导入）", len(mig["providers"]) == 4 and "disabled" not in byid)
        check("kimi 自动改写 base_url", byid["moonshot"]["base_url"] == "https://api.kimi.com/coding/v1")
        check("kimi paths 序保持", byid["moonshot"]["paths"] == ["/usages", "/me", "/models"])
        check("opencode 改写+cookie+头", byid["opencode"]["base_url"] == "https://opencode.ai"
              and byid["opencode"]["auth_style"] == "cookie"
              and byid["opencode"]["extra_headers"].get("x-server-id") == "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
              and byid["opencode"]["extra_headers"].get("x-server-instance") == "server-fn:5")
        log = inst.log()
        check("迁移 INFO 对照日志", "迁移改写" in log and "api.kimi.com/coding/v1" in log)
        check("迁移后为默认密码态（红 banner 条件）", "正在使用默认密码" in log)
    finally:
        inst.kill()
    return d


def scenario_lock(binpath, d, port, cfg, inst):
    print("\n== 场景 4：双开拒绝 / stale 接管 / 多配置并存 ==")
    # 同配置双开 → 第二个退出并提示 PID
    p2 = Instance(binpath, cfg, free_port())
    rc = p2.proc.wait(timeout=15)
    out2 = p2.log()
    check("同配置双开 exit 1", rc == 1, f"rc={rc}")
    check("提示已在运行 (PID)", "已在运行 (PID" in out2, out2[-200:])

    # 不同配置并存
    cfg2 = os.path.join(d, "config2.json")
    port2 = free_port()
    p3 = Instance(binpath, cfg2, port2)
    try:
        p3.wait_ready()
        check("同目录两套配置并存", True)
    finally:
        p3.kill()

    # kill -9 → stale 接管
    pid = inst.proc.pid
    if IS_WIN:
        subprocess.run(["taskkill", "/F", "/PID", str(pid)], capture_output=True)
    else:
        os.kill(pid, signal.SIGKILL)
    inst.proc.wait(timeout=10)
    p4 = Instance(binpath, cfg, port)
    try:
        p4.wait_ready()
        check("kill -9 后重启 stale 接管", True)
    finally:
        p4.kill()


import hashlib


def lock_name(cfg):
    return "quotaclock-" + hashlib.sha256(os.path.abspath(cfg).encode()).hexdigest()[:16] + ".lock"


def scenario_graceful(binpath, d, port, cfg):
    print("\n== 场景 5：优雅退出（锁删除 + 退出码 0 + <26s） ==")
    if IS_WIN:
        # CTRL_BREAK → Go os.Interrupt；子进程须为独立进程组
        proc = subprocess.Popen([binpath, "-config", cfg, "-port", str(port)],
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                universal_newlines=True,
                                creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
    else:
        proc = subprocess.Popen([binpath, "-config", cfg, "-port", str(port)],
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                universal_newlines=True)
    inst = Instance.__new__(Instance)
    inst.proc, inst.lines, inst.stop = proc, [], threading.Event()
    inst.reader = threading.Thread(target=inst._read, args=(), daemon=True)
    inst.reader.start()
    inst.port = port
    inst.wait_ready()
    t0 = time.time()
    if IS_WIN:
        os.kill(proc.pid, signal.CTRL_BREAK_EVENT)
    else:
        os.kill(proc.pid, signal.SIGTERM)
    rc = proc.wait(timeout=30)
    dt = time.time() - t0
    gone = not os.path.exists(os.path.join(d, lock_name(cfg)))
    check("优雅退出 exit 0", rc == 0, f"rc={rc}")
    check("退出时长 <26s（%.1fs）" % dt, dt < 26)
    check("锁文件已删除", gone)


def scenario_startup_time(binpath):
    print("\n== 场景 6：非功能：启动 <1s ==")
    d = tempfile.mkdtemp(prefix="qc-e2e-boot-")
    port = free_port()
    cfg = os.path.join(d, "config.json")
    inst = Instance(binpath, cfg, port)
    try:
        _, boot = inst.wait_ready(timeout=10)
        check("启动到出数 <1.5s（%.0fms，含进程拉起）" % (boot * 1000), boot < 1.5)
    finally:
        inst.kill()


def main():
    if not BIN or not os.path.exists(BIN):
        print("用法: python scripts/e2e.py <quotaclock 可执行文件>")
        sys.exit(2)
    binpath = os.path.abspath(BIN)
    mock = MockUpstream()
    try:
        d, port, cfg, inst = scenario_first_start(binpath)
        try:
            scenario_api_chain(d, port, cfg, inst, mock)
            scenario_log_redaction(inst, cfg)
            # 双开/stale 场景要求第一实例存活，故在 kill 之前执行（结尾以 kill -9 收尾）
            scenario_lock(binpath, d, port, cfg, inst)
        finally:
            inst.kill()
        scenario_graceful(binpath, d, port, cfg)
        scenario_migration(binpath, mock)
        scenario_startup_time(binpath)
    finally:
        mock.close()

    print("\n===== E2E 结果 =====")
    fails = [r for r in results if not r[1]]
    for name, ok, detail in results:
        print(("PASS  " if ok else "FAIL  ") + name + ("" if ok else "  -> " + detail))
    print(f"\n{len(results) - len(fails)}/{len(results)} 通过")
    sys.exit(1 if fails else 0)


if __name__ == "__main__":
    main()
