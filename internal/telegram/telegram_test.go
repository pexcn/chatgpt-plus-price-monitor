package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	var gotPath string
	var gotReq sendMessageReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	c := &Client{Token: "123:ABC", ChatID: "42", HTTP: srv.Client(), BaseURL: srv.URL}
	if err := c.Send(context.Background(), "<b>hi</b>"); err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if gotPath != "/bot123:ABC/sendMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if gotReq.ChatID != "42" || gotReq.Text != "<b>hi</b>" || gotReq.ParseMode != "HTML" {
		t.Errorf("请求体 = %+v", gotReq)
	}
}

func TestSendAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	}))
	defer srv.Close()

	c := &Client{Token: "secret-token", ChatID: "42", HTTP: srv.Client(), BaseURL: srv.URL}
	err := c.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("期望报错")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("错误信息应包含 API 描述, 得到: %v", err)
	}
	// token 不能出现在错误信息里，否则会被写进日志。
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("错误信息泄露了 token: %v", err)
	}
}

func TestSendMissingCredentials(t *testing.T) {
	for _, c := range []*Client{
		{ChatID: "42"},
		{Token: "123:ABC"},
	} {
		if err := c.Send(context.Background(), "hi"); err == nil {
			t.Errorf("凭据不全时期望报错: %+v", c)
		}
	}
}

func TestEscape(t *testing.T) {
	if got := Escape(`a & b < c > d`); got != `a &amp; b &lt; c &gt; d` {
		t.Errorf("Escape() = %q", got)
	}
}
