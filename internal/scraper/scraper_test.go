package scraper

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const listingHTML = `<html><body>
<div class="list">
  <div class="row"><span class="name">卖家A</span><span class="price">¥8.80</span></div>
  <div class="row"><span class="name">卖家B</span><span class="price">¥9.50</span></div>
  <div class="row"><span class="name">卖家C</span><span class="price">￥10.00</span></div>
  <div class="row"><span class="name">卖家D</span><span class="price">12.30 元</span></div>
  <div class="row"><span class="name">卖家E</span><span class="price">¥15</span></div>
  <div class="row"><span class="name">卖家F</span><span class="price">¥20.00</span></div>
</div>
<footer>© 2026 共 1024 条记录</footer>
</body></html>`

const nextDataHTML = `<html><body><div id="__next">loading</div>
<script id="__NEXT_DATA__" type="application/json">
{"props":{"pageProps":{"offers":[
 {"id":9001,"seller":"卖家A","price":8.8,"currency":"CNY"},
 {"id":9002,"seller":"卖家B","price":"9.50","currency":"CNY"},
 {"id":9003,"seller":"卖家C","price":10,"currency":"CNY"},
 {"id":9004,"seller":"卖家D","price":12.3,"currency":"CNY"},
 {"id":9005,"seller":"卖家E","price":15,"currency":"CNY"}
]}},"buildId":"abc123"}
</script></body></html>`

func TestExtractViaSelector(t *testing.T) {
	res := mustFetch(t, listingHTML, Options{Selector: ".price"})
	if res.Method != "selector" {
		t.Errorf("Method = %q, 期望 selector", res.Method)
	}
	want := []float64{8.80, 9.50, 10.00, 12.30, 15, 20.00}
	if !reflect.DeepEqual(res.Prices, want) {
		t.Errorf("Prices = %v, 期望 %v", res.Prices, want)
	}
}

func TestExtractViaEmbeddedJSONPreservesOrder(t *testing.T) {
	res := mustFetch(t, nextDataHTML, Options{})
	if res.Method != "embedded-json" {
		t.Fatalf("Method = %q, 期望 embedded-json", res.Method)
	}
	// 顺序是关键：map 解码会打乱顺序，"前 N 个"就失去意义。
	want := []float64{8.8, 9.50, 10, 12.3, 15}
	if !reflect.DeepEqual(res.Prices, want) {
		t.Errorf("Prices = %v, 期望 %v", res.Prices, want)
	}
}

func TestExtractViaTextRegexIgnoresNoise(t *testing.T) {
	res := mustFetch(t, listingHTML, Options{})
	if res.Method != "text-regex" {
		t.Fatalf("Method = %q, 期望 text-regex", res.Method)
	}
	want := []float64{8.80, 9.50, 10.00, 12.30, 15, 20.00}
	if !reflect.DeepEqual(res.Prices, want) {
		// 页脚的 "2026" 和 "1024" 没有货币标记，不应被当成价格。
		t.Errorf("Prices = %v, 期望 %v", res.Prices, want)
	}
}

