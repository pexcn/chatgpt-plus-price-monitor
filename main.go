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
	interval  time.Duration
	once      bool

	cooldown      time.Duration
	noRebound     bool
	failThreshold int
	timeout       time.Duration
	verbose       bool
}

// notifier 发送一条通知。没配置 Telegram 凭据时为 nil，此时只记日志。
type notifier interface {
	Send(ctx context.Context, text string) error
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	cfg, fs := parseFlags(os.Args[1:])
	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n", err)
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
	if notify == nil {
		log.Print("未设置 TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID，只打印结果，不发送通知")
	}

	if cfg.once {
		if _, err := check(ctx, fetch, notify, cfg, state.State{}); err != nil {
			log.Printf("检查失败: %v", err)
			os.Exit(1)
		}
		return
	}

	log.Printf("开始监控（每 %s 检查一次，最便宜的 %d 个均价 <= %.2f 元时通知）",
		cfg.interval, cfg.top, cfg.threshold)

	// 状态只存在内存里，所以必须常驻运行才能去重。
	var st state.State
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	for {
		next, err := check(ctx, fetch, notify, cfg, st)
		if err != nil {
			// 单次失败不退出，等下一轮重试。
			log.Printf("检查失败: %v", err)
		}
		st = next

		select {
		case <-ctx.Done():
			log.Print("收到退出信号，停止监控")
			return
		case <-ticker.C:
		}
	}
}

// options 决定 --help 里的顺序，按重要程度排列。
// flag 包自带的输出是字典序的，跟使用频率对不上。
var options = []struct{ short, long string }{
	{"n", "top"},
	{"t", "threshold"},
	{"i", "interval"},
	{"", "once"},
	{"", "cooldown"},
	{"", "no-rebound"},
	{"", "fail-threshold"},
	{"", "timeout"},
	{"v", "verbose"},
}

func newFlagSet(cfg *config, errorHandling flag.ErrorHandling) *flag.FlagSet {
	fs := flag.NewFlagSet("chatgpt-plus-price-monitor", errorHandling)

	fs.IntVar(&cfg.top, "top", 5, "取最便宜的 N 个报价计算均价")
	fs.Float64Var(&cfg.threshold, "threshold", 10, "均价低于该价格（元）时通知")
	fs.DurationVar(&cfg.interval, "interval", 30*time.Minute, "轮询间隔")
	fs.BoolVar(&cfg.once, "once", false, "只检查一次就退出")
	fs.DurationVar(&cfg.cooldown, "cooldown", 24*time.Hour, "持续低于阈值时的重复提醒间隔，0 表示只提醒一次")
	fs.BoolVar(&cfg.noRebound, "no-rebound", false, "价格回升到阈值之上时不通知")
	fs.IntVar(&cfg.failThreshold, "fail-threshold", 3, "连续抓取失败 N 次后告警，0 表示关闭")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "单次 HTTP 请求超时")
	fs.BoolVar(&cfg.verbose, "verbose", false, "打印每条报价的店铺和标题")

	// 短选项和长选项共用同一个变量，这是 flag 包里做别名的常规写法。
	fs.IntVar(&cfg.top, "n", 5, "")
	fs.Float64Var(&cfg.threshold, "t", 10, "")
	fs.DurationVar(&cfg.interval, "i", 30*time.Minute, "")
	fs.BoolVar(&cfg.verbose, "v", false, "")

	fs.Usage = func() { usage(fs) }
	return fs
}

func parseFlags(args []string) (*config, *flag.FlagSet) {
	cfg := &config{}
	fs := newFlagSet(cfg, flag.ExitOnError)
	_ = fs.Parse(args)
	return cfg, fs
}

// newNotifier 从环境变量读 Telegram 凭据，没配全就返回 nil（只记日志）。
//
// 凭据不做成命令行选项：写在命令行上同机器的其他人 ps 就能看到，而且一旦把
// os.Getenv 当成 flag 的默认值，--help 会把 token 原样打出来。
func newNotifier(httpc *http.Client) notifier {
	token, chatID := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return nil
	}
	return &telegram.Client{Token: token, ChatID: chatID, HTTP: httpc}
}

