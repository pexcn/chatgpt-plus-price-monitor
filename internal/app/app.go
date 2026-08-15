package app

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
)

// Run 执行一次或持续运行价格监控，返回进程退出码。
func Run(args []string) int {
	log.SetFlags(log.Ldate | log.Ltime)
	cfg, fs := parseFlags(args)
	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n", err)
		fs.SetOutput(os.Stderr)
		fs.Usage()
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 1
	transport.IdleConnTimeout = cfg.interval + cfg.jitter + time.Minute
	httpc := &http.Client{Timeout: cfg.timeout, Transport: transport}
	defer transport.CloseIdleConnections()
	fetch := func(ctx context.Context, n int) ([]priceai.Offer, error) {
		return priceai.Cheapest(ctx, httpc, n)
	}
	notify := newNotifier(httpc)
	if notify == nil {
		log.Print("未设置 TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID，只打印结果，不发送通知")
	}

	if cfg.interval == 0 {
		if _, err := check(ctx, fetch, notify, cfg, state.State{}); err != nil {
			log.Printf("检查失败: %v", err)
			return 1
		}
		return 0
	}

	log.Printf("开始监控（每 %s 检查一次，最便宜的可信报价 <= %.2f 元时通知）",
		intervalText(cfg.interval, cfg.jitter), cfg.threshold)

	var st state.State
	for {
		next, err := check(ctx, fetch, notify, cfg, st)
		if err != nil {
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
			return 0
		case <-timer.C:
		}
	}
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max) + 1))
}
