//go:build !exclude

package main

/**
 * Claude Code WeChat Notification Plugin
 *
 * 仅支持 Claude 任务完成后发送微信通知。
 * 不再需要登录 claude.ai，通过 agent hook 触发通知发送。
 *
 * 使用官方微信 ClawBot ilink API。
 */

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── Config ────────────────────────────────────────────────────────────────────

const (
	channelName             = "wechat"
	channelVersion          = "0.2.0"
	defaultBaseURL          = "https://ilinkai.weixin.qq.com"
	botType                 = "3"
	longPollTimeoutMs       = 35000
	maxConsecutiveFailures = 3
	backoffDelayMs          = 30000
	retryDelayMs            = 2000
	httpDefaultPort         = 3100
	httpDefaultHost         = "127.0.0.1"
)

// ── Paths ────────────────────────────────────────────────────────────────────

func getCredentialsDir() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "~"
	}
	return filepath.Join(home, ".claude", "channels", "wechat")
}

func getCredentialsFile() string {
	return filepath.Join(getCredentialsDir(), "account.json")
}

func getContextTokenFile() string {
	return filepath.Join(getCredentialsDir(), "context_token.txt")
}

func getSyncBufFile() string {
	return filepath.Join(getCredentialsDir(), "sync_buf.txt")
}

// ── Logging ───────────────────────────────────────────────────────────────────

func logMsg(msg string) {
	log.Printf("[wechat-notification] %s", msg)
}

func logError(msg string) {
	log.Printf("[wechat-notification] ERROR: %s", msg)
}

// ── Credentials ──────────────────────────────────────────────────────────────

type AccountData struct {
	Token     string `json:"token"`
	BaseURL   string `json:"baseUrl"`
	AccountID string `json:"accountId"`
	UserID    string `json:"userId,omitempty"`
	SavedAt   string `json:"savedAt"`
}

func loadCredentials() *AccountData {
	file := getCredentialsFile()
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var account AccountData
	if err := json.Unmarshal(data, &account); err != nil {
		return nil
	}
	return &account
}

func saveCredentials(account *AccountData) error {
	dir := getCredentialsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file := getCredentialsFile()
	data, err := json.MarshalIndent(account, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		return err
	}
	return nil
}

func saveContextToken(token string) error {
	return os.WriteFile(getContextTokenFile(), []byte(token), 0644)
}

func loadContextToken() string {
	data, err := os.ReadFile(getContextTokenFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ── WeChat ilink API ─────────────────────────────────────────────────────────

func randomWechatUin() string {
	var b [4]byte
	rand.Read(b[:])
	return base64.StdEncoding.EncodeToString(b[:])
}

func buildHeaders(token, body string) map[string]string {
	headers := map[string]string{
		"Content-Type":      "application/json",
		"AuthorizationType": "ilink_bot_token",
		"X-WECHAT-UIN":      randomWechatUin(),
	}
	if body != "" {
		headers["Content-Length"] = fmt.Sprintf("%d", len(body))
	}
	if token = strings.TrimSpace(token); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

func apiFetch(params struct {
	BaseURL   string
	Endpoint  string
	Body      string
	Token     string
	TimeoutMs int
}) (string, error) {
	baseURL := params.BaseURL
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	reqURL, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	reqURL.Path = strings.TrimSuffix(reqURL.Path, "/") + "/" + params.Endpoint

	headers := buildHeaders(params.Token, params.Body)

	req, err := http.NewRequest("POST", reqURL.String(), bytes.NewBufferString(params.Body))
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: time.Duration(params.TimeoutMs) * time.Millisecond,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return string(respBody), nil
}

// ── QR Login ─────────────────────────────────────────────────────────────────

type QRCodeResponse struct {
	QRCode          string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

type QRStatusResponse struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token,omitempty"`
	ILinkBotID  string `json:"ilink_bot_id,omitempty"`
	BaseURL     string `json:"baseurl,omitempty"`
	ILinkUserID string `json:"ilink_user_id,omitempty"`
}

func fetchQRCode(baseURL string) (*QRCodeResponse, error) {
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	reqURL := fmt.Sprintf("%silink/bot/get_bot_qrcode?bot_type=%s", baseURL, url.QueryEscape(botType))

	resp, err := http.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("QR fetch failed: %d", resp.StatusCode)
	}

	var qrResp QRCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&qrResp); err != nil {
		return nil, err
	}
	return &qrResp, nil
}

func pollQRStatus(baseURL, qrcode string) (*QRStatusResponse, error) {
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	reqURL := fmt.Sprintf("%silink/bot/get_qrcode_status?qrcode=%s", baseURL, url.QueryEscape(qrcode))

	client := &http.Client{
		Timeout: 35000 * time.Millisecond,
	}
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("iLink-App-ClientVersion", "1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("QR status failed: %d", resp.StatusCode)
	}

	var statusResp QRStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, err
	}
	return &statusResp, nil
}

