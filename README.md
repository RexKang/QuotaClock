# LLM 用量 Dashboard

单页多平台 LLM 用量总览，纯前端 + 可选本地 CORS 代理。

![Dashboard Preview](https://img.shields.io/badge/Platform-智谱%20%7C%20DeepSeek%20%7C%20Kimi%20%7C%20OpenCode-blue)
![License](https://img.shields.io/badge/License-MIT-green)
![Zero Dependencies](https://img.shields.io/badge/Dependencies-Zero-brightgreen)

## 功能

- **总览页**：每平台一行卡片，显示周期额度使用百分比 + 双进度条（额度使用 + 时间进度）+ 重置倒计时
- **详情页**：展开各接口原始返回，含 KPI 概览
- **设置**：增删平台、配置接口列表、测试连接、导入/导出 JSON
- **持久化**：配置存 `localStorage`，刷新不丢
- **零依赖**：纯原生 HTML + CSS + JS，无 npm / 无打包 / 无框架

## 支持平台

| 平台 | 接口 | 鉴权 | 代理 |
|---|---|---|---|
| 智谱 GLM | `/api/monitor/usage/quota/limit`（逆向） | 控制台 Bearer Token | 否 |
| DeepSeek | `/user/balance`（公开） | API Key | 否 |
| Kimi Code | `/usages` + `/me` + `/models` | API Key | 是 |
| OpenCode Go | `/_server` function | 登录 Cookie | 是 |

## 快速开始

### 1. 直接打开（智谱 + DeepSeek）

```bash
# 双击打开，或命令行：
start index.html
```

浏览器以 `file://` 协议加载，智谱和 DeepSeek 支持跨域，功能正常。

### 2. 启用 Kimi / OpenCode（需代理）

```bash
python llm-proxy.py
```

```
LLM proxy listening on http://127.0.0.1:8787
  /kimi/*      ->  https://api.kimi.com/coding/v1/*
  /opencode/*  ->  https://opencode.ai/*
```

代理启动后，打开 `index.html` → 设置 → 对应平台 baseURL 填写：

- Kimi: `http://127.0.0.1:8787/kimi`
- OpenCode: `http://127.0.0.1:8787/opencode`

### 3. 首次配置

1. 打开 Dashboard → 点「设置」
2. 选择平台
3. 填入 Token（从对应平台 F12 → Network → 复制）
4. 点「测试连接」确认接口通
5. 保存 → 回到总览页 → 点「刷新」

## Token 获取指南

| 平台 | 获取方式 |
|---|---|
| 智谱 GLM | 登录 open.bigmodel.cn → F12 → Network → 点任意 `/api/biz/...` 请求 → 复制 `Authorization: Bearer ...` 的值 |
| DeepSeek | 登录 platform.deepseek.com → API Keys → 创建/复制 Key |
| Kimi Code | 登录 platform.kimi.com → API Keys → 创建/复制 Key |
| OpenCode Go | 登录 opencode.ai → F12 → Network → 复制 `/_server` 请求头里的 Cookie 值 |

## 技术细节

### 双进度条

- **左侧**：额度使用百分比（已用 / 总额）
- **右侧**：时间进度百分比（当前时间在周期窗口中的位置）
  - 5 小时：滚动 5h 窗口
  - 周：滚动 7 天窗口
  - 月：自然月窗口（重置点往前推一个月）

### CORS 代理

部分平台 API 不支持浏览器跨域请求。本机代理（`llm-proxy.py`）在 `127.0.0.1:8787` 转发并注入 `Access-Control-Allow-Origin: *` 头。鉴权信息通过 `X-Cookie` 自定义头传输（浏览器会静默丢弃跨域 `Cookie` 头）。

## 常见问题

**Q: 刷新后配置丢了吗？**
不会。配置存在 localStorage，只要不清浏览器数据就一直保留。也可以「导出 JSON」做备份。

**Q: Kimi 的 server function id 会变吗？**
可能。opencode.ai 重新部署后 id 会变化，需要从 F12 Network 重新复制最新的 `/_server` 路径。

**Q: 能加新的 LLM 平台吗？**
可以。点「设置」→「添加平台」→ 填写 Base URL、Token、接口列表。只要返回的 JSON 里有 `balance`、`total_balance`、`percentage`、`used`/`limit` 等字段，Dashboard 会自动识别并展示。

## License

MIT
