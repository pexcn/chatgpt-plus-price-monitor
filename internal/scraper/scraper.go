// Package scraper 负责抓取页面并从中提取价格列表。
package scraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/PuerkitoBio/goquery"
)

const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// Options 控制一次抓取的行为。
type Options struct {
	URL string
	// Selector 是可选的 CSS 选择器。给了就只认它命中的元素，
	// 不再走自动识别，结果最稳定。
	Selector string
	// UserAgent 覆盖默认 UA。
	UserAgent string
	// DumpPath 非空时把原始 HTML 落盘，方便排查选择器。
	DumpPath string
}

// Result 是一次抓取的产物。
type Result struct {
	// Prices 按页面出现顺序排列。
	Prices []float64
	// Method 说明价格是用哪条策略提取到的，便于确认抓对了地方。
	Method string
}

// Fetch 下载页面并提取价格。
func Fetch(ctx context.Context, c *http.Client, opts Options) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return nil, err
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求页面失败: %w", err)
	}
	defer resp.Body.Close()

	// 限制读取体积，避免异常响应把内存吃满。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("页面返回 HTTP %d", resp.StatusCode)
	}

	if opts.DumpPath != "" {
		if err := os.WriteFile(opts.DumpPath, body, 0o644); err != nil {
			return nil, fmt.Errorf("写入 dump 文件失败: %w", err)
		}
	}

	// 页面是前端渲染时，把 -url 直接指向站点的 JSON 接口是最省事的办法，
	// 所以这里也接受 JSON 响应。
	if opts.Selector == "" && looksLikeJSON(resp.Header.Get("Content-Type"), body) {
		if prices := pricesFromJSON(body); len(prices) > 0 {
			return &Result{Prices: prices, Method: "json-api"}, nil
		}
		return nil, fmt.Errorf("响应是 JSON，但没有找到价格字段（字段名需以 price/amount/cost/价格 等结尾）")
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 失败: %w", err)
	}
	return extract(doc, opts.Selector)
}

// looksLikeJSON 判断响应体是不是一份 JSON 文档。
func looksLikeJSON(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return true
	}
	trimmed := strings.TrimLeftFunc(string(body), unicode.IsSpace)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

// extract 按 选择器 > 内嵌 JSON > 正文正则 的优先级提取价格。
func extract(doc *goquery.Document, selector string) (*Result, error) {
	if selector != "" {
		var prices []float64
		doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
			// 选择器是用户明确指定的，命中的文本大概率就是价格本身，
			// 所以这里允许裸数字。
			if p, ok := ParsePrice(strings.TrimSpace(s.Text()), true); ok {
				prices = append(prices, p)
			}
		})
		if len(prices) == 0 {
			return nil, fmt.Errorf("选择器 %q 没有匹配到任何价格", selector)
		}
		return &Result{Prices: prices, Method: "selector"}, nil
	}

	// 站点多为 SSR 框架，价格通常在内嵌的 JSON 里，比正文正则更可靠。
	var jsonPrices []float64
	doc.Find(`script#__NEXT_DATA__, script#__NUXT_DATA__, script[type="application/json"]`).EachWithBreak(
		func(_ int, s *goquery.Selection) bool {
			if p := pricesFromJSON([]byte(s.Text())); len(p) > 0 {
				jsonPrices = p
				return false
			}
			return true
		})
	if len(jsonPrices) > 0 {
		return &Result{Prices: jsonPrices, Method: "embedded-json"}, nil
	}

	// 兜底：扫正文里带 ¥ / 元 标记的数字。
	clone := doc.Clone()
	clone.Find("script, style, noscript").Remove()
	if prices := pricesFromText(clone.Text()); len(prices) > 0 {
		return &Result{Prices: prices, Method: "text-regex"}, nil
	}

	return nil, fmt.Errorf("页面里没有找到任何价格（可能是前端渲染或结构变了）；" +
		"用 -dump page.html 保存页面后确认，再用 -selector 指定选择器")
}

// TopN 取前 n 个价格。sortAsc 为真时先按价格升序，即"最便宜的 n 个"；
// 否则按页面原始顺序。
func TopN(prices []float64, n int, sortAsc bool) []float64 {
	out := make([]float64, len(prices))
	copy(out, prices)
	if sortAsc {
		sort.Float64s(out)
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// Stats 汇总一组价格。
type Stats struct {
	Avg   float64
	Min   float64
	Max   float64
	Count int
}

// Summarize 计算均价与极值。传入空切片返回零值。
func Summarize(prices []float64) Stats {
	if len(prices) == 0 {
		return Stats{}
	}
	s := Stats{Min: prices[0], Max: prices[0], Count: len(prices)}
	var sum float64
	for _, p := range prices {
		sum += p
		if p < s.Min {
			s.Min = p
		}
		if p > s.Max {
			s.Max = p
		}
	}
	s.Avg = sum / float64(len(prices))
	return s
}