func doQRLogin(baseURL string) *AccountData {
	logMsg("正在获取微信登录二维码...")
	qrResp, err := fetchQRCode(baseURL)
	if err != nil {
		logError(fmt.Sprintf("获取二维码失败: %v", err))
		return nil
	}

	logMsg("\n请使用微信扫描以下二维码：\n")
	logMsg(fmt.Sprintf("二维码链接: %s", qrResp.QRCodeImgContent))

	logMsg("等待扫码...")
	deadline := time.Now().Add(480 * time.Second)
	scannedPrinted := false

	for time.Now().Before(deadline) {
		status, err := pollQRStatus(baseURL, qrResp.QRCode)
		if err != nil {
			logError(fmt.Sprintf("轮询状态失败: %v", err))
			time.Sleep(1 * time.Second)
			continue
		}

		switch status.Status {
		case "wait":
			time.Sleep(1 * time.Second)
		case "scaned":
			if !scannedPrinted {
				logMsg("已扫码，请在微信中确认...")
				scannedPrinted = true
			}
			time.Sleep(1 * time.Second)
		case "expired":
			logMsg("二维码已过期，请重新启动。")
			return nil
		case "confirmed":
			if status.BotToken == "" || status.ILinkBotID == "" {
				logError("登录确认但未返回 bot 信息")
				return nil
			}
			account := &AccountData{
				Token:     status.BotToken,
				BaseURL:   status.BaseURL,
				AccountID: status.ILinkBotID,
				UserID:    status.ILinkUserID,
				SavedAt:   time.Now().Format(time.RFC3339),
			}
			if err := saveCredentials(account); err != nil {
				logError(fmt.Sprintf("保存凭据失败: %v", err))
			}
			logMsg("微信连接成功！")
			return account
		default:
			time.Sleep(1 * time.Second)
		}
	}

	logMsg("登录超时")
	return nil
}

// ── WeChat Message Types ─────────────────────────────────────────────────────

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type MessageItem struct {
	Type     int       `json:"type,omitempty"`
	TextItem *TextItem `json:"text_item,omitempty"`
}

