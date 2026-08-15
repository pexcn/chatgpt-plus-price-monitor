package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/telegram"
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

func (f *fakeTelegram) client() notifier {
	return &telegram.Client{Token: "111:AAA", ChatID: "42", HTTP: f.srv.Client(), BaseURL: f.srv.URL}
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
	return func(ctx context.Context, n int) ([]priceai.Offer, error) { return offers, nil }
}

func failingFetch(msg string) fetcher {
	return func(ctx context.Context, n int) ([]priceai.Offer, error) { return nil, errors.New(msg) }
}

func testConfig() *config {
	return &config{
		threshold:     10,
		interval:      30 * time.Minute,
		sample:        30,
		floorRatio:    0.5,
		top:           5,
		cooldown:      24 * time.Hour,
		failThreshold: 3,
		timeout:       5 * time.Second,
	}
}

// 跌破阈值发一次，之后在冷却期内保持静默。
func TestCheckAlertsOnceThenStaysSilent(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig()
	fetch := stubFetch(offersAt(8.8, 9.0, 9.5, 10.0, 11.2)) // 中位数 9.50，最低可信 8.80

	var st state.State
	for i := 0; i < 3; i++ {
		var err error
		if st, err = check(context.Background(), fetch, tg.client(), cfg, st); err != nil {
			t.Fatalf("第 %d 次 check 失败: %v", i+1, err)
		}
	}

	msgs := tg.messages()
	if len(msgs) != 1 {
		t.Fatalf("发了 %d 条通知，期望只发 1 条", len(msgs))
	}
	if !strings.Contains(msgs[0], "降价提醒") {
		t.Errorf("通知内容不对: %s", msgs[0])
	}
	for _, want := range []string{"8.80", "9.50", "店铺A"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("通知里缺少 %q:\n%s", want, msgs[0])
		}
	}
}

// 价格回升后应发一条回升通知，并带上上次的价格。
func TestCheckNotifiesOnRebound(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig()
	n := tg.client()
	ctx := context.Background()

	st, _ := check(ctx, stubFetch(offersAt(8, 8, 8, 8, 8)), n, cfg, state.State{})
	st, _ = check(ctx, stubFetch(offersAt(20, 20, 20, 20, 20)), n, cfg, st)
	// 回升后再跑一次应保持静默。
	_, _ = check(ctx, stubFetch(offersAt(20, 20, 20, 20, 20)), n, cfg, st)

	msgs := tg.messages()
	if len(msgs) != 2 {
		t.Fatalf("发了 %d 条通知，期望 2 条", len(msgs))
	}
	if !strings.Contains(msgs[1], "价格回升") {
		t.Errorf("第二条应是回升通知: %s", msgs[1])
	}
	if !strings.Contains(msgs[1], "8.00") {
		t.Errorf("回升通知里应带上次报价 8.00:\n%s", msgs[1])
	}
}

// 端到端验证异常规格的剔除：均价规则会把这两种情况都判反。
func TestCheckIgnoresOffSpecOutlier(t *testing.T) {
	tests := []struct {
		name       string
		prices     []float64
		wantNotify bool
	}{
		// 均价 9.50 低于阈值，但 2 显然不是一份月卡，不该通知。
		{"极低价是别的规格", []float64{2, 11, 11.5, 12, 11}, false},
		// 均价 10.90 高于阈值，但 9 是真便宜，该通知。
		{"最低价接近价位", []float64{9, 11, 11.5, 12, 11}, true},
		// 剔除 2 之后，9 仍然值得提醒。
		{"剔除异常值后仍有真低价", []float64{2, 9, 11, 11.5, 12}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := newFakeTelegram(t)
			if _, err := check(context.Background(), stubFetch(offersAt(tt.prices...)),
				tg.client(), testConfig(), state.State{}); err != nil {
				t.Fatalf("check 失败: %v", err)
			}
			got := len(tg.messages()) == 1
			if got != tt.wantNotify {
				t.Errorf("是否通知 = %v, 期望 %v（价格 %v）", got, tt.wantNotify, tt.prices)
			}
		})
	}
}

// --no-rebound 时价格回升不发通知。
func TestCheckNoRebound(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig()
	cfg.noRebound = true
	n := tg.client()
	ctx := context.Background()

	st, _ := check(ctx, stubFetch(offersAt(8, 8, 8, 8, 8)), n, cfg, state.State{})
	_, _ = check(ctx, stubFetch(offersAt(20, 20, 20, 20, 20)), n, cfg, st)

	if got := len(tg.messages()); got != 1 {
		t.Errorf("发了 %d 条通知，期望只有降价那 1 条", got)
	}
}