// 页面前端渲染时，用户可以把 -url 直接指向站点的 JSON 接口。
func TestFetchJSONAPI(t *testing.T) {
	body := `{"code":0,"data":{"list":[
	 {"id":1,"unitPrice":8.8,"currency":"CNY"},
	 {"id":2,"unitPrice":"9.50","currency":"CNY"},
	 {"id":3,"unitPrice":10,"currency":"CNY"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.Client(), Options{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if res.Method != "json-api" {
		t.Errorf("Method = %q, 期望 json-api", res.Method)
	}
	if want := []float64{8.8, 9.50, 10}; !reflect.DeepEqual(res.Prices, want) {
		t.Errorf("Prices = %v, 期望 %v", res.Prices, want)
	}
}

func TestLooksLikeJSON(t *testing.T) {
	cases := []struct {
		ct   string
		body string
		want bool
	}{
		{"application/json", `{"a":1}`, true},
		{"application/json; charset=utf-8", `[]`, true},
		{"", "  \n{\"a\":1}", true},
		{"", "[1,2]", true},
		{"text/html; charset=utf-8", "<html><body>¥9</body></html>", false},
		{"", "<!doctype html><html></html>", false},
	}
	for _, c := range cases {
		if got := looksLikeJSON(c.ct, []byte(c.body)); got != c.want {
			t.Errorf("looksLikeJSON(%q, %q) = %v, 期望 %v", c.ct, c.body, got, c.want)
		}
	}
}

func TestExtractFailsLoudlyOnEmptyPage(t *testing.T) {
	// 前端渲染的页面抓下来只有骨架，必须报错而不是静默返回空。
	_, err := fetchFrom(t, `<html><body><div id="app"></div></body></html>`, Options{})
	if err == nil {
		t.Fatal("期望报错，实际成功")
	}
}

func TestExtractFailsOnBadSelector(t *testing.T) {
	_, err := fetchFrom(t, listingHTML, Options{Selector: ".nonexistent"})
	if err == nil {
		t.Fatal("选择器无命中时期望报错")
	}
}

func TestFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.Client(), Options{URL: srv.URL}); err == nil {
		t.Fatal("HTTP 403 时期望报错")
	}
}

func TestFetchDump(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "page.html")
	mustFetch(t, listingHTML, Options{Selector: ".price", DumpPath: dump})
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("读取 dump 失败: %v", err)
	}
	if string(b) != listingHTML {
		t.Error("dump 内容与原始 HTML 不一致")
	}
}

func TestTopN(t *testing.T) {
	prices := []float64{12, 8, 20, 9, 15, 7}
	tests := []struct {
		name    string
		n       int
		sortAsc bool
		want    []float64
	}{
		{"页面顺序前3", 3, false, []float64{12, 8, 20}},
		{"最便宜的3个", 3, true, []float64{7, 8, 9}},
		{"N 超过总数", 100, false, []float64{12, 8, 20, 9, 15, 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TopN(prices, tt.n, tt.sortAsc); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TopN() = %v, 期望 %v", got, tt.want)
			}
		})
	}
	// TopN 不能改动调用方的切片。
	if prices[0] != 12 {
		t.Errorf("TopN 修改了入参切片: %v", prices)
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{8.80, 9.50, 10.00, 12.30, 15})
	if math.Abs(s.Avg-11.12) > 1e-9 {
		t.Errorf("Avg = %v, 期望 11.12", s.Avg)
	}
	if s.Min != 8.80 || s.Max != 15 || s.Count != 5 {
		t.Errorf("Min/Max/Count = %v/%v/%v", s.Min, s.Max, s.Count)
	}
	if got := Summarize(nil); got.Count != 0 {
		t.Errorf("空切片应返回零值, 得到 %+v", got)
	}
}

func TestParsePrice(t *testing.T) {
	tests := []struct {
		in        string
		allowBare bool
		want      float64
		ok        bool
	}{
		{"¥8.80", false, 8.80, true},
		{"￥12", false, 12, true},
		{"9.90元", false, 9.90, true},
		{"9.90 元", false, 9.90, true},
		{"CNY 15.50", false, 15.50, true},
		{"售价 ¥ 7.5 起", false, 7.5, true},
		{"8.80", false, 0, false},  // 无货币标记且不允许裸数字
		{"8.80", true, 8.80, true}, // 选择器模式下允许
		{"暂无报价", true, 0, false},   // 没有数字
		{"2026年", false, 0, false}, // 不是价格
	}
	for _, tt := range tests {
		got, ok := ParsePrice(tt.in, tt.allowBare)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("ParsePrice(%q, %v) = %v, %v; 期望 %v, %v", tt.in, tt.allowBare, got, ok, tt.want, tt.ok)
		}
	}
}

func TestIsPriceKey(t *testing.T) {
	yes := []string{"price", "Price", "unitPrice", "sale_price", "discountPrice", "amount", "totalAmount", "minPrice", "售价", "价格", "cost"}
	// priceUnit 是币种单位、priceId 是主键，都不是金额本身。
	no := []string{"priceId", "price_url", "priceCount", "currency", "priceType", "priceUnit", "priceName", "id", "sellerName", ""}
	for _, k := range yes {
		if !isPriceKey(k) {
			t.Errorf("isPriceKey(%q) = false, 期望 true", k)
		}
	}
	for _, k := range no {
		if isPriceKey(k) {
			t.Errorf("isPriceKey(%q) = true, 期望 false", k)
		}
	}
}

func mustFetch(t *testing.T, html string, opts Options) *Result {
	t.Helper()
	res, err := fetchFrom(t, html, opts)
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	return res
}

func fetchFrom(t *testing.T, html string, opts Options) (*Result, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("请求缺少 User-Agent")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	opts.URL = srv.URL
	return Fetch(context.Background(), srv.Client(), opts)
}
