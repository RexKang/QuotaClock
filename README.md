# QuotaClock

多平台 LLM 配额总览 Dashboard——单二进制 Go 服务端，零依赖、开箱即用。

后台常驻采集器定时拉取各平台用量，浏览器访问 `http://127.0.0.1:8787` 即可打开看板，
无需任何本地配置；一份服务端配置全设备共享。

![Version](https://img.shields.io/badge/Version-0.2.0-blue)
![Platform](https://img.shields.io/badge/Platform-智谱%20%7C%20DeepSeek%20%7C%20Kimi%20%7C%20OpenCode-blue)
![License](https://img.shields.io/badge/License-MIT-green)

## 功能

- **多平台额度总览**：智谱 GLM / DeepSeek / Kimi Code / OpenCode（平台差异全部走配置，可自行增删）
- **后台常驻采集**：默认 300s + 5~25s 抖动定时拉取，平台间 1~5s 错峰；页面只读服务端缓存
- **失败语义区分**：token 失效红字提示并停采；网络失败按指数退避（×2 封顶 30min），429 按 Retry-After 顺延
- **配置加密落盘**：token 以 AES-256-GCM 加密存 `config.json`，密钥为随机生成的 `key.bin`，配置与日志中永不出现明文
- **两档鉴权**：`admin`（查看公开、修改须登录）/ `none`（全开放，本机自用）
- **热生效**：保存配置后采集器立即按新配置重建；仅监听地址/端口需重启

## 快速开始

1. 下载对应平台产物（Release 页）：`quotaclock.exe`（Windows amd64）/ `quotaclock-Linux-x86_64` / `quotaclock-Linux-arm64`
2. 放到任意目录，直接运行：

```bash
./quotaclock              # Linux
quotaclock.exe            # Windows（双击亦可）
```

3. 首次启动自动生成模板 `config.json`，并**自动在默认浏览器中打开看板**（无图形环境会跳过并打印地址）：

```
2026-09-05 19:00:00 [INFO] 已生成模板配置 ...\config.json（默认密码 Quota@2026090S，请尽快修改）
2026-09-05 19:00:00 [WARN] 正在使用默认密码，请立即修改
2026-09-05 19:00:00 [INFO] QuotaClock v0.2.0 已启动 | 监听 http://127.0.0.1:8787 | auth=admin | ...
2026-09-05 19:00:00 [INFO] 已在浏览器打开 http://127.0.0.1:8787
```

4. 浏览器打开 `http://127.0.0.1:8787` → 「设置」→「登录」（默认密码 `Quota@2026090S`）→
   添加平台并填入 token → 保存，采集器即刻开始拉取。

> ⚠️ 首次登录后请立即在设置中修改管理密码。

### Token 获取指南

| 平台 | 获取方式 |
|---|---|
| 智谱 GLM | 登录 open.bigmodel.cn → F12 → Network → 点任意 `/api/biz/...` 请求 → 复制 `Authorization: Bearer ...` 的值 |
| DeepSeek | platform.deepseek.com → API Keys |
| Kimi Code | platform.kimi.com → API Keys（周期额度查询走 `/usages`，官方 CLI 同款接口） |
| OpenCode | opencode.ai 控制台 → API Keys（鉴权方式用缺省 bearer；用量接口 `GET /zen/go/v1/usage`） |

## 从 v0.1 迁移

v0.1 是纯前端单文件页面（配置存 localStorage）。迁移走**旁路文件**（localStorage 跨源不可读，因此没有导入 UI）：

1. 从 Release 页下载 **v0.1.0** 中的 `index.html`（或 `git checkout v0.1.0 -- index.html`），浏览器打开（file://）→ 设置 → 「导出 JSON」下载配置
2. 将导出文件**重命名为 `config.json`**，放到 v0.2 可执行文件同目录
3. 启动 v0.2 → 自动迁移：schema 升至 version 3、token 立即加密落盘、
   原 Python 代理平台自动改写为直连地址（启动日志打印改写对照）
4. 核对日志后按需在设置页修正 `paths`

### 迁移改写对照表（自动执行）

| v0.1 base_url | v0.2 base_url | 附带动作 |
|---|---|---|
| `http://127.0.0.1:8787/kimi` | `https://api.kimi.com/coding/v1` | 截余路径并入 paths[0] |
| `http://127.0.0.1:8787/opencode` | `https://opencode.ai/zen/go/v1` | paths 固定 `[/usage]`（官方用量接口）；v0.1 的 Cookie 登录态无法转换为 API Key，**token 未迁移**——到控制台生成 API Key 后在设置页录入 |
| 直连地址（如 open.bigmodel.cn） | 原样导入 | `baseURL`→`base_url`，endpoints 仅取 GET |
| 指向其他本机代理端口 | **跳过 + WARN** | 不猜不改写；请手动添加真实上游地址 |

v0.1 的 `enabled=false` 平台不导入（v0.2 无 enabled 概念）；endpoint 的 JSON 参数不迁移（如需 query 请并入 path）。

> **端口提示**：v0.1 的 kimi/opencode 依赖 Python 代理 `llm-proxy.py`（127.0.0.1:8787），
> v0.2 服务端直连后**不再需要代理**。v0.2 默认也监听 8787——迁移前请先关闭代理，
> 或用 `-port` 换端口。

## 配置说明（config.json）

```jsonc
{
  "version": 3,
  "listen":   { "host": "127.0.0.1", "port": 8787 },
  "collector": {
    "interval_base_s": 300,    // 采集基准间隔（≥30）
    "jitter_min_s": 5, "jitter_max_s": 25,
    "stagger_min_s": 1, "stagger_max_s": 5,
    "backoff_multiplier": 2, "backoff_max_s": 1800
  },
  "auth": { "mode": "admin" },                 // password_hash 服务端自管，勿手写
  "providers": [{
    "id": "deepseek", "name": "DeepSeek",
    "base_url": "https://api.deepseek.com",
    "paths": ["/user/balance"],
    "auth_style": "bearer",                    // bearer | cookie（缺省 bearer）
    "extra_headers": {},                       // 逐条并入请求头，可覆盖默认头
    "token_cipher": "…"                        // 服务端自管，勿手写
  }]
}
```

- 推荐在设置页修改（保存即原子写盘 + 热生效）；token 留空 = 保留原值
- 手工编辑 `config.json` 亦受支持，但请先停止程序（运行中保存会覆盖手改）
- `config.json` / `key.bin` / `quotaclock-*.lock` 为运行时文件，已列入 .gitignore，切勿外传

## 启动参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `-addr` | `127.0.0.1` | 监听 IP（`0.0.0.0` 时日志与页面双重暴露警示） |
| `-port` | `8787` | 监听端口（改动需重启生效） |
| `-interval` | config 值 | 采集基准间隔（秒），覆盖 config |
| `-config` | exe 同目录/config.json | 配置文件路径（亦可用环境变量 `QUOTACLOCK_CONFIG`） |
| `-admin-password` | 无 | 初始化/重置管理密码（写盘后继续启动） |
| `-version` | - | 打印版本后退出 |

优先级：启动参数 > config.json > 内置默认值。环境变量：

| 环境变量 | 说明 |
|---|---|
| `QUOTACLOCK_CONFIG` | 配置文件路径（等价 `-config`） |
| `QUOTACLOCK_LOG=debug` | 日志提升至 DEBUG |
| `QUOTACLOCK_NO_BROWSER=1` | 禁用启动时自动打开浏览器 |

自动打开浏览器的行为：Windows（交互会话）用默认浏览器打开；Linux 检测到图形会话
（`DISPLAY` / `WAYLAND_DISPLAY`）时经 `xdg-open` 打开，SSH / systemd / 容器等无图形环境
自动跳过、只打印访问地址。

## 部署指南

### 常驻运行

```ini
# Linux systemd 示例
[Service]
ExecStart=/opt/quotaclock/quotaclock -config /opt/quotaclock/config.json
Restart=on-failure
# 日志走 stdout，journald 自动收集；建议绝对路径启动
```

```powershell
# Windows：计划任务 / 快捷方式
D:\quotaclock\quotaclock.exe -config D:\quotaclock\config.json -port 8787
```

- **单实例锁**：同一配置文件同时只允许一个进程（重复启动提示 PID 后退出）；
  崩溃/断电残留的锁会被下次启动自动接管
- **多实例**：同目录放多份配置（如 `config-a.json` / `config-b.json`）即可并行多套账号
- **局域网开放**：`-addr 0.0.0.0`（建议置于反向代理之后；HTTPS 建议由反代终结）

### 恢复路径 ①：key.bin 丢失 / 损毁

`key.bin` 是全部 token 的加密密钥，**丢失后密文不可恢复**（设计使然，无后门）：

1. 停止程序；`key.bin` 已损坏或被删则跳过此步
2. 启动程序——自动生成新 `key.bin`，所有平台显示「token 无法解密，请在设置中重新录入」
3. 到设置页逐平台重新粘贴 token 保存即可；其余配置不受影响

### 恢复路径 ②：忘记管理密码

```bash
quotaclock -admin-password 新密码
# 日志提示「管理员密码已重置」，随后正常启动，用新密码登录
```

## 隐私与安全

- token 明文只存在于内存；落盘为 AES-256-GCM 密文；日志全链路脱敏（明文/密文/掩码/会话 cookie 永不入日志）
- 后端只读硬约束：唯一写路径是配置保存；文件系统白名单仅 `config.json` / `key.bin` / 锁文件 / `*.tmp`
- 会话 cookie：HttpOnly + SameSite=Lax，7 天有效；登出立即失效（重启后旧登出 cookie 至多存活至自然过期，单管理员场景已接受的取舍）
- CORS 红线：服务端不发送任何 CORS 头；`/api/login` 仅接受 JSON（CSRF 三道防线）
- `auth.mode=none` 表示全开放（任何可访问者可查看/修改配置），页面顶部常驻警示

## 常见问题

**平台接口返回失败？**
部分平台接口为逆向所得，路径可能随平台部署变化失效。处置 = 设置页更新该平台的
「请求路径」，不改代码。OpenCode 已切换官方用量接口（`GET /zen/go/v1/usage`，Bearer API Key），
一般无需跟进平台部署变化。

**怎么看原始返回？**
「详情」页可查看每个平台的状态、上次成功采集时间与原始 JSON。

**能加新的平台吗？**
可以。设置 →「+ 添加平台」→ 填 Base URL、鉴权方式、路径、token。只要返回 JSON 含
`balance` / `total_balance` / `percentage` / `used`/`limit` 等字段，看板会自动识别展示。

**换机器怎么搬？**
`-config` 指向任意路径；`key.bin` + `config.json` 一起拷走即可在别的机器解密（同配置请用绝对路径启动）。

## 构建

```bash
go build -ldflags "-s -w -X main.version=v0.2.0" -o quotaclock ./cmd/quotaclock
go test ./...                        # 单元 + 集成（-race 建议）
python scripts/e2e.py ./quotaclock   # E2E 演练（需要 python3）
```

发布由 GoReleaser + GitHub Actions 完成（push tag 触发，windows/amd64 + linux/amd64 + linux/arm64 产物 + sha256）。

## License

MIT，见 [LICENSE](LICENSE)。