// 一直高于阈值时一条都不该发。
func TestCheckSilentWhenAlwaysExpensive(t *testing.T) {
	tg := newFakeTelegram(t)
	if _, err := check(context.Background(),
		stubFetch(offersAt(100.94, 101.97, 105.06, 107.64, 108.15)),
		tg.client(), testConfig(), state.State{}); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	if n := len(tg.messages()); n != 0 {
		t.Errorf("发了 %d 条通知，期望 0 条", n)
	}
}

// 没有凭据时只记日志，但抓取错误仍要返回（--once 靠它拿非 0 退出码）。
func TestCheckWithoutNotifier(t *testing.T) {
	cfg := testConfig()
	if _, err := check(context.Background(), stubFetch(offersAt(1, 1, 1, 1, 1)), nil, cfg, state.State{}); err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	if _, err := check(context.Background(), failingFetch("boom"), nil, cfg, state.State{}); err == nil {
		t.Fatal("抓取失败时应返回错误")
	}
}

// 连续失败达到阈值才告警，且一轮故障只告一次。
func TestCheckAlertsAfterConsecutiveFailures(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig()
	fetch := failingFetch("接口只返回了 0 条报价")
	ctx := context.Background()

	var st state.State
	// 前两次失败不发通知，但都要把错误抛出去。
	for i := 1; i <= 2; i++ {
		var err error
		if st, err = check(ctx, fetch, tg.client(), cfg, st); err == nil {
			t.Fatalf("第 %d 次应返回错误", i)
		}
		if n := len(tg.messages()); n != 0 {
			t.Fatalf("第 %d 次就发了 %d 条通知，期望 0 条", i, n)
		}
	}

	st, _ = check(ctx, fetch, tg.client(), cfg, st)
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

	for i := 0; i < 3; i++ {
		st, _ = check(ctx, fetch, tg.client(), cfg, st)
	}
	if n := len(tg.messages()); n != 1 {
		t.Errorf("持续故障期间发了 %d 条通知，期望仍是 1 条", n)
	}
}

// 抓取恢复后要补一条恢复通知，并把失败计数清零。
func TestCheckNotifiesOnFetchRecovery(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig()
	cfg.failThreshold = 2
	n := tg.client()
	ctx := context.Background()

	var st state.State
	for i := 0; i < 2; i++ {
		st, _ = check(ctx, failingFetch("boom"), n, cfg, st)
	}
	if got := len(tg.messages()); got != 1 {
		t.Fatalf("故障期发了 %d 条，期望 1 条", got)
	}

	// 恢复，且价格很贵（不会触发降价通知），只应有恢复通知。
	st, err := check(ctx, stubFetch(offersAt(100, 100, 100, 100, 100)), n, cfg, st)
	if err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	msgs := tg.messages()
	if len(msgs) != 2 {
		t.Fatalf("发了 %d 条，期望 2 条", len(msgs))
	}
	if !strings.Contains(msgs[1], "已恢复") {
		t.Errorf("第二条应是恢复通知: %s", msgs[1])
	}
	if st.Failures != 0 || st.FailNotified {
		t.Errorf("恢复后失败计数应清零, 得到 %+v", st)
	}

	// 再失败一次不该立刻告警（计数已清零，阈值是 2）。
	_, _ = check(ctx, failingFetch("boom"), n, cfg, st)
	if got := len(tg.messages()); got != 2 {
		t.Errorf("恢复后第一次失败就告警了，期望仍是 2 条，得到 %d", got)
	}
}

// --fail-threshold 0 关闭失败告警。
func TestCheckFailAlertDisabled(t *testing.T) {
	tg := newFakeTelegram(t)
	cfg := testConfig()
	cfg.failThreshold = 0

	var st state.State
	for i := 0; i < 5; i++ {
		st, _ = check(context.Background(), failingFetch("boom"), tg.client(), cfg, st)
	}
	if n := len(tg.messages()); n != 0 {
		t.Errorf("关闭时发了 %d 条通知，期望 0 条", n)
	}
}

