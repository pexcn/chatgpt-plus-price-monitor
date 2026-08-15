package app

import (
	"strings"
	"testing"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/priceai"
	"github.com/pexcn/chatgpt-plus-price-monitor/internal/state"
)

func TestBuildMessageLinksOfferLabel(t *testing.T) {
	offer := priceai.Offer{
		SourceStoreName: `鹰鹰小铺 & 商店`,
		SourceTitle:     "ChatGPT Plus 成品号",
		Price:           37.08,
		URL:             `https://example.com/item?a=1&b="2"`,
	}
	analysis := priceai.Analysis{Median: 80.70, Kept: []priceai.Offer{offer}}

	got := buildMessage(state.AlertBelow, &config{threshold: 58, top: 1}, analysis, offer, 0)
	wantLink := `<a href="https://example.com/item?a=1&amp;b=&#34;2&#34;">37.08 元 · 鹰鹰小铺 &amp; 商店</a>`
	if !strings.Contains(got, wantLink) {
		t.Errorf("报价标题未包含预期链接:\n%s", got)
	}
	if strings.Contains(got, `</a>\n<b>37.08</b>`) || strings.Contains(got, `"><b>37.08</b>`) {
		t.Errorf("报价价格不应加粗:\n%s", got)
	}
	if strings.Contains(got, "\nhttps://example.com/item") {
		t.Errorf("报价 URL 不应再单独显示:\n%s", got)
	}
	if strings.Contains(got, priceai.ProductPage) {
		t.Errorf("报价消息不应包含产品页链接:\n%s", got)
	}
}
