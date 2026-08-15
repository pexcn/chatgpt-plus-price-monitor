// Command chatgpt-plus-price-monitor 监控 priceai.cc 上 ChatGPT Plus 的报价，
// 最便宜的 N 个的均价低于阈值时通过 Telegram 通知。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
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
	notifyRebound bool
	failThreshold int

	dryRun  bool
	verbose bool
}

// notifier 发送一条通知。没配置 Telegram 凭据时为 nil，此时只记日志。
type notifier interface {
	Send(ctx context.Context, text string) error
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	cfg, fs := parseFlags(os.Args[1:])

	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n\n", err)
		fs.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpc := &http.Client{Timeout: cfg.timeout}
	fetch := func(ctx context.Context, n int) ([]priceai.Offer, error) {
		return priceai.Cheapest(ctx, httpc, n)
	}

	notify := newNotifier(httpc)
	switch {
	case cfg.dryRun:
		notify = nil
		log.Print("dry-run：只打印结果，不发送通知、不写状态文件")
	case notify == nil:
		log.Print("未设置 TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID，只打印结果，不发送通知")
	}

	// interval 为 0 时跑一次就退出，交给 cron / systemd timer 调度。
	if cfg.interval <= 0 {
		if err := checkOnce(ctx, fetch, notify, cfg); err != nil {
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
		if err := checkOnce(ctx, fetch, notify, cfg); err != nil {
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

// newFlagSet 注册所有选项。用独立的 FlagSet 而不是全局的 flag.CommandLine，
// 这样测试里能拿到和线上完全一致的选项集合。
func newFlagSet(cfg *config, errorHandling flag.ErrorHandling) *flag.FlagSet {
	fs := flag.NewFlagSet("chatgpt-plus-price-monitor", errorHandling)

	fs.IntVar(&cfg.top, "top", 5, "取最便宜的 N 个报价计算均价")
	fs.Float64Var(&cfg.threshold, "threshold", 10, "阈值（元），均价 <= 该值时通知")

	fs.DurationVar(&cfg.interval, "interval", 0, "轮询间隔，如 30m；为 0 时只检查一次后退出（配合 cron 使用）")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "单次 HTTP 请求超时")

	fs.StringVar(&cfg.statePath, "state", "state.json", "状态文件路径，用于通知去重")
	fs.DurationVar(&cfg.cooldown, "cooldown", 24*time.Hour, "价格持续低于阈值时的重复提醒间隔；为 0 表示只在跌破时提醒一次")
	fs.BoolVar(&cfg.notifyRebound, "notify-rebound", true, "价格回升到阈值之上时也发一条通知")
	fs.IntVar(&cfg.failThreshold, "fail-threshold", 3, "连续抓取失败 N 次后发告警（避免监控静默失效）；0 表示关闭")

	fs.BoolVar(&cfg.dryRun, "dry-run", false, "只抓取并打印结果，不发送通知、不写状态文件")
	fs.BoolVar(&cfg.verbose, "verbose", false, "打印每条报价的店铺和标题")

	fs.Usage = func() { usage(fs) }
	return fs
}

func parseFlags(args []string) (*config, *flag.FlagSet) {
	cfg := &config{}
	fs := newFlagSet(cfg, flag.ExitOnError)
	_ = fs.Parse(args)
	return cfg, fs
}

// newNotifier 从环境变量读 Telegram 凭据。
//
// 凭据只从环境变量读、不做成命令行选项：写在命令行上同机器的其他人 ps 就能看到，
// 而且一旦把 os.Getenv 当成 flag 的默认值，--help 就会把 token 原样打出来。
//
// 没配全凭据时返回 nil，调用方只记日志。
func newNotifier(httpc *http.Client) notifier {
	token, chatID := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return nil
	}
	return &telegram.Client{Token: token, ChatID: chatID, HTTP: httpc}
}

func usage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintf(out, "监控 ChatGPT Plus 代充价格，低于阈值时 Telegram 通知。\n\n用法:\n  %s [options]\n\n选项:\n", os.Args[0])
	printOptions(out, fs)
	fmt.Fprint(out, `
Telegram 凭据只从环境变量读取（都设置了才会发通知，否则只打印日志）:
  TELEGRAM_BOT_TOKEN    Bot Token
  TELEGRAM_CHAT_ID      Chat ID

示例:
  # 先看看现在什么价（没有环境变量，只打印）
  chatgpt-plus-price-monitor --verbose

  # 常驻运行，每 30 分钟检查一次
  export TELEGRAM_BOT_TOKEN=123456:ABC...
  export TELEGRAM_CHAT_ID=123456789
  chatgpt-plus-price-monitor --interval 30m --top 5 --threshold 10
`)
}

// printOptions 用 --name 的风格打印选项。
//
// 标准库的 flag.PrintDefaults 只会打印单横线，而 Go 的 flag 包本身
// 单双横线都接受，所以这里只是把帮助信息统一成双横线的写法。
func printOptions(out io.Writer, fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		typeName, help := flag.UnquoteUsage(f)
		head := "  --" + f.Name
		if typeName != "" {
			head += " " + typeName
		}
		fmt.Fprintf(out, "%s\n    \t%s", head, help)
		// 布尔开关默认关闭时没必要写出来，噪音。
		if f.DefValue != "" && f.DefValue != "false" {
			fmt.Fprintf(out, "（默认 %s）", f.DefValue)
		}
		fmt.Fprintln(out)
	})
}

func (c *config) validate() error {
	if c.top <= 0 {
		return fmt.Errorf("--top 必须大于 0")
	}
	if c.threshold <= 0 {
		return fmt.Errorf("--threshold 必须大于 0")
	}
	if c.timeout <= 0 {
		return fmt.Errorf("--timeout 必须大于 0")
	}
	return nil
}

