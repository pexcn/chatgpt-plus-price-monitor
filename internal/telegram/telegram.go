// Package telegram 封装 Bot API 的 sendMessage 调用。
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const apiBase = "https://api.telegram.org"

// Client 是一个最小化的 Telegram Bot 客户端。
type Client struct {
	Token  string
	ChatID string
	HTTP   *http.Client
	// BaseURL 可覆盖 API 地址，测试用。
	BaseURL string
}

type sendMessageReq struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type apiResp struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// Send 以 HTML 格式发送一条消息。
func (c *Client) Send(ctx context.Context, text string) error {
	if c.Token == "" || c.ChatID == "" {
		return fmt.Errorf("缺少 telegram token 或 chat id")
	}
	base := c.BaseURL
	if base == "" {
		base = apiBase
	}
	payload, err := json.Marshal(sendMessageReq{
		ChatID:                c.ChatID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(base, "/"), c.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("调用 telegram api 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var ar apiResp
	if err := json.Unmarshal(body, &ar); err != nil {
		return fmt.Errorf("telegram 返回了无法解析的响应 (HTTP %d)", resp.StatusCode)
	}
	if !ar.OK {
		// 注意不要把 token 带进错误信息里。
		return fmt.Errorf("telegram 拒绝了请求: %s (error_code=%d)", ar.Description, ar.ErrorCode)
	}
	return nil
}

// Escape 转义 HTML parse_mode 下的保留字符。
func Escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
