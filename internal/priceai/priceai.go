// Package priceai 封装 priceai.cc 的报价接口。
package priceai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const (
	// ProductPage 是放进通知里的人类可读页面。
	ProductPage = "https://priceai.cc/products/chatgpt-plus"

	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"
)

// apiEndpoint 是页面背后的报价接口，测试里会被替换成本地服务器。
var apiEndpoint = "https://priceai.cc/api/products/chatgpt-plus/offers"

// Offer 是一条报价。接口返回的字段远不止这些，这里只取用得上的。
type Offer struct {
	ID              string  `json:"id"`
	SourceName      string  `json:"sourceName"`
	SourceStoreName string  `json:"sourceStoreName"`
	SourceTitle     string  `json:"sourceTitle"`
	Price           float64 `json:"price"`
	Currency        string  `json:"currency"`
	URL             string  `json:"url"`
	Status          string  `json:"status"`
	StockCount      *int    `json:"stockCount"`
	EffectiveStatus string  `json:"effectiveStatus"`
}

// Store 返回展示用的店铺名。
func (o Offer) Store() string {
	if o.SourceStoreName != "" {
		return o.SourceStoreName
	}
	return o.SourceName
}

type response struct {
	Offers []Offer `json:"offers"`
	Total  int     `json:"total"`
}

// Cheapest 取最便宜的 n 条报价。
//
// 接口默认按价格升序返回，所以 offset=0&limit=n 拿到的就是最便宜的 n 条，
// 也正是页面上显示的前 n 条，不需要本地再排序。
func Cheapest(ctx context.Context, c *http.Client, n int) ([]Offer, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(n))
	q.Set("offset", "0")
	endpoint := apiEndpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// 模拟页面发起的同源 AJAX 请求的基础头部；不伪造 sec-* 浏览器指纹头。
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", ProductPage)
	req.Header.Set("Cookie", "priceai_account_auth_hint=anonymous")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求报价接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("报价接口返回 HTTP %d", resp.StatusCode)
	}

	return parse(body, n)
}

// parse 解析接口响应并做基本校验。
func parse(body []byte, n int) ([]Offer, error) {
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析接口响应失败: %w", err)
	}

	// 样本不足说明接口改了或者被限流，此时算出的均价不可信，
	// 宁可报错也不要误报一条"降价了"。
	if len(r.Offers) < n {
		return nil, fmt.Errorf("接口只返回了 %d 条报价，少于需要的 %d 条", len(r.Offers), n)
	}
	offers := r.Offers[:n]

	for i, o := range offers {
		if o.Price <= 0 {
			return nil, fmt.Errorf("第 %d 条报价的价格异常: %v", i+1, o.Price)
		}
		// 阈值是按人民币比的，混进别的币种会把均价算错。
		if o.Currency != "CNY" {
			return nil, fmt.Errorf("第 %d 条报价的币种是 %s，不是 CNY", i+1, o.Currency)
		}
	}
	return offers, nil
}
