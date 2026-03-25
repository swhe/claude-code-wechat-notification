# Claude Code WeChat 通知 MCP

Claude Code 的微信通知 MCP 服务器，通过 agent hook 在任务完成后发送微信通知。

基于微信官方 ClawBot ilink API，无需登录 claude.ai。

## 工作原理

```
Claude Code 任务完成 → Stop Hook → Agent Hook → wechat_reply tool → ilink API → 微信
```

## 前置要求

- [Node.js](https://nodejs.org) >= 18（或 [Bun](https://bun.sh) >= 1.0）
- [Claude Code](https://claude.com/claude-code)
- 微信 iOS 最新版（需支持 ClawBot 插件）

## 快速开始

### 1. 启动 MCP 服务器

```bash
npx claude-code-wechat-notification start
```

首次运行时会自动弹出微信扫码登录。终端会显示二维码，用微信扫描并确认。凭据保存到 `~/.claude/channels/wechat/account.json`。

### 2. 生成 MCP 配置

```bash
npx claude-code-wechat-notification install
```

这会在当前目录生成（或更新） `.mcp.json`，指向本插件。

### 3. 配置 Agent Hook

在 `~/.claude/settings.json` 中添加：

```json
{
  "hooks": {
    "Stop": [{
      "hooks": [{
        "type": "agent",
        "prompt": "请使用 wechat_reply tool 发送消息给微信用户，内容为：任务已完成",
        "timeout": 30
      }]
    }]
  }
}
```

### 4. 完成一次微信消息交互

为了让 MCP 获取 `context_token`，需要先让微信用户发送一条消息给 ClawBot。MCP 会自动缓存这个 token。

## 命令说明

| 命令 | 说明 |
|------|------|
| `npx claude-code-wechat-notification install` | 生成 .mcp.json 配置 |
| `npx claude-code-wechat-notification start` | 启动 MCP 服务器（首次运行自动扫码登录） |
| `npx claude-code-wechat-notification start --mode http` | 以 HTTP API 模式启动 |
| `npx claude-code-wechat-notification help` | 显示帮助 |

## HTTP API 模式

除了 MCP stdio 模式外，还支持 HTTP API 模式，便于其他服务调用。

### 启动 HTTP 服务器

```bash
npx claude-code-wechat-notification start --mode http
```

默认监听 `127.0.0.1:3100`，可通过 `--port` 和 `--host` 自定义：

```bash
npx claude-code-wechat-notification start --mode http --port 8080 --host 0.0.0.0
```

### API 端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 健康检查 |
| `/api/status` | GET | 获取登录状态、最后发送者、context_token 状态 |
| `/api/send` | POST | 发送微信消息 |
| `/mcp` | POST | MCP over HTTP 协议端点 |

### 发送消息示例

```bash
# 健康检查
curl http://127.0.0.1:3100/health

# 查看状态
curl http://127.0.0.1:3100/api/status

# 发送消息（需要先让用户发送消息以获取 context_token）
curl -X POST http://127.0.0.1:3100/api/send \
  -H "Content-Type: application/json" \
  -d '{"text": "Hello from HTTP API"}'

# 指定 sender_id 发送
curl -X POST http://127.0.0.1:3100/api/send \
  -H "Content-Type: application/json" \
  -d '{"sender_id": "user@im.wechat", "text": "Hello"}'
```

### 注意事项

- HTTP API 模式同时提供 MCP over HTTP 端点 (`/mcp`)
- 首次使用仍需扫码登录（仅首次）
- `context_token` 通过长轮询自动获取并缓存

## 技术细节

- **通知发送**: 通过 `ilink/bot/sendmessage` 发送微信消息
- **Context Token**: 通过 `ilink/bot/getupdates` 长轮询获取并缓存到本地
- **认证**: 使用 `ilink/bot/get_bot_qrcode` QR 码登录获取 Bearer Token
- **协议**: MCP (Model Context Protocol)

## 注意事项

- 微信 ClawBot 目前仅支持 iOS 最新版
- `context_token` 需要定期刷新，微信用户发送消息可更新 token
- MCP 服务器需要持续运行以保持 token 有效

## License

MIT
