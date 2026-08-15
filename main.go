// Command chatgpt-plus-price-monitor 监控 priceai.cc 上 ChatGPT Plus 的报价，
// 最便宜的 N 个的均价低于阈值时通过 Telegram 通知。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/telegram"
)

type config struct {
	top       int
	threshold float64

	interval time.Duration
	timeout  time.Duration

	statePath     string
	cooldown      time.Duration
	notifyRecover bool

	token   string
	chatID  string
	apiBase string

	dryRun  bool
	verbose bool
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
	fetch := func(ctx context.Context, n int) ([]priceai.Offer, error) {
		return priceai.Cheapest(ctx, httpc, n)
	}

	// interval 为 0 时跑一次就退出，交给 cron / systemd timer 调度。
	if cfg.interval <= 0 {
		if err := checkOnce(ctx, fetch, httpc, cfg); err != nil {
			log.Printf("检查失败: %v", err)
			os.Exit(1)
		}
		return
	}

	log.Printf("开始监控（每 %s 检查一次，最便宜的 %d 个均价 <= %.2f 元时通知）",
		cfg.interval, cfg.top, cfg.threshold)

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	for {
		if err := checkOnce(ctx, fetch, httpc, cfg); err != nil {
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
	flag.IntVar(&cfg.top, "top", 5, "取最便宜的 N 个报价计算均价")
	flag.Float64Var(&cfg.threshold, "threshold", 10, "阈值（元），均价 <= 该值时通知")

	flag.DurationVar(&cfg.interval, "interval", 0, "轮询间隔，如 30m；为 0 时只检查一次后退出（配合 cron 使用）")
	flag.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "单次 HTTP 请求超时")

	flag.StringVar(&cfg.statePath, "state", "state.json", "状态文件路径，用于通知去重")
	flag.DurationVar(&cfg.cooldown, "cooldown", 24*time.Hour, "价格持续低于阈值时的重复提醒间隔；为 0 表示只在跌破时提醒一次")
	flag.BoolVar(&cfg.notifyRecover, "notify-recover", true, "价格回升到阈值之上时也发一条通知")

	flag.StringVar(&cfg.token, "telegram-token", os.Getenv("TELEGRAM_BOT_TOKEN"), "Telegram Bot Token（建议改用环境变量 TELEGRAM_BOT_TOKEN）")
	flag.StringVar(&cfg.chatID, "telegram-chat", os.Getenv("TELEGRAM_CHAT_ID"), "Telegram Chat ID（可用环境变量 TELEGRAM_CHAT_ID）")
	flag.StringVar(&cfg.apiBase, "telegram-api", envOr("TELEGRAM_API_BASE", "https://api.telegram.org"), "Telegram API 地址；国内直连不通时可指向自建 Bot API 或反代")

	flag.BoolVar(&cfg.dryRun, "dry-run", false, "只抓取并打印结果，不发送通知、不写状态文件")
	flag.BoolVar(&cfg.verbose, "verbose", false, "打印每条报价的店铺和标题")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "监控 ChatGPT Plus 代充价格，低于阈值时 Telegram 通知。\n\n用法:\n  %s [options]\n\n选项:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprint(flag.CommandLine.Output(), `
示例:
  # 先看看现在什么价（不发通知）
  chatgpt-plus-price-monitor -dry-run -verbose

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
	if !c.dryRun && (c.token == "" || c.chatID == "") {
		return fmt.Errorf("需要 Telegram 凭据：设置 TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID 环境变量，或用 -telegram-token / -telegram-chat；也可以加 -dry-run 先只看结果")
	}
	return nil
}

// fetcher 取最便宜的 n 条报价。抽成函数类型是为了让测试能替换掉真实接口。
type fetcher func(ctx context.Context, n int) ([]priceai.Offer, error)

func checkOnce(ctx context.Context, fetch fetcher, httpc *http.Client, cfg *config) error {
	offers, err := fetch(ctx, cfg.top)
	if err != nil {
		return err
	}
	st := priceai.Summarize(offers)
	below := st.Avg <= cfg.threshold

	log.Printf("最便宜的 %d 个均价 %.2f 元（最低 %.2f / 最高 %.2f），阈值 %.2f -> %s",
		cfg.top, st.Avg, st.Min, st.Max, cfg.threshold,
		map[bool]string{true: "已达成", false: "未达成"}[below])
	if cfg.verbose {
		for i, o := range offers {
			log.Printf("  %d. %.2f 元 | %s | %s", i+1, o.Price, o.Store(), o.SourceTitle)
		}
	}

	if cfg.dryRun {
		log.Print("dry-run：跳过通知")
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
	if err := tg.Send(ctx, buildMessage(action, cfg, offers, st, prev.LastAvg)); err != nil {
		return err
	}
	log.Printf("已发送 Telegram 通知 (%s)", action)

	return state.Save(cfg.statePath, state.State{Below: below, LastNotify: now, LastAvg: st.Avg})
}

func buildMessage(action state.Action, cfg *config, offers []priceai.Offer, st priceai.Stats, prevAvg float64) string {
	var b strings.Builder
	switch action {
	case state.AlertBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 降价提醒</b>\n\n")
	case state.RemindBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 仍在低价</b>\n\n")
	case state.AlertRecover:
		b.WriteString("🔺 <b>ChatGPT Plus 价格回升</b>\n\n")
	}

	fmt.Fprintf(&b, "最便宜的 %d 个均价：<b>%.2f</b> 元（阈值 %.2f）\n", cfg.top, st.Avg, cfg.threshold)
	fmt.Fprintf(&b, "最低 %.2f / 最高 %.2f\n", st.Min, st.Max)
	if action == state.AlertRecover && prevAvg > 0 {
		fmt.Fprintf(&b, "上次通知时为 %.2f 元\n", prevAvg)
	}

	b.WriteString("\n")
	for i, o := range offers {
		// 价格做成可点的链接，收到通知就能直接跳去下单。
		fmt.Fprintf(&b, "%d. <a href=\"%s\">%.2f 元</a> · %s\n",
			i+1, telegram.Escape(o.URL), o.Price, telegram.Escape(o.Store()))
		if t := o.SourceTitle; t != "" {
			fmt.Fprintf(&b, "    <i>%s</i>\n", telegram.Escape(truncate(t, 50)))
		}
	}

	fmt.Fprintf(&b, "\n%s", priceai.ProductPage)
	return b.String()
}

// truncate 按字符（而非字节）截断，避免把中文切坏。
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
