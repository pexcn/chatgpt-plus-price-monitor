// Command chatgpt-plus-price-monitor 监控 priceai.cc 上 ChatGPT Plus 的挂单价格，
// 当前 N 个的均价低于阈值时通过 Telegram 通知。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/scraper"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/telegram"
)

const defaultURL = "https://priceai.cc/products/chatgpt-plus"

type config struct {
	url       string
	top       int
	threshold float64
	selector  string
	sortAsc   bool
	userAgent string

	interval time.Duration
	timeout  time.Duration

	statePath     string
	cooldown      time.Duration
	notifyRecover bool

	token   string
	chatID  string
	apiBase string

	dryRun   bool
	dumpPath string
	verbose  bool
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	cfg := parseFlags()

	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n\n", err)
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpc := &http.Client{Timeout: cfg.timeout}

	// interval 为 0 时跑一次就退出，交给 cron / systemd timer 调度。
	if cfg.interval <= 0 {
		if err := checkOnce(ctx, httpc, cfg); err != nil {
			log.Printf("检查失败: %v", err)
			os.Exit(1)
		}
		return
	}

	log.Printf("开始监控 %s（每 %s 检查一次，前 %d 个均价 <= %.2f 时通知）",
		cfg.url, cfg.interval, cfg.top, cfg.threshold)

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	for {
		if err := checkOnce(ctx, httpc, cfg); err != nil {
			// 常驻模式下单次失败不退出，等下一轮重试。
			log.Printf("检查失败: %v", err)
		}
		select {
		case <-ctx.Done():
			log.Print("收到退出信号，停止监控")
			return
		case <-ticker.C:
		}
	}
}

func parseFlags() *config {
	cfg := &config{}
	flag.StringVar(&cfg.url, "url", defaultURL, "监控的页面地址")
	flag.IntVar(&cfg.top, "top", 5, "取前 N 个价格计算均价")
	flag.Float64Var(&cfg.threshold, "threshold", 10, "阈值（元），均价 <= 该值时通知")
	flag.StringVar(&cfg.selector, "selector", "", "价格元素的 CSS 选择器；留空则自动识别")
	flag.BoolVar(&cfg.sortAsc, "sort", false, "先按价格升序再取前 N（即最便宜的 N 个），默认按页面顺序")
	flag.StringVar(&cfg.userAgent, "user-agent", "", "自定义 User-Agent")

	flag.DurationVar(&cfg.interval, "interval", 0, "轮询间隔，如 30m；为 0 时只检查一次后退出（配合 cron 使用）")
	flag.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "单次 HTTP 请求超时")

	flag.StringVar(&cfg.statePath, "state", "state.json", "状态文件路径，用于通知去重")
	flag.DurationVar(&cfg.cooldown, "cooldown", 24*time.Hour, "价格持续低于阈值时的重复提醒间隔；为 0 表示只在跌破时提醒一次")
	flag.BoolVar(&cfg.notifyRecover, "notify-recover", true, "价格回升到阈值之上时也发一条通知")

	flag.StringVar(&cfg.token, "telegram-token", os.Getenv("TELEGRAM_BOT_TOKEN"), "Telegram Bot Token（建议改用环境变量 TELEGRAM_BOT_TOKEN）")
	flag.StringVar(&cfg.chatID, "telegram-chat", os.Getenv("TELEGRAM_CHAT_ID"), "Telegram Chat ID（可用环境变量 TELEGRAM_CHAT_ID）")
	flag.StringVar(&cfg.apiBase, "telegram-api", envOr("TELEGRAM_API_BASE", "https://api.telegram.org"), "Telegram API 地址；国内直连不通时可指向自建 Bot API 或反代")

	flag.BoolVar(&cfg.dryRun, "dry-run", false, "只抓取并打印结果，不发送通知、不写状态文件")
	flag.StringVar(&cfg.dumpPath, "dump", "", "把抓到的原始 HTML 存到指定文件，用于调试选择器")
	flag.BoolVar(&cfg.verbose, "verbose", false, "输出每次抓到的完整价格列表")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "监控 ChatGPT Plus 代充价格，低于阈值时 Telegram 通知。\n\n用法:\n  %s [options]\n\n选项:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprint(flag.CommandLine.Output(), `
示例:
  # 先确认能正确抓到价格（不发通知）
  chatgpt-plus-price-monitor -dry-run -verbose

  # 抓不到时保存页面，人工确认选择器
  chatgpt-plus-price-monitor -dry-run -dump page.html

  # 常驻运行，每 30 分钟检查一次
  export TELEGRAM_BOT_TOKEN=123456:ABC...
  export TELEGRAM_CHAT_ID=123456789
  chatgpt-plus-price-monitor -interval 30m -top 5 -threshold 10
