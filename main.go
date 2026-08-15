// Command chatgpt-plus-price-monitor 监控 priceai.cc 上 ChatGPT Plus 的报价，
// 最便宜的 N 个的均价低于阈值时通过 Telegram 通知。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
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
	threshold float64
	interval  time.Duration
	jitter    time.Duration

	sample        int
	floorRatio    float64
	floorRatioSet bool
	top           int

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

	// interval 为 0 时跑一次就退出。
	if cfg.interval == 0 {
		if _, err := check(ctx, fetch, notify, cfg, state.State{}); err != nil {
			log.Printf("检查失败: %v", err)
			os.Exit(1)
		}
		return
	}

	log.Printf("开始监控（每 %s 检查一次，最便宜的可信报价 <= %.2f 元时通知）",
		intervalText(cfg.interval, cfg.jitter), cfg.threshold)

	// 状态只存在内存里，所以必须常驻运行才能去重。
	var st state.State
	for {
		next, err := check(ctx, fetch, notify, cfg, st)
		if err != nil {
			// 单次失败不退出，等下一轮重试。
			log.Printf("检查失败: %v", err)
		}
		st = next

		wait := cfg.interval + randomJitter(cfg.jitter)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			log.Print("收到退出信号，停止监控")
			return
		case <-timer.C:
		}
	}
}

// options 决定 --help 里的顺序，按重要程度排列。
// flag 包自带的输出是字典序的，跟使用频率对不上。
var options = []struct{ short, long string }{
	{"t", "threshold"},
	{"i", "interval"},
	{"s", "sample"},
	{"", "floor-ratio"},
	{"n", "top"},
	{"", "cooldown"},
	{"", "no-rebound"},
	{"", "fail-threshold"},
	{"", "timeout"},
	{"v", "verbose"},
}

func newFlagSet(cfg *config, errorHandling flag.ErrorHandling) *flag.FlagSet {
	fs := flag.NewFlagSet("chatgpt-plus-price-monitor", errorHandling)

	fs.Float64Var(&cfg.threshold, "threshold", 10, "最便宜的可信报价低于该价格（元）时通知")
	intervalValue := intervalFlag{base: 3 * time.Minute, jitter: time.Minute, cfg: cfg}
	cfg.interval, cfg.jitter = intervalValue.base, intervalValue.jitter
	fs.Var(&intervalValue, "interval", "轮询间隔，格式为 基础间隔[:最大抖动]，0 表示只检查一次就退出")
	fs.IntVar(&cfg.sample, "sample", 30, "取多少条报价作为参考价位的样本")
	fs.Float64Var(&cfg.floorRatio, "floor-ratio", 0, "启用地板线过滤：低于\"参考价位×该比例\"的报价将被剔除")
	fs.IntVar(&cfg.top, "top", 5, "通知里列出最便宜的 N 条")
	fs.DurationVar(&cfg.cooldown, "cooldown", 24*time.Hour, "持续低于阈值时的重复提醒间隔，0 表示只提醒一次")
	fs.BoolVar(&cfg.noRebound, "no-rebound", false, "价格回升到阈值之上时不通知")
	fs.IntVar(&cfg.failThreshold, "fail-threshold", 3, "连续抓取失败 N 次后告警，0 表示关闭")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "单次 HTTP 请求超时")
	fs.BoolVar(&cfg.verbose, "verbose", false, "打印每条报价的店铺和标题")

	// 短选项和长选项共用同一个变量，这是 flag 包里做别名的常规写法。
	fs.Float64Var(&cfg.threshold, "t", 10, "")
	fs.Var(&intervalValue, "i", "")
	fs.IntVar(&cfg.sample, "s", 30, "")
	fs.IntVar(&cfg.top, "n", 5, "")
	fs.BoolVar(&cfg.verbose, "v", false, "")

	fs.Usage = func() { usage(fs) }
	return fs
}

// intervalFlag 解析例如 3m:1m。没有冒号时抖动为 0。
type intervalFlag struct {
	base   time.Duration
	jitter time.Duration
	cfg    *config
}

func (f *intervalFlag) String() string {
	if f.jitter == 0 {
		return shortDuration(f.base)
	}
	return shortDuration(f.base) + ":" + shortDuration(f.jitter)
}

func (f *intervalFlag) Set(value string) error {
	if value == "0" {
		f.base, f.jitter = 0, 0
		f.cfg.interval, f.cfg.jitter = 0, 0
		return nil
	}
	parts := strings.Split(value, ":")
	if len(parts) > 2 || len(parts) == 0 {
		return fmt.Errorf("--interval 格式应为 基础间隔[:最大抖动]，例如 3m:1m")
	}

	base, err := time.ParseDuration(parts[0])
	if err != nil {
		return fmt.Errorf("--interval 基础间隔无效: %w", err)
	}
	jitter := time.Duration(0)
	if len(parts) == 2 {
		jitter, err = time.ParseDuration(parts[1])
		if err != nil {
			return fmt.Errorf("--interval 抖动无效: %w", err)
		}
	}
	f.base, f.jitter = base, jitter
	f.cfg.interval, f.cfg.jitter = base, jitter
	return nil
}

