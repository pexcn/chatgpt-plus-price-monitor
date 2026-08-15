package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
)

// fakeTelegram 记录所有发出的消息。
type fakeTelegram struct {
	srv  *httptest.Server
	mu   sync.Mutex
	sent []string
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.sent = append(f.sent, req.Text)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func offersAt(prices ...float64) []priceai.Offer {
	out := make([]priceai.Offer, 0, len(prices))
	for i, p := range prices {
		out = append(out, priceai.Offer{
			ID:              string(rune('a' + i)),
			SourceStoreName: "店铺" + string(rune('A'+i)),
			SourceTitle:     "GPT Plus 月卡",
			Price:           p,
			Currency:        "CNY",
			URL:             "https://example.com/item",
		})
	}
	return out
}

func stubFetch(offers []priceai.Offer) fetcher {
	return func(ctx context.Context, n int) ([]priceai.Offer, error) {
		return offers, nil
	}
}

func testConfig(t *testing.T, tg *fakeTelegram) *config {
	t.Helper()
	return &config{
		top:           5,
		threshold:     10,
		timeout:       5 * time.Second,
		statePath:     filepath.Join(t.TempDir(), "state.json"),
		cooldown:      24 * time.Hour,
		notifyRecover: true,
		token:         "111:AAA",
		chatID:        "42",
		apiBase:       tg.srv.URL,
	}
}

// 跌破阈值发一次，之后在冷却期内保持静默。
func TestCheckOnceAlertsOnceThenStaysSilent(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig(t, tg)
	fetch := stubFetch(offersAt(8.8, 9.0, 9.5, 10.0, 11.2)) // 均价 9.70

	for i := 0; i < 3; i++ {
		if err := checkOnce(context.Background(), fetch, tg.srv.Client(), cfg); err != nil {
			t.Fatalf("第 %d 次 checkOnce 失败: %v", i+1, err)
		}
	}

	msgs := tg.messages()
	if len(msgs) != 1 {
		t.Fatalf("发了 %d 条通知，期望只发 1 条", len(msgs))
	}
	if !strings.Contains(msgs[0], "降价提醒") {
		t.Errorf("通知内容不对: %s", msgs[0])
	}
	// 均价和明细都要出现在通知里。
	for _, want := range []string{"9.70", "8.80", "店铺A", "11.20"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("通知里缺少 %q:\n%s", want, msgs[0])
		}
	}
}

// 价格回升后应发一条回升通知，并带上上次的均价。
func TestCheckOnceNotifiesOnRecover(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig(t, tg)
	httpc := tg.srv.Client()
	ctx := context.Background()

	if err := checkOnce(ctx, stubFetch(offersAt(8, 8, 8, 8, 8)), httpc, cfg); err != nil {
		t.Fatalf("checkOnce 失败: %v", err)
	}
	if err := checkOnce(ctx, stubFetch(offersAt(20, 20, 20, 20, 20)), httpc, cfg); err != nil {
		t.Fatalf("checkOnce 失败: %v", err)
	}
	// 回升后再跑一次应保持静默。
	if err := checkOnce(ctx, stubFetch(offersAt(20, 20, 20, 20, 20)), httpc, cfg); err != nil {
		t.Fatalf("checkOnce 失败: %v", err)
	}

	msgs := tg.messages()
	if len(msgs) != 2 {
		t.Fatalf("发了 %d 条通知，期望 2 条", len(msgs))
	}
	if !strings.Contains(msgs[1], "价格回升") {
		t.Errorf("第二条应是回升通知: %s", msgs[1])
	}
	if !strings.Contains(msgs[1], "8.00") {
		t.Errorf("回升通知里应带上次均价 8.00:\n%s", msgs[1])
	}
}

// 一直高于阈值时一条都不该发。
func TestCheckOnceSilentWhenAlwaysExpensive(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig(t, tg)
	fetch := stubFetch(offersAt(100.94, 101.97, 105.06, 107.64, 108.15))

	if err := checkOnce(context.Background(), fetch, tg.srv.Client(), cfg); err != nil {
		t.Fatalf("checkOnce 失败: %v", err)
	}
	if n := len(tg.messages()); n != 0 {
		t.Errorf("发了 %d 条通知，期望 0 条", n)
	}
}

// dry-run 不发通知也不写状态文件。
func TestCheckOnceDryRun(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig(t, tg)
	cfg.dryRun = true

	if err := checkOnce(context.Background(), stubFetch(offersAt(1, 1, 1, 1, 1)), tg.srv.Client(), cfg); err != nil {
		t.Fatalf("checkOnce 失败: %v", err)
	}
	if n := len(tg.messages()); n != 0 {
		t.Errorf("dry-run 不应发通知，实际发了 %d 条", n)
	}
	if _, err := os.Stat(cfg.statePath); !os.IsNotExist(err) {
		t.Error("dry-run 不应写状态文件")
	}
}

func TestValidate(t *testing.T) {
	base := func() *config {
		return &config{top: 5, threshold: 10, timeout: time.Second, token: "t", chatID: "c"}
	}
	if err := base().validate(); err != nil {
		t.Errorf("合法配置不应报错: %v", err)
	}

	bad := map[string]func(*config){
		"top 为 0":   func(c *config) { c.top = 0 },
		"阈值为 0":     func(c *config) { c.threshold = 0 },
		"超时为 0":     func(c *config) { c.timeout = 0 },
		"缺 token":   func(c *config) { c.token = "" },
		"缺 chat id": func(c *config) { c.chatID = "" },
	}
	for name, mutate := range bad {
		c := base()
		mutate(c)
		if err := c.validate(); err == nil {
			t.Errorf("%s 时期望报错", name)
		}
	}

	// dry-run 下不需要 Telegram 凭据。
	c := base()
	c.token, c.chatID, c.dryRun = "", "", true
	if err := c.validate(); err != nil {
		t.Errorf("dry-run 不该要求凭据: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	// 按字符截断，不能把中文切成半个。
	if got := truncate("一二三四五", 3); got != "一二三…" {
		t.Errorf("truncate() = %q", got)
	}
	if got := truncate("短", 10); got != "短" {
		t.Errorf("truncate() = %q", got)
	}
}