// fetcher 取最便宜的 n 条报价。抽成函数类型是为了让测试能替换掉真实接口。
type fetcher func(ctx context.Context, n int) ([]priceai.Offer, error)

func checkOnce(ctx context.Context, fetch fetcher, notify notifier, cfg *config) error {
	offers, fetchErr := fetch(ctx, cfg.top)

	// 不发通知时也不碰状态文件：否则等你配好凭据，第一次降价会因为
	// 状态里已经记着"低于阈值"而被当成重复通知吞掉。
	if notify == nil {
		if fetchErr != nil {
			return fetchErr
		}
		logResult(cfg, offers, priceai.Summarize(offers))
		return nil
	}

	prev, err := state.Load(cfg.statePath)
	if err != nil {
		return fmt.Errorf("读取状态文件失败: %w", err)
	}
	if fetchErr != nil {
		return reportFailure(ctx, notify, cfg, prev, fetchErr)
	}
	return reportPrice(ctx, notify, cfg, prev, offers)
}

// reportFailure 处理抓取失败：累计次数，达到阈值时告警一次。
//
// 无论有没有发通知都原样返回抓取错误，让单次模式保持非 0 退出码。
func reportFailure(ctx context.Context, notify notifier, cfg *config, prev state.State, cause error) error {
	next := prev
	next.Failures++

	// 一轮故障只告警一次，恢复后才会重新武装。
	if cfg.failThreshold > 0 && next.Failures >= cfg.failThreshold && !prev.FailNotified {
		if err := notify.Send(ctx, buildFailureMessage(next.Failures, cause)); err != nil {
			// 通知本身也失败了（比如整个网络都不通），保持未告警状态下轮重试。
			log.Printf("发送失败告警时出错: %v", err)
		} else {
			next.FailNotified = true
			log.Printf("已发送抓取失败告警（连续失败 %d 次）", next.Failures)
		}
	}

	if err := state.Save(cfg.statePath, next); err != nil {
		log.Printf("写状态文件失败: %v", err)
	}
	return cause
}

// reportPrice 处理抓取成功：先补一条恢复通知（如果之前告过警），再走价格判断。
func reportPrice(ctx context.Context, notify notifier, cfg *config, prev state.State, offers []priceai.Offer) error {
	st := priceai.Summarize(offers)
	below := st.Avg <= cfg.threshold
	logResult(cfg, offers, st)

	next := prev
	next.Below = below
	next.Failures = 0
	next.FailNotified = false

	if prev.FailNotified {
		if err := notify.Send(ctx, buildRecoveryMessage(prev.Failures)); err != nil {
			log.Printf("发送恢复通知时出错: %v", err)
		} else {
			log.Print("已发送抓取恢复通知")
		}
	}

	now := time.Now()
	action := prev.Decide(below, now, cfg.cooldown, cfg.notifyRebound)
	if action == state.Silent {
		// 状态本身仍要更新，否则跌破后的第一条提醒会重复发。
		return state.Save(cfg.statePath, next)
	}

	if err := notify.Send(ctx, buildMessage(action, cfg, offers, st, prev.LastAvg)); err != nil {
		return err
	}
	log.Printf("已发送 Telegram 通知 (%s)", action)

	next.LastNotify = now
	next.LastAvg = st.Avg
	return state.Save(cfg.statePath, next)
}

func logResult(cfg *config, offers []priceai.Offer, st priceai.Stats) {
	reached := "未达成"
	if st.Avg <= cfg.threshold {
		reached = "已达成"
	}
	log.Printf("最便宜的 %d 个均价 %.2f 元（最低 %.2f / 最高 %.2f），阈值 %.2f -> %s",
		cfg.top, st.Avg, st.Min, st.Max, cfg.threshold, reached)
	if cfg.verbose {
		for i, o := range offers {
			log.Printf("  %d. %.2f 元 | %s | %s", i+1, o.Price, o.Store(), o.SourceTitle)
		}
	}
}

func buildMessage(action state.Action, cfg *config, offers []priceai.Offer, st priceai.Stats, prevAvg float64) string {
	var b strings.Builder
	switch action {
	case state.AlertBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 降价提醒</b>\n\n")
	case state.RemindBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 仍在低价</b>\n\n")
	case state.AlertRebound:
		b.WriteString("🔺 <b>ChatGPT Plus 价格回升</b>\n\n")
	}

	fmt.Fprintf(&b, "最便宜的 %d 个均价：<b>%.2f</b> 元（阈值 %.2f）\n", cfg.top, st.Avg, cfg.threshold)
	fmt.Fprintf(&b, "最低 %.2f / 最高 %.2f\n", st.Min, st.Max)
	if action == state.AlertRebound && prevAvg > 0 {
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

func buildFailureMessage(failures int, cause error) string {
	var b strings.Builder
	b.WriteString("⚠️ <b>价格监控异常</b>\n\n")
	fmt.Fprintf(&b, "已连续 %d 次抓取失败，监控可能已经失效。\n\n", failures)
	fmt.Fprintf(&b, "最后一次错误：\n<code>%s</code>\n", telegram.Escape(cause.Error()))
	fmt.Fprintf(&b, "\n接口可能改版了，需要人工确认：\n%s", priceai.ProductPage)
	return b.String()
}

func buildRecoveryMessage(failures int) string {
	var b strings.Builder
	b.WriteString("✅ <b>价格监控已恢复</b>\n\n")
	fmt.Fprintf(&b, "抓取重新正常（此前连续失败 %d 次），继续监控中。", failures)
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