func parseFlags(args []string) (*config, *flag.FlagSet) {
	cfg := &config{}
	fs := newFlagSet(cfg, flag.ExitOnError)
	_ = fs.Parse(args)
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "floor-ratio" {
			cfg.floorRatioSet = true
		}
	})
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
	fmt.Fprint(out, `监控 ChatGPT Plus 的价格，低于阈值时 Telegram 通知。

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
		// 默认值为 0 / false 的开关不必写出来，是噪音。
		if d := f.DefValue; d != "" && d != "false" && d != "0" {
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
	case c.threshold <= 0:
		return fmt.Errorf("--threshold 必须大于 0")
	case c.interval < 0:
		return fmt.Errorf("--interval 不能为负数")
	case c.jitter < 0:
		return fmt.Errorf("--interval 抖动不能为负数")
	case c.interval == 0 && c.jitter != 0:
		return fmt.Errorf("--interval 为 0 时不能设置抖动")
	case c.sample <= 0:
		return fmt.Errorf("--sample 必须大于 0")
	case c.floorRatioSet && (c.floorRatio <= 0 || c.floorRatio > 1):
		return fmt.Errorf("--floor-ratio 必须在 0 和 1 之间")
	case c.top <= 0:
		return fmt.Errorf("--top 必须大于 0")
	case c.timeout <= 0:
		return fmt.Errorf("--timeout 必须大于 0")
	}
	return nil
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max) + 1))
}

func intervalText(interval, jitter time.Duration) string {
	if jitter == 0 {
		return interval.String()
	}
	return interval.String() + "~" + (interval + jitter).String()
}

func shortDuration(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}

// fetcher 取最便宜的 n 条报价。抽成函数类型是为了让测试能替换掉真实接口。
type fetcher func(ctx context.Context, n int) ([]priceai.Offer, error)

// check 跑一轮检查，返回更新后的状态。
func check(ctx context.Context, fetch fetcher, notify notifier, cfg *config, prev state.State) (state.State, error) {
	offers, err := fetch(ctx, cfg.sample)
	if err != nil {
		return reportFailure(ctx, notify, cfg, prev, err)
	}

	// 用中位数当参照系剔除掉明显不是同一档商品的报价，再看最便宜的那条。
	a := priceai.Analyze(offers, cfg.floorRatio)
	best, ok := a.Best()
	if !ok {
		return prev, fmt.Errorf("%d 条报价全低于地板线 %.2f", len(offers), a.Floor)
	}
	below := best.Price <= cfg.threshold
	logResult(cfg, a, best, below)

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
	if !send(ctx, notify, buildMessage(action, cfg, a, best, prev.LastAvg), action.String()) {
		return next, nil
	}
	next.LastNotify = now
	next.LastAvg = best.Price
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

func logResult(cfg *config, a priceai.Analysis, best priceai.Offer, below bool) {
	reached := "未达成"
	if below {
		reached = "已达成"
	}
	if cfg.floorRatioSet {
		log.Printf("最便宜的可信报价 %.2f 元（参考价位 %.2f，地板线 %.2f；剔除 %d 条异常），阈值 %.2f -> %s",
			best.Price, a.Median, a.Floor, len(a.Dropped), cfg.threshold, reached)
	} else {
		log.Printf("最便宜的报价 %.2f 元（参考价位 %.2f；未启用异常低价过滤），阈值 %.2f -> %s",
			best.Price, a.Median, cfg.threshold, reached)
	}
	if !cfg.verbose {
		return
	}
	for i, o := range a.Kept {
		if i >= cfg.top {
			break
		}
		log.Printf("  %d. %.2f 元 | %s | %s", i+1, o.Price, o.Store(), o.SourceTitle)
	}
	for _, o := range a.Dropped {
		log.Printf("  [剔除] %.2f 元 | %s | %s", o.Price, o.Store(), o.SourceTitle)
	}
}

func buildMessage(action state.Action, cfg *config, a priceai.Analysis, best priceai.Offer, prevBest float64) string {
	var b strings.Builder
	switch action {
	case state.AlertBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 降价提醒</b>\n\n")
	case state.RemindBelow:
		b.WriteString("🔻 <b>ChatGPT Plus 仍在低价</b>\n\n")
	case state.AlertRebound:
		b.WriteString("🔺 <b>ChatGPT Plus 价格回升</b>\n\n")
	}

	fmt.Fprintf(&b, "最低可信报价：<b>%.2f</b> 元（阈值 %.2f）\n", best.Price, cfg.threshold)
	fmt.Fprintf(&b, "参考价位 %.2f 元", a.Median)
	if n := len(a.Dropped); cfg.floorRatioSet && n > 0 {
		fmt.Fprintf(&b, "，已剔除 %d 条低于 %.2f 元的异常报价", n, a.Floor)
	}
	b.WriteString("\n")
	if action == state.AlertRebound && prevBest > 0 {
		fmt.Fprintf(&b, "上次通知时为 %.2f 元\n", prevBest)
	}

	b.WriteString("\n")
	for i, o := range a.Kept {
		if i >= cfg.top {
			break
		}
		fmt.Fprintf(&b, "%d. <b>%.2f</b> 元 · %s\n", i+1, o.Price, telegram.Escape(o.Store()))
		if t := o.SourceTitle; t != "" {
			fmt.Fprintf(&b, "<i>%s</i>\n", telegram.Escape(truncate(t, 50)))
		}
		// 链接单独一行且不缩进，Telegram 里直接可点，跳过去就是付款页。
		if o.URL != "" {
			fmt.Fprintf(&b, "%s\n", telegram.Escape(o.URL))
		}
		b.WriteString("\n")
	}
	fmt.Fprint(&b, priceai.ProductPage)
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