`)
	}
	flag.Parse()
	return cfg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (c *config) validate() error {
	if c.top <= 0 {
		return fmt.Errorf("-top 必须大于 0")
	}
	if c.threshold <= 0 {
		return fmt.Errorf("-threshold 必须大于 0")
	}
	if c.timeout <= 0 {
		return fmt.Errorf("-timeout 必须大于 0")
	}
	if !strings.HasPrefix(c.url, "http://") && !strings.HasPrefix(c.url, "https://") {
		return fmt.Errorf("-url 必须以 http:// 或 https:// 开头")
	}
	if !c.dryRun && (c.token == "" || c.chatID == "") {
		return fmt.Errorf("需要 Telegram 凭据：设置 TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID 环境变量，或用 -telegram-token / -telegram-chat；也可以加 -dry-run 先只看结果")
	}
	return nil
}

func checkOnce(ctx context.Context, httpc *http.Client, cfg *config) error {
	res, err := scraper.Fetch(ctx, httpc, scraper.Options{
		URL:       cfg.url,
		Selector:  cfg.selector,
		UserAgent: cfg.userAgent,
		DumpPath:  cfg.dumpPath,
	})
	if err != nil {
		return err
	}
	if cfg.verbose {
		log.Printf("提取方式=%s 共 %d 个价格: %s", res.Method, len(res.Prices), formatPrices(res.Prices))
	}
	// 样本不足说明页面结构可能变了，此时算出的均价不可信，
	// 宁可报错也不要误报一条"降价了"。
	if len(res.Prices) < cfg.top {
		return fmt.Errorf("只抓到 %d 个价格，少于 -top %d（提取方式=%s）；页面结构可能已变化，可用 -dump 保存页面后用 -selector 指定",
			len(res.Prices), cfg.top, res.Method)
	}

	top := scraper.TopN(res.Prices, cfg.top, cfg.sortAsc)
	st := scraper.Summarize(top)
	below := st.Avg <= cfg.threshold

	log.Printf("前 %d 个均价 %.2f 元（最低 %.2f / 最高 %.2f），阈值 %.2f -> %s",
		cfg.top, st.Avg, st.Min, st.Max, cfg.threshold, map[bool]string{true: "已达成", false: "未达成"}[below])

	if cfg.dryRun {
		log.Printf("dry-run：跳过通知，明细 %s", formatPrices(top))
		return nil
	}

	prev, err := state.Load(cfg.statePath)
	if err != nil {
		return fmt.Errorf("读取状态文件失败: %w", err)
	}
	now := time.Now()
	action := prev.Decide(below, now, cfg.cooldown, cfg.notifyRecover)
	if action == state.Silent {
		// 状态本身仍要更新，否则跌破后的第一条提醒会重复发。
		return state.Save(cfg.statePath, state.State{Below: below, LastNotify: prev.LastNotify, LastAvg: prev.LastAvg})
	}

	tg := &telegram.Client{Token: cfg.token, ChatID: cfg.chatID, HTTP: httpc, BaseURL: cfg.apiBase}
	msg := buildMessage(action, cfg, top, st, prev.LastAvg)
	if err := tg.Send(ctx, msg); err != nil {
		return err
	}
	log.Printf("已发送 Telegram 通知 (%s)", action)

	return state.Save(cfg.statePath, state.State{Below: below, LastNotify: now, LastAvg: st.Avg})
}

func buildMessage(action state.Action, cfg *config, top []float64, st scraper.Stats, prevAvg float64) string {
	var b strings.Builder
	switch action {
	case state.AlertBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 降价提醒</b>\n\n")
	case state.RemindBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 仍在低价</b>\n\n")
	case state.AlertRecover:
		b.WriteString("🔺 <b>ChatGPT Plus 价格回升</b>\n\n")
	}

	order := "页面顺序"
	if cfg.sortAsc {
		order = "价格升序"
	}
	fmt.Fprintf(&b, "前 %d 个均价：<b>%.2f</b> 元（阈值 %.2f，%s）\n", cfg.top, st.Avg, cfg.threshold, order)
	fmt.Fprintf(&b, "最低 %.2f / 最高 %.2f\n", st.Min, st.Max)
	fmt.Fprintf(&b, "明细：%s\n", telegram.Escape(formatPrices(top)))
	if action == state.AlertRecover && prevAvg > 0 {
		fmt.Fprintf(&b, "上次通知时为 %.2f 元\n", prevAvg)
	}
	fmt.Fprintf(&b, "\n%s", telegram.Escape(cfg.url))
	return b.String()
}

func formatPrices(prices []float64) string {
	parts := make([]string, 0, len(prices))
	for _, p := range prices {
		parts = append(parts, strconv.FormatFloat(p, 'f', 2, 64))
	}
	return strings.Join(parts, ", ")
}
