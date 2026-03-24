# Claude Code WeChat 通知插件

通过 agent hook 在 Claude 任务完成后自动发送微信通知。

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

### 1. 微信扫码登录

```bash
npx claude-code-wechat-channel setup
```

终端会显示二维码，用微信扫描并确认。凭据保存到 `~/.claude/channels/wechat/account.json`。

### 2. 生成 MCP 配置

```bash
npx claude-code-wechat-channel install
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

### 4. 启动 MCP 服务器

```bash
npx claude-code-wechat-channel start
```

让 MCP 服务器在后台运行，持续监听微信消息以获取 `context_token`。

### 5. 完成一次微信消息交互

为了让 MCP 获取 `context_token`，需要先让微信用户发送一条消息给 ClawBot。MCP 会自动缓存这个 token。

## 命令说明

| 命令 | 说明 |
|------|------|
| `npx claude-code-wechat-channel setup` | 微信扫码登录 |
| `npx claude-code-wechat-channel install` | 生成 .mcp.json 配置 |
| `npx claude-code-wechat-channel start` | 启动 MCP 服务器（需在后台运行） |
| `npx claude-code-wechat-channel help` | 显示帮助 |

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