type WeixinMessage struct {
	FromUserID   string         `json:"from_user_id,omitempty"`
	ToUserID     string         `json:"to_user_id,omitempty"`
	ClientID     string         `json:"client_id,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	MessageType  int            `json:"message_type,omitempty"`
	MessageState int            `json:"message_state,omitempty"`
	ItemList     []MessageItem  `json:"item_list,omitempty"`
	ContextToken string         `json:"context_token,omitempty"`
	CreateTimeMs int64          `json:"create_time_ms,omitempty"`
}

type GetUpdatesResp struct {
	Ret                  int            `json:"ret,omitempty"`
	Errcode              int            `json:"errcode,omitempty"`
	Errmsg               string         `json:"errmsg,omitempty"`
	Msgs                 []WeixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string         `json:"get_updates_buf,omitempty"`
	LongpollingTimeoutMs int64         `json:"longpolling_timeout_ms,omitempty"`
}

// Message type constants
const (
	msgTypeUser     = 1
	msgItemText     = 1
	msgTypeBot      = 2
	msgStateFinish  = 2
)

func extractTextFromMessage(msg *WeixinMessage) string {
	if msg.ItemList == nil {
		return ""
	}
	for _, item := range msg.ItemList {
		if item.Type == msgItemText && item.TextItem != nil && item.TextItem.Text != "" {
			return item.TextItem.Text
		}
	}
	return ""
}

// ── getUpdates / sendMessage ─────────────────────────────────────────────────

func getUpdates(baseURL, token, getUpdatesBuf string) (*GetUpdatesResp, error) {
	body := fmt.Sprintf(`{"get_updates_buf":"%s","base_info":{"channel_version":"%s"}}`,
		getUpdatesBuf, channelVersion)

	raw, err := apiFetch(struct {
		BaseURL   string
		Endpoint  string
		Body      string
		Token     string
		TimeoutMs int
	}{
		BaseURL:   baseURL,
		Endpoint:  "ilink/bot/getupdates",
		Body:      body,
		Token:     token,
		TimeoutMs: longPollTimeoutMs,
	})
	if err != nil {
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "context") {
			return &GetUpdatesResp{Ret: 0, Msgs: []WeixinMessage{}, GetUpdatesBuf: getUpdatesBuf}, nil
		}
		return nil, err
	}

	var resp GetUpdatesResp
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func generateClientId() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("claude-code-wechat:%d-%x", time.Now().UnixMilli(), b)
}

func sendTextMessage(baseURL, token, to, text, contextToken string) (string, error) {
	clientID := generateClientId()

	msg := map[string]interface{}{
		"msg": map[string]interface{}{
			"from_user_id":  "",
			"to_user_id":    to,
			"client_id":     clientID,
			"message_type":  msgTypeBot,
			"message_state": msgStateFinish,
			"item_list": []map[string]interface{}{
				{
					"type": msgItemText,
					"text_item": map[string]string{
						"text": text,
					},
				},
			},
			"context_token": contextToken,
		},
		"base_info": map[string]string{
			"channel_version": channelVersion,
		},
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}

	_, err = apiFetch(struct {
		BaseURL   string
		Endpoint  string
		Body      string
		Token     string
		TimeoutMs int
	}{
		BaseURL:   baseURL,
		Endpoint:  "ilink/bot/sendmessage",
		Body:      string(body),
		Token:     token,
		TimeoutMs: 15000,
	})
	if err != nil {
		return "", err
	}
	return clientID, nil
}

// ── Global State ─────────────────────────────────────────────────────────────

var (
	activeAccount *AccountData
	lastSenderID  string
)

// ── HTTP API ─────────────────────────────────────────────────────────────────

type httpAPIConfig struct {
	port int
	host string
}

func createHTTPServer(account *AccountData, config httpAPIConfig) *http.Server {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mode": "http"})
	})

	// Send message
	mux.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		var req struct {
			SenderID string `json:"sender_id"`
			Text     string `json:"text"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		targetSenderID := req.SenderID
		if targetSenderID == "" {
			targetSenderID = lastSenderID
		}
		if targetSenderID == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "no sender_id"})
			return
		}

		contextToken := loadContextToken()
		if contextToken == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "no context_token. User may need to send a message first."})
			return
		}

		var sendErr error
		_, sendErr = sendTextMessage(account.BaseURL, account.Token, targetSenderID, req.Text, contextToken)
		if sendErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": sendErr.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "sent", "to": targetSenderID})
	})

	// Status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"logged_in":         activeAccount != nil,
			"last_sender_id":    lastSenderID,
			"has_context_token": loadContextToken() != "",
		})
	})

	addr := fmt.Sprintf("%s:%d", config.host, config.port)
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logMsg(fmt.Sprintf("HTTP API 服务器已启动: http://%s", addr))
	logMsg("  - GET  /health        - 健康检查")
	logMsg("  - GET  /api/status    - 获取状态")
	logMsg("  - POST /api/send       - 发送微信消息")

	return server
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func handleCORS(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
	}
}

// ── Long-poll loop ──────────────────────────────────────────────────────────

func startPolling(account *AccountData) {
	getUpdatesBuf := ""
	consecutiveFailures := 0

	// Load cached sync buf if available
	syncBufFile := getSyncBufFile()
	if data, err := os.ReadFile(syncBufFile); err == nil {
		getUpdatesBuf = string(data)
		logMsg(fmt.Sprintf("恢复上次同步状态 (%d bytes)", len(getUpdatesBuf)))
	}

	logMsg("开始监听微信消息以获取 context_token...")

	for {
		resp, err := getUpdates(account.BaseURL, account.Token, getUpdatesBuf)
		if err != nil {
			consecutiveFailures++
			logError(fmt.Sprintf("getUpdates 失败: %v", err))
			if consecutiveFailures >= maxConsecutiveFailures {
				consecutiveFailures = 0
				logMsg(fmt.Sprintf("连续失败 %d 次，等待 %ds", maxConsecutiveFailures, backoffDelayMs/1000))
				time.Sleep(time.Duration(backoffDelayMs) * time.Millisecond)
			} else {
				time.Sleep(time.Duration(retryDelayMs) * time.Millisecond)
			}
			continue
		}

		// Handle API errors
		isError := (resp.Ret != 0) || (resp.Errcode != 0)
		if isError {
			consecutiveFailures++
			logError(fmt.Sprintf("getUpdates 失败: ret=%d errcode=%d errmsg=%s", resp.Ret, resp.Errcode, resp.Errmsg))
			if consecutiveFailures >= maxConsecutiveFailures {
				consecutiveFailures = 0
				logMsg(fmt.Sprintf("连续失败 %d 次，等待 %ds", maxConsecutiveFailures, backoffDelayMs/1000))
				time.Sleep(time.Duration(backoffDelayMs) * time.Millisecond)
			} else {
				time.Sleep(time.Duration(retryDelayMs) * time.Millisecond)
			}
			continue
		}

		consecutiveFailures = 0

		// Save sync buf
		if resp.GetUpdatesBuf != "" {
			getUpdatesBuf = resp.GetUpdatesBuf
			os.WriteFile(syncBufFile, []byte(getUpdatesBuf), 0644)
		}

		// Process messages to extract and cache context_token
		for i := range resp.Msgs {
			msg := &resp.Msgs[i]
			if msg.MessageType != msgTypeUser {
				continue
			}

			text := extractTextFromMessage(msg)
			if text == "" {
				continue
			}

			senderID := msg.FromUserID
			if senderID == "" {
				senderID = "unknown"
			}

			// Cache context token for later use
			if msg.ContextToken != "" {
				saveContextToken(msg.ContextToken)
				lastSenderID = senderID
				logMsg(fmt.Sprintf("已缓存 context_token for %s", senderID))
			}
		}
	}
}