func usage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprint(out, `监控 ChatGPT Plus 代充价格，低于阈值时 Telegram 通知。

Usage:
  chatgpt-plus-price-monitor [flags]

Flags:
`)
	printOptions(out, fs)
	fmt.Fprint(out, `
Environment:
  TELEGRAM_BOT_TOKEN    Bot Token，与 CHAT_ID 都设置时才发送通知
  TELEGRAM_CHAT_ID      Chat ID
`)
}

func printOptions(out io.Writer, fs *flag.FlagSet) {
	type row struct{ head, help string }
	rows := make([]row, 0, len(options))
	width := 0

	for _, o := range options {
		f := fs.Lookup(o.long)
		if f == nil {
			continue
		}
		head := "      --" + o.long
		if o.short != "" {
			head = "  -" + o.short + ", --" + o.long
		}
		name, help := flag.UnquoteUsage(f)
		if name != "" {
			head += " " + name
		}
		if d := f.DefValue; d != "" && d != "false" {
			help += fmt.Sprintf(" (default %s)", d)
		}
		if len(head) > width {
			width = len(head)
		}
		rows = append(rows, row{head, help})
	}
	for _, r := range rows {
		fmt.Fprintf(out, "%-*s  %s\n", width, r.head, r.help)
	}
}

func (c *config) validate() error {
	switch {
	case c.top <= 0:
		return fmt.Errorf("--top 必须大于 0")
	case c.threshold <= 0:
		return fmt.Errorf("--threshold 必须大于 0")
	case c.interval <= 0:
		return fmt.Errorf("--interval 必须大于 0")
	case c.timeout <= 0:
		return fmt.Errorf("--timeout 必须大于 0")
	}
	return nil
}

// fetcher 取最便宜的 n 条报价。抽成函数类型是为了让测试能替换掉真实接口。
type fetcher func(ctx context.Context, n int) ([]priceai.Offer, error)

// check 跑一轮检查，返回更新后的状态。
func check(ctx context.Context, fetch fetcher, notify notifier, cfg *config, prev state.State) (state.State, error) {
	offers, err := fetch(ctx, cfg.top)
	if err != nil {
		return reportFailure(ctx, notify, cfg, prev, err)
	}

	st := priceai.Summarize(offers)
	below := st.Avg <= cfg.threshold
	logResult(cfg, offers, st, below)

	next := prev
	next.Below = below
	next.Failures = 0
	next.FailNotified = false

	if notify == nil {
		return next, nil
	}
	if prev.FailNotified {
		send(ctx, notify, buildRecoveryMessage(prev.Failures), "抓取恢复通知")
	}

	now := time.Now()
	action := prev.Decide(below, now, cfg.cooldown, !cfg.noRebound)
	if action == state.Silent {
		return next, nil
	}
	if !send(ctx, notify, buildMessage(action, cfg, offers, st, prev.LastAvg), action.String()) {
		return next, nil
	}
	next.LastNotify = now
	next.LastAvg = st.Avg
	return next, nil
}

// reportFailure 累计失败次数，达到阈值时告警一次，并原样返回抓取错误。
func reportFailure(ctx context.Context, notify notifier, cfg *config, prev state.State, cause error) (state.State, error) {
	next := prev
	next.Failures++
	if notify == nil {
		return next, cause
	}
	// 一轮故障只告警一次，恢复后才会重新武装。发送失败时不置位，下轮重试。
	if cfg.failThreshold > 0 && next.Failures >= cfg.failThreshold && !prev.FailNotified {
		if send(ctx, notify, buildFailureMessage(next.Failures, cause), "抓取失败告警") {
			next.FailNotified = true
		}
	}
	return next, cause
}

// send 发送一条通知，失败只记日志不中断监控。
func send(ctx context.Context, notify notifier, msg, kind string) bool {
	if err := notify.Send(ctx, msg); err != nil {
		log.Printf("发送%s失败: %v", kind, err)
		return false
	}
	log.Printf("已发送 %s", kind)
	return true
}

func logResult(cfg *config, offers []priceai.Offer, st priceai.Stats, below bool) {
	reached := "未达成"
	if below {
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
	return fmt.Sprintf("✅ <b>价格监控已恢复</b>\n\n抓取重新正常（此前连续失败 %d 次），继续监控中。", failures)
}

// truncate 按字符（而非字节）截断，避免把中文切坏。
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
