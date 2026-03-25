#!/usr/bin/env bun
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  StreamableHTTPServerTransport
} from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import {
  ListToolsRequestSchema,
  CallToolRequestSchema
} from "@modelcontextprotocol/sdk/types.js";
const CHANNEL_NAME = "wechat";
const CHANNEL_VERSION = "0.2.0";
const DEFAULT_BASE_URL = "https://ilinkai.weixin.qq.com";
const BOT_TYPE = "3";
const CREDENTIALS_DIR = path.join(
  process.env.HOME || "~",
  ".claude",
  "channels",
  "wechat"
);
const CREDENTIALS_FILE = path.join(CREDENTIALS_DIR, "account.json");
const CONTEXT_TOKEN_FILE = path.join(CREDENTIALS_DIR, "context_token.txt");
const LONG_POLL_TIMEOUT_MS = 35e3;
const MAX_CONSECUTIVE_FAILURES = 3;
const BACKOFF_DELAY_MS = 3e4;
const RETRY_DELAY_MS = 2e3;
function log(msg) {
  process.stderr.write(`[wechat-notification] ${msg}
`);
}
function logError(msg) {
  process.stderr.write(`[wechat-notification] ERROR: ${msg}
`);
}
function loadCredentials() {
  try {
    if (!fs.existsSync(CREDENTIALS_FILE)) return null;
    return JSON.parse(fs.readFileSync(CREDENTIALS_FILE, "utf-8"));
  } catch {
    return null;
  }
}
function saveCredentials(data) {
  fs.mkdirSync(CREDENTIALS_DIR, { recursive: true });
  fs.writeFileSync(CREDENTIALS_FILE, JSON.stringify(data, null, 2), "utf-8");
  try {
    fs.chmodSync(CREDENTIALS_FILE, 384);
  } catch {
  }
}
function saveContextToken(token) {
  fs.writeFileSync(CONTEXT_TOKEN_FILE, token, "utf-8");
}
function loadContextToken() {
  try {
    if (fs.existsSync(CONTEXT_TOKEN_FILE)) {
      return fs.readFileSync(CONTEXT_TOKEN_FILE, "utf-8").trim();
    }
  } catch {
  }
  return null;
}
function randomWechatUin() {
  const uint32 = crypto.randomBytes(4).readUInt32BE(0);
  return Buffer.from(String(uint32), "utf-8").toString("base64");
}
function buildHeaders(token, body) {
  const headers = {
    "Content-Type": "application/json",
    AuthorizationType: "ilink_bot_token",
    "X-WECHAT-UIN": randomWechatUin()
  };
  if (body) {
    headers["Content-Length"] = String(Buffer.byteLength(body, "utf-8"));
  }
  if (token?.trim()) {
    headers.Authorization = `Bearer ${token.trim()}`;
  }
  return headers;
}
async function apiFetch(params) {
  const base = params.baseUrl.endsWith("/") ? params.baseUrl : `${params.baseUrl}/`;
  const url = new URL(params.endpoint, base).toString();
  const headers = buildHeaders(params.token, params.body);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), params.timeoutMs);
  try {
    const res = await fetch(url, {
      method: "POST",
      headers,
      body: params.body,
      signal: controller.signal
    });
    clearTimeout(timer);
    const text = await res.text();
    if (!res.ok) throw new Error(`HTTP ${res.status}: ${text}`);
    return text;
  } catch (err) {
    clearTimeout(timer);
    throw err;
  }
}
async function fetchQRCode(baseUrl) {
  const base = baseUrl.endsWith("/") ? baseUrl : `${baseUrl}/`;
  const url = new URL(
    `ilink/bot/get_bot_qrcode?bot_type=${encodeURIComponent(BOT_TYPE)}`,
    base
  );
  const res = await fetch(url.toString());
  if (!res.ok) throw new Error(`QR fetch failed: ${res.status}`);
  return await res.json();
}
async function pollQRStatus(baseUrl, qrcode) {
  const base = baseUrl.endsWith("/") ? baseUrl : `${baseUrl}/`;
  const url = new URL(
    `ilink/bot/get_qrcode_status?qrcode=${encodeURIComponent(qrcode)}`,
    base
  );
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 35e3);
  try {
    const res = await fetch(url.toString(), {
      headers: { "iLink-App-ClientVersion": "1" },
      signal: controller.signal
    });
    clearTimeout(timer);
    if (!res.ok) throw new Error(`QR status failed: ${res.status}`);
    return await res.json();
  } catch (err) {
    clearTimeout(timer);
    if (err instanceof Error && err.name === "AbortError") {
      return { status: "wait" };
    }
    throw err;
  }
}
async function doQRLogin(baseUrl) {
  log("\u6B63\u5728\u83B7\u53D6\u5FAE\u4FE1\u767B\u5F55\u4E8C\u7EF4\u7801...");
  const qrResp = await fetchQRCode(baseUrl);
  log("\n\u8BF7\u4F7F\u7528\u5FAE\u4FE1\u626B\u63CF\u4EE5\u4E0B\u4E8C\u7EF4\u7801\uFF1A\n");
  try {
    const qrterm = await import("qrcode-terminal");
    await new Promise((resolve) => {
      qrterm.default.generate(
        qrResp.qrcode_img_content,
        { small: true },
        (qr) => {
          process.stderr.write(qr + "\n");
          resolve();
        }
      );
    });
  } catch {
    log(`\u4E8C\u7EF4\u7801\u94FE\u63A5: ${qrResp.qrcode_img_content}`);
  }
  log("\u7B49\u5F85\u626B\u7801...");
  const deadline = Date.now() + 48e4;
  let scannedPrinted = false;
  while (Date.now() < deadline) {
    const status = await pollQRStatus(baseUrl, qrResp.qrcode);
    switch (status.status) {
      case "wait":
        break;
      case "scaned":
        if (!scannedPrinted) {
          log("\u{1F440} \u5DF2\u626B\u7801\uFF0C\u8BF7\u5728\u5FAE\u4FE1\u4E2D\u786E\u8BA4...");
          scannedPrinted = true;
        }
        break;
      case "expired":
        log("\u4E8C\u7EF4\u7801\u5DF2\u8FC7\u671F\uFF0C\u8BF7\u91CD\u65B0\u542F\u52A8\u3002");
        return null;
      case "confirmed": {
        if (!status.ilink_bot_id || !status.bot_token) {
          logError("\u767B\u5F55\u786E\u8BA4\u4F46\u672A\u8FD4\u56DE bot \u4FE1\u606F");
          return null;
        }
        const account = {
          token: status.bot_token,
          baseUrl: status.baseurl || baseUrl,
          accountId: status.ilink_bot_id,
          userId: status.ilink_user_id,
          savedAt: (/* @__PURE__ */ new Date()).toISOString()
        };
        saveCredentials(account);
        log("\u2705 \u5FAE\u4FE1\u8FDE\u63A5\u6210\u529F\uFF01");
        return account;
      }
    }
    await new Promise((r) => setTimeout(r, 1e3));
  }
  log("\u767B\u5F55\u8D85\u65F6");
  return null;
}
const MSG_TYPE_USER = 1;
const MSG_ITEM_TEXT = 1;
const MSG_TYPE_BOT = 2;
const MSG_STATE_FINISH = 2;
function extractTextFromMessage(msg) {
  if (!msg.item_list?.length) return "";
  for (const item of msg.item_list) {
    if (item.type === MSG_ITEM_TEXT && item.text_item?.text) {
      return item.text_item.text;
    }
  }
  return "";
}
async function getUpdates(baseUrl, token, getUpdatesBuf) {
  try {
    const raw = await apiFetch({
      baseUrl,
      endpoint: "ilink/bot/getupdates",
      body: JSON.stringify({
        get_updates_buf: getUpdatesBuf,
        base_info: { channel_version: CHANNEL_VERSION }
      }),
      token,
      timeoutMs: LONG_POLL_TIMEOUT_MS
    });
    return JSON.parse(raw);
  } catch (err) {
    if (err instanceof Error && err.name === "AbortError") {
      return { ret: 0, msgs: [], get_updates_buf: getUpdatesBuf };
    }
    throw err;
  }
}
function generateClientId() {
  return `claude-code-wechat:${Date.now()}-${crypto.randomBytes(4).toString("hex")}`;
}
async function sendTextMessage(baseUrl, token, to, text, contextToken) {
  const clientId = generateClientId();
  await apiFetch({
    baseUrl,
    endpoint: "ilink/bot/sendmessage",
    body: JSON.stringify({
      msg: {
        from_user_id: "",
        to_user_id: to,
        client_id: clientId,
        message_type: MSG_TYPE_BOT,
        message_state: MSG_STATE_FINISH,
        item_list: [{ type: MSG_ITEM_TEXT, text_item: { text } }],
        context_token: contextToken
      },
      base_info: { channel_version: CHANNEL_VERSION }
    }),
    token,
    timeoutMs: 15e3
  });
  return clientId;
}
const mcp = new Server(
  { name: CHANNEL_NAME, version: CHANNEL_VERSION },
  {
    capabilities: {
      tools: {}
    },
    instructions: "\u5FAE\u4FE1\u901A\u77E5\u63D2\u4EF6\uFF0C\u652F\u6301\u53D1\u9001\u901A\u77E5\u5230\u5FAE\u4FE1\u3002\u4E0D\u63A5\u6536\u6765\u81EA\u5FAE\u4FE1\u7684\u6D88\u606F\u3002"
  }
);
mcp.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: "wechat_reply",
      description: "Send a text reply back to the WeChat user (sender_id defaults to last known user)",
      inputSchema: {
        type: "object",
        properties: {
          sender_id: {
            type: "string",
            description: "The sender_id from the inbound <channel> tag (xxx@im.wechat format). Optional - defaults to the last message sender."
          },
          text: {
            type: "string",
            description: "The plain-text message to send (no markdown)"
          }
        },
        required: ["text"]
      }
    }
  ]
}));
let activeAccount = null;
let lastSenderId = null;
function initializeLastSenderId(account) {
  lastSenderId = account.userId ?? null;
  if (lastSenderId) {
    log(`\u4F7F\u7528 account.userId \u4F5C\u4E3A lastSenderId: ${lastSenderId}`);
  }
}
mcp.setRequestHandler(CallToolRequestSchema, async (req) => {
  if (req.params.name === "wechat_reply") {
    const args = req.params.arguments;
    const sender_id = args.sender_id ?? lastSenderId;
    const text = args.text;
    if (!sender_id) {
      return {
        content: [
          {
            type: "text",
            text: `error: no sender_id. args sender_id: ${args.sender_id}, lastSenderId: ${lastSenderId}.`
          }
        ]
      };
    }
    if (!activeAccount) {
      return {
        content: [{ type: "text", text: "error: not logged in" }]
      };
    }
    let contextToken = loadContextToken();
    if (!contextToken) {
      return {
        content: [
          {
            type: "text",
            text: `error: no context_token for ${sender_id}. The user may need to send a message first.`
          }
        ]
      };
    }
    try {
      await sendTextMessage(
        activeAccount.baseUrl,
        activeAccount.token,
        sender_id,
        text,
        contextToken
      );
      return { content: [{ type: "text", text: "sent" }] };
    } catch (err) {
      return {
        content: [
          { type: "text", text: `send failed: ${String(err)}` }
        ]
      };
    }
  }
  throw new Error(`unknown tool: ${req.params.name}`);
});
async function startPolling(account) {
  const { baseUrl, token } = account;
  let getUpdatesBuf = "";
  let consecutiveFailures = 0;
  const syncBufFile = path.join(CREDENTIALS_DIR, "sync_buf.txt");
  try {
    if (fs.existsSync(syncBufFile)) {
      getUpdatesBuf = fs.readFileSync(syncBufFile, "utf-8");
      log(`\u6062\u590D\u4E0A\u6B21\u540C\u6B65\u72B6\u6001 (${getUpdatesBuf.length} bytes)`);
    }
  } catch {
  }
  log("\u5F00\u59CB\u76D1\u542C\u5FAE\u4FE1\u6D88\u606F\u4EE5\u83B7\u53D6 context_token...");
  while (true) {
    try {
      const resp = await getUpdates(baseUrl, token, getUpdatesBuf);
      const isError = resp.ret !== void 0 && resp.ret !== 0 || resp.errcode !== void 0 && resp.errcode !== 0;
      if (isError) {
        consecutiveFailures++;
        logError(
          `getUpdates \u5931\u8D25: ret=${resp.ret} errcode=${resp.errcode} errmsg=${resp.errmsg ?? ""}`
        );
        if (consecutiveFailures >= MAX_CONSECUTIVE_FAILURES) {
          logError(
            `\u8FDE\u7EED\u5931\u8D25 ${MAX_CONSECUTIVE_FAILURES} \u6B21\uFF0C\u7B49\u5F85 ${BACKOFF_DELAY_MS / 1e3}s`
          );
          consecutiveFailures = 0;
          await new Promise((r) => setTimeout(r, BACKOFF_DELAY_MS));
        } else {
          await new Promise((r) => setTimeout(r, RETRY_DELAY_MS));
        }
        continue;
      }
      consecutiveFailures = 0;
      if (resp.get_updates_buf) {
        getUpdatesBuf = resp.get_updates_buf;
        try {
          fs.writeFileSync(syncBufFile, getUpdatesBuf, "utf-8");
        } catch {
        }
      }
      for (const msg of resp.msgs ?? []) {
        if (msg.message_type !== MSG_TYPE_USER) continue;
        const text = extractTextFromMessage(msg);
        if (!text) continue;
        const senderId = msg.from_user_id ?? "unknown";
        if (msg.context_token) {
          saveContextToken(msg.context_token);
          lastSenderId = senderId;
          log(`\u5DF2\u7F13\u5B58 context_token for ${senderId}`);
        }
      }
    } catch (err) {
      consecutiveFailures++;
      logError(`\u8F6E\u8BE2\u5F02\u5E38: ${String(err)}`);
      if (consecutiveFailures >= MAX_CONSECUTIVE_FAILURES) {
        consecutiveFailures = 0;
        await new Promise((r) => setTimeout(r, BACKOFF_DELAY_MS));
      } else {
        await new Promise((r) => setTimeout(r, RETRY_DELAY_MS));
      }
    }
  }
}
const HTTP_DEFAULT_PORT = 3100;
const HTTP_DEFAULT_HOST = "127.0.0.1";
async function createHttpServer(account, config) {
  const { baseUrl, token } = account;
  const { port, host } = config;
  const transport = new StreamableHTTPServerTransport({
    sessionIdGenerator: void 0
    // stateless mode
  });
  mcp.connect(transport);
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url ?? "/", `http://${host}:${port}`);
    const setCorsHeaders = () => {
      res.setHeader("Access-Control-Allow-Origin", "*");
      res.setHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
      res.setHeader("Access-Control-Allow-Headers", "Content-Type");
    };
    if (req.method === "OPTIONS") {
      setCorsHeaders();
      res.writeHead(204);
      res.end();
      return;
    }
    if (url.pathname === "/health" && req.method === "GET") {
      setCorsHeaders();
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ status: "ok", mode: "http" }));
      return;
    }
    if (url.pathname === "/api/send" && req.method === "POST") {
      setCorsHeaders();
      try {
        const body = await new Promise((resolve, reject) => {
          let data = "";
          req.on("data", (chunk) => data += chunk);
          req.on("end", () => resolve(data));
          req.on("error", reject);
        });
        const parsed = JSON.parse(body);
        const { sender_id, text } = parsed;
        const targetSenderId = sender_id ?? lastSenderId;
        if (!targetSenderId) {
          res.writeHead(400, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ error: "no sender_id" }));
          return;
        }
        const contextToken = loadContextToken();
        if (!contextToken) {
          res.writeHead(400, { "Content-Type": "application/json" });
          res.end(JSON.stringify({
            error: "no context_token. User may need to send a message first."
          }));
          return;
        }
        await sendTextMessage(baseUrl, token, targetSenderId, text, contextToken);
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ status: "sent", to: targetSenderId }));
      } catch (err) {
        res.writeHead(500, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: String(err) }));
      }
      return;
    }
    if (url.pathname === "/api/status" && req.method === "GET") {
      setCorsHeaders();
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({
        logged_in: !!activeAccount,
        last_sender_id: lastSenderId,
        has_context_token: !!loadContextToken()
      }));
      return;
    }
    if (url.pathname === "/mcp" || url.pathname.startsWith("/mcp/")) {
      try {
        await transport.handleRequest(req, res, req.url);
      } catch (err) {
        logError(`MCP handler error: ${String(err)}`);
        if (!res.headersSent) {
          res.writeHead(500);
          res.end();
        }
      }
      return;
    }
    setCorsHeaders();
    res.writeHead(404, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ error: "not found" }));
  });
  return new Promise((resolve) => {
    server.listen(port, host, () => {
      log(`HTTP API \u670D\u52A1\u5668\u5DF2\u542F\u52A8: http://${host}:${port}`);
      log(`  - GET  /health        - \u5065\u5EB7\u68C0\u67E5`);
      log(`  - GET  /api/status    - \u83B7\u53D6\u72B6\u6001`);
      log(`  - POST /api/send       - \u53D1\u9001\u5FAE\u4FE1\u6D88\u606F`);
      log(`  - POST /mcp           - MCP over HTTP`);
      resolve(server);
    });
  });
}
async function main() {
  const args = process.argv.slice(2);
  let mode = "stdio";
  let httpPort = HTTP_DEFAULT_PORT;
  let httpHost = HTTP_DEFAULT_HOST;
  for (let i = 0; i < args.length; i++) {
    switch (args[i]) {
      case "--mode":
      case "-m":
        mode = args[++i];
        break;
      case "--port":
      case "-p":
        httpPort = parseInt(args[++i], 10);
        break;
      case "--host":
      case "-h":
        httpHost = args[++i];
        break;
      case "http":
      case "stdio":
        mode = args[i];
        break;
    }
  }
  if (mode !== "stdio" && mode !== "http") {
    logError(`Unknown mode: ${mode}. Use 'stdio' or 'http'.`);
    process.exit(1);
  }
  let account = loadCredentials();
  if (!account) {
    log("\u672A\u627E\u5230\u5DF2\u4FDD\u5B58\u7684\u51ED\u636E\uFF0C\u542F\u52A8\u5FAE\u4FE1\u626B\u7801\u767B\u5F55...");
    account = await doQRLogin(DEFAULT_BASE_URL);
    if (!account) {
      logError("\u767B\u5F55\u5931\u8D25\uFF0C\u9000\u51FA\u3002");
      process.exit(1);
    }
  } else {
    log(`\u4F7F\u7528\u5DF2\u4FDD\u5B58\u8D26\u53F7: ${account.accountId}`);
  }
  activeAccount = account;
  initializeLastSenderId(account);
  startPolling(account).catch((err) => {
    logError(`\u8F6E\u8BE2\u5F02\u5E38: ${String(err)}`);
  });
  if (mode === "http") {
    await createHttpServer(account, { port: httpPort, host: httpHost });
  } else {
    await mcp.connect(new StdioServerTransport());
    log("MCP (stdio) \u8FDE\u63A5\u5C31\u7EEA");
  }
}
main().catch((err) => {
  logError(`Fatal: ${String(err)}`);
  process.exit(1);
});