// ── Main ────────────────────────────────────────────────────────────────────

type serverMode string

const (
	modeStdio serverMode = "stdio"
	modeHTTP  serverMode = "http"
)

func main() {
	mode := modeStdio
	httpPort := httpDefaultPort
	httpHost := httpDefaultHost

	// Parse command line arguments
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--mode", "-m":
			if i+1 < len(args) {
				mode = serverMode(args[i+1])
				i++
			}
		case "--port", "-p":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &httpPort)
				i++
			}
		case "--host":
			if i+1 < len(args) {
				httpHost = args[i+1]
				i++
			}
		case "http", "stdio":
			mode = serverMode(args[i])
		}
	}

	if mode != modeStdio && mode != modeHTTP {
		logError(fmt.Sprintf("Unknown mode: %s. Use 'stdio' or 'http'.", mode))
		os.Exit(1)
	}

	// Check for saved credentials
	account := loadCredentials()

	if account == nil {
		logMsg("未找到已保存的凭据，启动微信扫码登录...")
		account = doQRLogin(defaultBaseURL)
		if account == nil {
			logError("登录失败，退出。")
			os.Exit(1)
		}
	} else {
		logMsg(fmt.Sprintf("使用已保存账号: %s", account.AccountID))
	}

	activeAccount = account

	// Initialize lastSenderId
	if account.UserID != "" {
		lastSenderID = account.UserID
		logMsg(fmt.Sprintf("使用 account.UserID 作为 lastSenderID: %s", lastSenderID))
	}

	// Start long-poll to collect context_token (runs in background)
	go startPolling(account)

	if mode == modeHTTP {
		// Start HTTP server
		server := createHTTPServer(account, httpAPIConfig{port: httpPort, host: httpHost})
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logError(fmt.Sprintf("HTTP server error: %v", err))
		}
	} else {
		// Stdio mode - MCP server using official SDK
		runMCPServer(account)
	}
}

// ── MCP Server ───────────────────────────────────────────────────────────────

// MCP tool input/output types
type SendMessageInput struct {
	Text     string `json:"text" jsonschema:"the message text to send"`
	SenderID string `json:"sender_id,omitempty" jsonschema:"the sender ID to send to (optional, uses last sender if not provided)"`
}

type SendMessageOutput struct {
	Status string `json:"status"`
	To     string `json:"to"`
}

// SendMessage handler for MCP tool
func sendMessageHandler(ctx context.Context, req *mcp.CallToolRequest, input SendMessageInput) (*mcp.CallToolResult, SendMessageOutput, error) {
	targetSenderID := input.SenderID
	if targetSenderID == "" {
		targetSenderID = lastSenderID
	}

	if targetSenderID == "" {
		return nil, SendMessageOutput{Status: "error", To: ""}, fmt.Errorf("no sender_id available")
	}

	contextToken := loadContextToken()
	if contextToken == "" {
		return nil, SendMessageOutput{Status: "error", To: targetSenderID}, fmt.Errorf("no context_token: please send a message from WeChat first")
	}

	_, err := sendTextMessage(activeAccount.BaseURL, activeAccount.Token, targetSenderID, input.Text, contextToken)
	if err != nil {
		return nil, SendMessageOutput{Status: "error", To: targetSenderID}, err
	}

	return nil, SendMessageOutput{Status: "sent", To: targetSenderID}, nil
}

func runMCPServer(account *AccountData) {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    "wechat-notification",
			Version: channelVersion,
		},
		nil,
	)

	// Register send_message tool
	mcp.AddTool(
		mcpServer,
		&mcp.Tool{
			Name:        "send_message",
			Description: "Send a WeChat message notification",
		},
		sendMessageHandler,
	)

	logMsg("MCP (stdio) 服务器已启动")

	if err := mcpServer.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logError(fmt.Sprintf("MCP server error: %v", err))
		os.Exit(1)
	}
}
