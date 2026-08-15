package app

import (
	"fmt"
	"html"
	"strings"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/telegram"
)

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

	fmt.Fprintf(&b, "最低可信报价：<b>%.2f</b> 元（阈值 %.2f 元）\n", best.Price, cfg.threshold)
	fmt.Fprintf(&b, "参考价位：<b>%.2f</b> 元", a.Median)
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
		store := telegram.Escape(o.Store())
		if o.URL != "" {
			fmt.Fprintf(&b, "%d. <a href=\"%s\">%.2f 元 · %s</a>\n", i+1, html.EscapeString(o.URL), o.Price, store)
		} else {
			fmt.Fprintf(&b, "%d. %.2f 元 · %s\n", i+1, o.Price, store)
		}
		if t := o.SourceTitle; t != "" {
			fmt.Fprintf(&b, "<i>%s</i>\n", telegram.Escape(truncate(t, 50)))
		}
		b.WriteString("\n")
	}
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

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