// 告警发送本身失败时不能标记成已告警，否则这轮故障就再也不会提醒了。
func TestCheckRetriesAlertWhenSendFails(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`)
			return
		}
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	cfg := testConfig()
	cfg.failThreshold = 1
	n := &telegram.Client{Token: "111:AAA", ChatID: "42", HTTP: srv.Client(), BaseURL: srv.URL}
	ctx := context.Background()

	st, _ := check(ctx, failingFetch("boom"), n, cfg, state.State{})
	if st.FailNotified {
		t.Fatal("通知发送失败时不应标记为已告警")
	}

	down.Store(false)
	st, _ = check(ctx, failingFetch("boom"), n, cfg, st)
	if sent.Load() != 1 {
		t.Errorf("补发了 %d 条，期望 1 条", sent.Load())
	}
	if !st.FailNotified {
		t.Error("补发成功后应标记为已告警")
	}
}

// 凭据只从环境变量读，不应有对应的命令行选项，帮助信息里也不会泄露。
func TestNoCredentialFlagsAndNoLeak(t *testing.T) {
	const secret = "123456:SUPER-SECRET"
	t.Setenv("TELEGRAM_BOT_TOKEN", secret)
	t.Setenv("TELEGRAM_CHAT_ID", "99887766")

	_, fs := parseFlags(nil)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.Usage()

	for _, leaked := range []string{secret, "99887766"} {
		if strings.Contains(buf.String(), leaked) {
			t.Errorf("帮助信息泄露了凭据 %q:\n%s", leaked, buf.String())
		}
	}
	for _, gone := range []string{"telegram-token", "telegram-chat", "telegram-api"} {
		if fs.Lookup(gone) != nil {
			t.Errorf("选项 --%s 应当已被移除", gone)
		}
	}
}

// 两个环境变量都设置了才发通知。
func TestNewNotifier(t *testing.T) {
	cases := []struct {
		name          string
		token, chatID string
		want          bool
	}{
		{"都设置了", "111:AAA", "42", true},
		{"只有 token", "111:AAA", "", false},
		{"只有 chat id", "", "42", false},
		{"都没设置", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TELEGRAM_BOT_TOKEN", c.token)
			t.Setenv("TELEGRAM_CHAT_ID", c.chatID)
			if got := newNotifier(http.DefaultClient); (got != nil) != c.want {
				t.Errorf("newNotifier() != nil = %v, 期望 %v", got != nil, c.want)
			}
		})
	}
}

// 短选项与长选项必须指向同一个变量。
func TestShortFlags(t *testing.T) {
	long, _ := parseFlags([]string{"--top", "8", "--threshold", "12.5", "--interval", "1h", "--sample", "50", "--verbose"})
	short, _ := parseFlags([]string{"-n", "8", "-t", "12.5", "-i", "1h", "-s", "50", "-v"})

	if *long != *short {
		t.Errorf("短选项结果与长选项不一致:\n长 %+v\n短 %+v", *long, *short)
	}
	if short.top != 8 || short.threshold != 12.5 || short.interval != time.Hour || short.sample != 50 || !short.verbose {
		t.Errorf("短选项解析结果不对: %+v", short)
	}
}

// 帮助里的选项顺序必须与 options 一致，且不能漏掉任何已注册的选项。
func TestPrintOptionsOrderAndCoverage(t *testing.T) {
	var buf bytes.Buffer
	fs := newFlagSet(&config{}, flag.ContinueOnError)
	printOptions(&buf, fs)
	out := buf.String()

	// 顺序：按 options 里的先后依次出现。
	pos := -1
	for _, o := range options {
		i := strings.Index(out, "--"+o.long)
		if i < 0 {
			t.Fatalf("帮助里缺少 --%s:\n%s", o.long, out)
		}
		if i < pos {
			t.Errorf("--%s 的位置不符合 options 的顺序:\n%s", o.long, out)
		}
		pos = i
	}

	// 覆盖：每个注册过的选项要么在 options 里，要么是短选项别名。
	// 少了这条，新加的选项会静默地从帮助里消失。
	known := map[string]bool{}
	for _, o := range options {
		known[o.long] = true
		if o.short != "" {
			known[o.short] = true
		}
	}
	fs.VisitAll(func(f *flag.Flag) {
		if !known[f.Name] {
			t.Errorf("选项 --%s 没有出现在 options 里，会从帮助中漏掉", f.Name)
		}
	})
}

func TestValidate(t *testing.T) {
	if err := testConfig().validate(); err != nil {
		t.Errorf("合法配置不应报错: %v", err)
	}
	bad := map[string]func(*config){
		"top 为 0":          func(c *config) { c.top = 0 },
		"阈值为 0":            func(c *config) { c.threshold = 0 },
		"interval 为 0":     func(c *config) { c.interval = 0 },
		"sample 为 0":       func(c *config) { c.sample = 0 },
		"floor-ratio 为 0":  func(c *config) { c.floorRatio = 0 },
		"floor-ratio 超过 1": func(c *config) { c.floorRatio = 1.5 },
		"超时为 0":            func(c *config) { c.timeout = 0 },
	}
	for name, mutate := range bad {
		c := testConfig()
		mutate(c)
		if err := c.validate(); err == nil {
			t.Errorf("%s 时期望报错", name)
		}
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
