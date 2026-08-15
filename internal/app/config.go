package app

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
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

type option struct{ short, long string }

// options 决定 --help 里的顺序，按重要程度排列。
var options = []option{
	{"t", "threshold"},
	{"i", "interval"},
	{"s", "sample"},
	{"f", "floor-ratio"},
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
	fs.BoolVar(&cfg.verbose, "verbose", false, "显示详细日志，输出每条报价的店铺和标题")

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
	_ = fs.Parse(translateShortArgs(args))
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "floor-ratio" {
			cfg.floorRatioSet = true
		}
	})
	return cfg, fs
}

func translateShortArgs(in []string) []string {
	mapping := make(map[string]string, len(options))
	for _, option := range options {
		if option.short != "" {
			mapping[option.short] = option.long
		}
	}

	out := make([]string, 0, len(in))
	for i, arg := range in {
		if arg == "--" {
			return append(append(out, arg), in[i+1:]...)
		}
		if strings.HasPrefix(arg, "--") || !strings.HasPrefix(arg, "-") || len(arg) < 2 {
			out = append(out, arg)
			continue
		}

		rest := arg[1:]
		if eq := strings.IndexByte(rest, '='); eq >= 0 {
			if long, ok := mapping[rest[:eq]]; ok {
				out = append(out, "--"+long+rest[eq:])
			} else {
				out = append(out, arg)
			}
			continue
		}

		long, ok := mapping[rest[:1]]
		if !ok {
			out = append(out, arg)
			continue
		}
		if attached := rest[1:]; attached != "" {
			out = append(out, "--"+long+"="+attached)
		} else {
			out = append(out, "--"+long)
		}
	}
	return out
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
		if d := f.DefValue; d != "" && d != "false" && d != "0" {
			help += fmt.Sprintf(" (default %s)", shortFlagDefault(f, d))
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

func shortFlagDefault(f *flag.Flag, value string) string {
	getter, ok := f.Value.(flag.Getter)
	if !ok {
		return value
	}
	if _, ok := getter.Get().(time.Duration); !ok {
		return value
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return value
	}
	return shortDuration(d)
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
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}
