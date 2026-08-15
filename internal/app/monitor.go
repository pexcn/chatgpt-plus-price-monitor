package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/telegram"
)

// notifier 发送一条通知。没配置 Telegram 凭据时为 nil，此时只记日志。
type notifier interface {
	Send(ctx context.Context, text string) error
}

type fetcher func(ctx context.Context, n int) ([]priceai.Offer, error)

func newNotifier(httpc *http.Client) notifier {
	token, chatID := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return nil
	}
	return &telegram.Client{Token: token, ChatID: chatID, HTTP: httpc}
}

// check 跑一轮检查，返回更新后的状态。
func check(ctx context.Context, fetch fetcher, notify notifier, cfg *config, prev state.State) (state.State, error) {
	offers, err := fetch(ctx, cfg.sample)
	if err != nil {
		return reportFailure(ctx, notify, cfg, prev, err)
	}

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

func reportFailure(ctx context.Context, notify notifier, cfg *config, prev state.State, cause error) (state.State, error) {
	next := prev
	next.Failures++
	if notify == nil {
		return next, cause
	}
	if cfg.failThreshold > 0 && next.Failures >= cfg.failThreshold && !prev.FailNotified {
		if send(ctx, notify, buildFailureMessage(next.Failures, cause), "抓取失败告警") {
			next.FailNotified = true
		}
	}
	return next, cause
}

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
