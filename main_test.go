package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
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

func failingFetch(msg string) fetcher {
	return func(ctx context.Context, n int) ([]priceai.Offer, error) {
		return nil, errors.New(msg)
	}
}

// 连续失败达到阈值才告警，且一轮故障只告一次。
func TestCheckOnceAlertsAfterConsecutiveFailures(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig(t, tg)
	cfg.failThreshold = 3
	fetch := failingFetch("接口只返回了 0 条报价")
	ctx := context.Background()

	// 前两次失败不该发通知，但都要把错误抛出去（单次模式靠它拿非 0 退出码）。
	for i := 1; i <= 2; i++ {
		if err := checkOnce(ctx, fetch, tg.srv.Client(), cfg); err == nil {
			t.Fatalf("第 %d 次应返回错误", i)
		}
		if n := len(tg.messages()); n != 0 {
			t.Fatalf("第 %d 次就发了 %d 条通知，期望 0 条", i, n)
		}
	}

	// 第三次达到阈值，发一条。
	if err := checkOnce(ctx, fetch, tg.srv.Client(), cfg); err == nil {
		t.Fatal("第 3 次应返回错误")
	}
	msgs := tg.messages()
	if len(msgs) != 1 {
		t.Fatalf("发了 %d 条通知，期望 1 条", len(msgs))
	}
	if !strings.Contains(msgs[0], "监控异常") || !strings.Contains(msgs[0], "连续 3 次") {
		t.Errorf("告警内容不对: %s", msgs[0])
	}
	// 原始错误要带进通知，否则不知道是什么坏了。
	if !strings.Contains(msgs[0], "接口只返回了 0 条报价") {
		t.Errorf("告警里应包含原始错误: %s", msgs[0])
	}

	// 继续失败不再重复告警。
	for i := 0; i < 3; i++ {
		_ = checkOnce(ctx, fetch, tg.srv.Client(), cfg)
	}
	if n := len(tg.messages()); n != 1 {
		t.Errorf("持续故障期间发了 %d 条通知，期望仍是 1 条", n)
	}
}

// 抓取恢复后要补一条恢复通知，并把失败计数清零。
func TestCheckOnceNotifiesOnFetchRecovery(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig(t, tg)
	cfg.failThreshold = 2
	httpc := tg.srv.Client()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_ = checkOnce(ctx, failingFetch("boom"), httpc, cfg)
	}
	if n := len(tg.messages()); n != 1 {
		t.Fatalf("故障期发了 %d 条，期望 1 条", n)
	}

	// 恢复，且价格很贵（不会触发降价通知），只应有恢复通知。
	if err := checkOnce(ctx, stubFetch(offersAt(100, 100, 100, 100, 100)), httpc, cfg); err != nil {
		t.Fatalf("checkOnce 失败: %v", err)
	}
	msgs := tg.messages()
	if len(msgs) != 2 {
		t.Fatalf("发了 %d 条，期望 2 条", len(msgs))
	}
	if !strings.Contains(msgs[1], "已恢复") {
		t.Errorf("第二条应是恢复通知: %s", msgs[1])
	}

	st, err := state.Load(cfg.statePath)
	if err != nil {
		t.Fatalf("读状态失败: %v", err)
	}
	if st.Failures != 0 || st.FailNotified {
		t.Errorf("恢复后失败计数应清零, 得到 %+v", st)
	}

	// 再失败一次不该立刻告警（计数已清零，阈值是 2）。
	_ = checkOnce(ctx, failingFetch("boom"), httpc, cfg)
	if n := len(tg.messages()); n != 2 {
		t.Errorf("恢复后第一次失败就告警了，期望仍是 2 条，得到 %d", n)
	}
}

// fail-threshold 为 0 时关闭该功能。
func TestCheckOnceFailAlertDisabled(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig(t, tg)
	cfg.failThreshold = 0

	for i := 0; i < 5; i++ {
		_ = checkOnce(context.Background(), failingFetch("boom"), tg.srv.Client(), cfg)
	}
	if n := len(tg.messages()); n != 0 {
		t.Errorf("关闭时发了 %d 条通知，期望 0 条", n)
	}
}

// 告警发送本身失败时不能标记成已告警，否则这轮故障就再也不会提醒了。
func TestCheckOnceRetriesAlertWhenSendFails(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`)
			return
		}
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	cfg := &config{
		top: 5, threshold: 10, timeout: 5 * time.Second,
		statePath: filepath.Join(t.TempDir(), "state.json"),
		cooldown:  24 * time.Hour, notifyRecover: true, failThreshold: 1,
		token: "111:AAA", chatID: "42", apiBase: srv.URL,
	}
	ctx := context.Background()

	// 第一次：达到阈值但通知发不出去。
	_ = checkOnce(ctx, failingFetch("boom"), srv.Client(), cfg)
	st, _ := state.Load(cfg.statePath)
	if st.FailNotified {
		t.Fatal("通知发送失败时不应标记为已告警")
	}

	// 第二次：Telegram 恢复了，应当补发。
	fail.Store(false)
	_ = checkOnce(ctx, failingFetch("boom"), srv.Client(), cfg)
	if sent.Load() != 1 {
		t.Errorf("补发了 %d 条，期望 1 条", sent.Load())
	}
	st, _ = state.Load(cfg.statePath)
	if !st.FailNotified {
		t.Error("补发成功后应标记为已告警")
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
